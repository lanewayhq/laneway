// Package provision is the Provisioner: it takes each Worktree through setup,
//
//	Discovered → Provisioning (database → Test Database → env → setup steps) → Ready | Failed
//
// one Worktree at a time per directory, in the background. What setup does is
// a list of Steps the daemon builds (internal/daemon/provision.go); this
// package only runs them, records the outcome and lets callers wait for it.
//
// Ready and Failed are persisted, so a Worktree set up once isn't set up again
// when the daemon restarts, and a failure stays visible until setup is
// retried. A setup the daemon was stopped in the middle of is forgotten and
// runs again from the start.
package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// State is where a Worktree is in setup.
type State string

const (
	// Discovered: the Worktree is known but setup hasn't started.
	Discovered State = "discovered"
	// Provisioning: setup is running.
	Provisioning State = "provisioning"
	// Ready: every step succeeded.
	Ready State = "ready"
	// Failed: a step failed; Status says which and what it printed.
	Failed State = "failed"
)

// Step is one thing setup does, such as applying the Env Patch or running
// one on_create command.
type Step struct {
	// Name identifies the step to the user when it fails, such as "env"
	// or "on_create: composer install".
	Name string
	// Run does the step. Anything written to out is kept and shown if the
	// step fails. It must stop when ctx is cancelled.
	Run func(ctx context.Context, out io.Writer) error
}

// Plan works out a Worktree's steps when its setup starts, so config (and the
// environment, such as the Runtimes) is read then. An error fails setup at
// the step "config". It must stop when ctx is cancelled.
type Plan func(ctx context.Context) ([]Step, error)

// Status is a Worktree's setup state.
type Status struct {
	State State `json:"state"`
	// Step is the step running (Provisioning) or the one that failed
	// (Failed).
	Step string `json:"step,omitempty"`
	// Error is why Step failed.
	Error string `json:"error,omitempty"`
	// Output is the last OutputLines lines the failed step printed.
	Output []string `json:"output,omitempty"`
	// Warnings are what steps reported with Warn, such as a database that
	// couldn't be cloned, in order.
	Warnings []string `json:"warnings,omitempty"`
}

// Done reports whether setup has finished, one way or the other.
func (s Status) Done() bool { return s.State == Ready || s.State == Failed }

// OutputLines is how much of a failed step's output is kept.
const OutputLines = 50

// ErrForgotten is returned by Wait when the Worktree is forgotten (removed)
// while waiting.
var ErrForgotten = errors.New("the Worktree was removed")

// ErrClosed is returned by Wait once the Provisioner is closed.
var ErrClosed = errors.New("the Portby daemon is shutting down")

// Provisioner runs and records setup for Worktrees, keyed by directory.
type Provisioner struct {
	path string
	log  *slog.Logger

	ctx   context.Context // cancelled by Close
	close context.CancelFunc
	jobs  sync.WaitGroup

	mu       sync.Mutex
	statuses map[string]Status
	running  map[string]*job
	changed  chan struct{} // closed and replaced on every change
	closed   bool
}

type job struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Open loads the setup states recorded at path (a JSON file the Provisioner
// owns) and returns a Provisioner that records them there.
func Open(path string, log *slog.Logger) (*Provisioner, error) {
	p := &Provisioner{
		path:     path,
		log:      log,
		statuses: map[string]Status{},
		running:  map[string]*job{},
		changed:  make(chan struct{}),
	}
	p.ctx, p.close = context.WithCancel(context.Background())
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading setup states: %w", err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("reading setup states %s: %w", path, err)
	}
	for dir, st := range f.Worktrees {
		if st.Done() {
			p.statuses[dir] = st
		}
	}
	return p, nil
}

// file is the on-disk format.
type file struct {
	Version   int               `json:"version"`
	Worktrees map[string]Status `json:"worktrees"`
}

// Status returns the setup state of the Worktree at dir.
func (p *Provisioner) Status(dir string) Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	if st, ok := p.statuses[dir]; ok {
		return st
	}
	return Status{State: Discovered}
}

// Begin starts setting up the Worktree at dir in the background with the
// steps plan gives, unless setup is running or has finished. With retry, a
// Failed setup runs again from the first step.
func (p *Provisioner) Begin(dir string, plan Plan, retry bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.running[dir] != nil {
		return
	}
	switch p.statuses[dir].State {
	case Ready:
		return
	case Failed:
		if !retry {
			return
		}
	}
	ctx, cancel := context.WithCancel(p.ctx)
	j := &job{cancel: cancel, done: make(chan struct{})}
	p.running[dir] = j
	p.setLocked(dir, Status{State: Provisioning})
	p.jobs.Go(func() {
		defer close(j.done)
		defer cancel()
		st := p.run(ctx, dir, j, plan)
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.running[dir] != j {
			return // forgotten meanwhile
		}
		delete(p.running, dir)
		if ctx.Err() != nil {
			// Stopped, not failed: it starts again next time.
			delete(p.statuses, dir)
			p.notifyLocked()
			return
		}
		p.setLocked(dir, st)
		p.saveLocked()
	})
}

