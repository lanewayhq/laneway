package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/gitrepo"
	"github.com/mrsafalpiya/portby.dev/internal/proxy"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
)

// control carries out `portby up`, `down` or `restart` (action) on the
// Worktree req names, or on one of its Processes, and returns the Worktree
// afterwards.
func (d *Daemon) control(ctx context.Context, action string, req api.ControlRequest) (api.Worktree, error) {
	if action != "up" && action != "down" && action != "restart" {
		return api.Worktree{}, &api.Error{Code: api.CodeBadRequest, Message: fmt.Sprintf("unknown action %q", action)}
	}
	p, checkout, err := d.worktreeAt(ctx, req.Dir)
	if err != nil {
		return api.Worktree{}, err
	}
	w, err := d.pickWorktree(p, checkout, req.Worktree)
	if err != nil {
		return api.Worktree{}, err
	}
	d.servedMu.Lock()
	s, ok := d.served[w.Domain]
	d.servedMu.Unlock()
	if !ok {
		return api.Worktree{}, &api.Error{Code: api.CodeUnknownWorktree, Message: fmt.Sprintf("the Worktree at %s isn't served yet", w.Path)}
	}
	cfg, err := recipe.ResolveDir(s.dir)
	if err != nil {
		return api.Worktree{}, &api.Error{Code: api.CodeInvalidConfig, Message: err.Error()}
	}
	var proc *recipe.Process
	if req.Process != "" {
		var names []string
		for i, pr := range cfg.Processes {
			names = append(names, pr.Name)
			if pr.Name == req.Process {
				proc = &cfg.Processes[i]
			}
		}
		if proc == nil {
			return api.Worktree{}, unknownProcess(req.Process, names)
		}
	}
	switch action {
	case "up":
		err = d.up(ctx, s, proc)
	case "down":
		d.down(ctx, s, proc)
	case "restart":
		err = d.restart(ctx, s, cfg, proc)
	}
	if err != nil {
		return api.Worktree{}, err
	}
	return d.withState(w), nil
}

// pickWorktree finds the Worktree of p that name refers to: git's name for
// it, its Domain or its path, or the Project's name for the main Worktree.
// An empty name is the checkout's own Worktree.
func (d *Daemon) pickWorktree(p registry.Project, checkout gitrepo.Checkout, name string) (api.Worktree, error) {
	worktrees := d.apiProject(p).Worktrees
	// Paths are compared as the Registry keeps them: with symlinks resolved.
	if filepath.IsAbs(name) {
		if real, err := filepath.EvalSymlinks(name); err == nil {
			name = real
		}
	}
	var names []string
	for _, w := range worktrees {
		switch {
		case name == "" && w.Path == checkout.Dir,
			name != "" && (name == w.Domain || filepath.Clean(name) == w.Path),
			name != "" && w.Main && name == p.Name,
			name != "" && !w.Main && name == w.Name:
			return w, nil
		}
		names = append(names, cmp.Or(w.Name, p.Name))
	}
	if name == "" {
		return api.Worktree{}, &api.Error{Code: api.CodeUnknownWorktree, Message: fmt.Sprintf("the Worktree at %s isn't served yet", checkout.Dir)}
	}
	return api.Worktree{}, &api.Error{
		Code:    api.CodeUnknownWorktree,
		Message: fmt.Sprintf("Project %q has no Worktree %q; its Worktrees are %s", p.Name, name, strings.Join(names, ", ")),
	}
}

// up starts proc, or the whole Worktree if proc is nil. A manual Process
// starts on its own. Lazy and always Processes run with their Worktree, so
// for them the Worktree starts the way a request starts it, also when it is
// already running: that starts any of its Processes that stopped or failed.
// It waits for the Primary Process to accept connections.
func (d *Daemon) up(ctx context.Context, s *served, proc *recipe.Process) error {
	if proc != nil && proc.StartMode == recipe.Manual {
		s.lifecycle.Lock()
		defer s.lifecycle.Unlock()
		d.applyServedEnvPatch(s)
		cfg, err := d.resolveWorktree(ctx, s.dir)
		if err != nil {
			return &api.Error{Code: api.CodeInvalidConfig, Message: err.Error()}
		}
		_, err = d.startProcess(s, d.specs(s, cfg, []recipe.Process{*proc})[0], true)
		return err
	}
	s.mu.Lock()
	if s.start != nil && isDone(s.start) {
		s.start = nil
	}
	s.mu.Unlock()
	st := d.starting(s)
	select {
	case <-st.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if se, ok := errors.AsType[*proxy.StartError](st.err); ok {
		what := "exited before it accepted connections"
		if se.TimedOut {
			what = "didn't accept connections in time"
		}
		return &api.Error{Code: api.CodeStartFailed, Message: fmt.Sprintf(
			"the Primary Process %q %s (%v); see its output with `portby logs %s`", se.Process, what, se.Err, se.Process)}
	}
	if st.err != nil {
		return &api.Error{Code: api.CodeStartFailed, Message: st.err.Error()}
	}
	return nil
}

// down stops proc, or every Process of the Worktree if proc is nil. Either
// way the next request starts what it needs again.
func (d *Daemon) down(ctx context.Context, s *served, proc *recipe.Process) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if proc != nil {
		d.supervisor.Stop(ctx, s.group, proc.Name)
		return
	}
	s.mu.Lock()
	s.start, s.idle = nil, false
	s.mu.Unlock()
	s.activity.Reset()
	d.supervisor.StopGroup(ctx, s.group)
}

// restart stops and starts proc, or the whole Worktree if proc is nil. A
// whole Worktree comes back with the manual Processes that were running.
func (d *Daemon) restart(ctx context.Context, s *served, cfg recipe.Resolved, proc *recipe.Process) error {
	if proc != nil {
		d.down(ctx, s, proc)
		return d.up(ctx, s, proc)
	}
	var manual []recipe.Process
	for _, pr := range cfg.Processes {
		if _, running := d.supervisor.Process(s.group, pr.Name); running && pr.StartMode == recipe.Manual {
			manual = append(manual, pr)
		}
	}
	d.down(ctx, s, nil)
	if err := d.up(ctx, s, nil); err != nil {
		return err
	}
	for _, pr := range manual {
		if err := d.up(ctx, s, &pr); err != nil {
			return err
		}
	}
	return nil
}

// withState fills in the state of Worktree w and its running Processes.
func (d *Daemon) withState(w api.Worktree) api.Worktree {
	w.State = d.worktreeState(w.Domain)
	w.Processes = nil
	for _, st := range d.supervisor.Processes(w.Path) {
		w.Processes = append(w.Processes, api.Process{Name: st.Name, State: string(st.State)})
	}
	return w
}