// run runs the plan's steps in order and returns the outcome.
func (p *Provisioner) run(ctx context.Context, dir string, j *job, plan Plan) Status {
	warned := &warnings{}
	ctx = context.WithValue(ctx, warningsKey{}, warned)
	steps, err := plan(ctx)
	if err != nil {
		return Status{State: Failed, Step: "config", Error: err.Error(), Warnings: warned.list()}
	}
	for _, step := range steps {
		p.mu.Lock()
		if p.running[dir] == j {
			p.setLocked(dir, Status{State: Provisioning, Step: step.Name, Warnings: warned.list()})
		}
		p.mu.Unlock()
		out := &tail{}
		if err := step.Run(ctx, out); err != nil {
			if ctx.Err() == nil {
				p.log.Error("Worktree setup failed", "dir", dir, "step", step.Name, "err", err)
			}
			return Status{State: Failed, Step: step.Name, Error: err.Error(), Output: out.Lines(), Warnings: warned.list()}
		}
	}
	for _, w := range warned.list() {
		p.log.Warn("Worktree setup", "dir", dir, "warning", w)
	}
	p.log.Info("Worktree is Ready", "dir", dir)
	return Status{State: Ready, Warnings: warned.list()}
}

// Warn records a warning for the developer from a running setup step (ctx is
// the one the step was given): something worked, but not as configured, such
// as a database created empty because there was none to clone. Warnings are
// kept in the Worktree's Status, whether setup ends Ready or Failed.
// Outside a setup step it does nothing.
func Warn(ctx context.Context, warning string) {
	if w, ok := ctx.Value(warningsKey{}).(*warnings); ok {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.warnings = append(w.warnings, warning)
	}
}

type warningsKey struct{}

// warnings are the ones a setup's steps reported.
type warnings struct {
	mu       sync.Mutex
	warnings []string
}

func (w *warnings) list() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.warnings)
}

// Wait blocks until the Worktree at dir is Ready or Failed and returns its
// status. It returns early with ctx's error, ErrForgotten if the Worktree is
// forgotten, or ErrClosed once the Provisioner closes. A Worktree whose setup
// hasn't begun is waited for until it begins and ends.
func (p *Provisioner) Wait(ctx context.Context, dir string) (Status, error) {
	p.mu.Lock()
	_, known := p.statuses[dir]
	p.mu.Unlock()
	for {
		p.mu.Lock()
		st, ok := p.statuses[dir]
		changed, closed := p.changed, p.closed
		p.mu.Unlock()
		switch {
		case ok && st.Done():
			return st, nil
		case closed:
			return Status{}, ErrClosed
		case known && !ok:
			return Status{}, ErrForgotten
		}
		known = known || ok
		select {
		case <-changed:
		case <-ctx.Done():
			return Status{}, ctx.Err()
		}
	}
}

// Forget stops the Worktree's setup if it is running, waits for it to stop
// and drops its record, for a Worktree that was removed.
func (p *Provisioner) Forget(dir string) {
	p.mu.Lock()
	j := p.running[dir]
	delete(p.running, dir)
	_, had := p.statuses[dir]
	delete(p.statuses, dir)
	if had {
		p.notifyLocked()
		p.saveLocked()
	}
	p.mu.Unlock()
	if j != nil {
		j.cancel()
		<-j.done
	}
}

// Go runs plan's steps in the background, in order, stopping at the first
// failure, which is logged. It is for steps that aren't part of any
// Worktree's setup, such as on_remove. Close stops them.
func (p *Provisioner) Go(what string, plan Plan) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.jobs.Go(func() {
		steps, err := plan(p.ctx)
		if err != nil {
			p.log.Error(what+" failed", "step", "config", "err", err)
			return
		}
		for _, step := range steps {
			out := &tail{}
			if err := step.Run(p.ctx, out); err != nil {
				p.log.Error(what+" failed", "step", step.Name, "err", err, "output", strings.Join(out.Lines(), "\n"))
				return
			}
		}
	})
}

// Close stops every running setup and on_remove step and waits for them.
// Setups it stops run again when the daemon next starts.
func (p *Provisioner) Close() {
	p.mu.Lock()
	p.closed = true
	p.notifyLocked()
	p.mu.Unlock()
	p.close()
	p.jobs.Wait()
}

func (p *Provisioner) setLocked(dir string, st Status) {
	p.statuses[dir] = st
	p.notifyLocked()
}

func (p *Provisioner) notifyLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// saveLocked writes the finished setups to disk (atomically), logging
// failures: the states in memory stay right either way.
func (p *Provisioner) saveLocked() {
	f := file{Version: 1, Worktrees: map[string]Status{}}
	for dir, st := range p.statuses {
		if st.Done() {
			f.Worktrees[dir] = st
		}
	}
	if err := writeFile(p.path, f); err != nil {
		p.log.Error("recording setup states", "err", err)
	}
}

func writeFile(path string, f file) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".setup-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
