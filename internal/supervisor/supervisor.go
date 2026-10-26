// Package supervisor is the Process Supervisor: it starts and stops the
// Processes of each Worktree, picking a fresh free loopback port for every
// HTTP Process on every start and passing it through `{port}` in the command
// and the PORT env var.
//
// A Process that exits without being stopped has crashed: it is restarted
// with backoff, and marked failed if it keeps crashing (see MaxCrashes). The
// backoff is timed by the injected clock.
//
// Each Process's output (stdout and stderr together) is kept: the most recent
// lines in memory (see Output), and all of it in a rotating log file.
//
// Start Modes come in a later ticket.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/clock"
)

// Spec says how to run one Process.
type Spec struct {
	Name string
	// Command is run with `sh -c`, after every `{port}` is replaced.
	Command string
	// HTTP Processes get a port; others don't, and their `{port}` is left
	// as it is.
	HTTP bool
	// Dir is the working directory, normally the Worktree's.
	Dir string
	// LogFile is where all of the Process's output is written (appended,
	// and rotated as it grows). Empty means output is only kept in memory.
	LogFile string
}

// StopTimeout is how long a Process gets to exit after SIGTERM before its
// process group is killed.
const StopTimeout = 5 * time.Second

// Supervisor runs Processes in groups, one group per Worktree. It is safe for
// concurrent use.
type Supervisor struct {
	log   *slog.Logger
	clock clock.Clock
	opts  Options

	mu     sync.Mutex
	groups map[string]map[string]*Process // group → Process name → Process

	outMu      sync.Mutex
	seq        uint64                        // number of the last line written
	outputs    map[string]map[string]*output // group → Process name → its output
	outChanged chan struct{}                 // closed and replaced on every line
	readers    sync.WaitGroup                // goroutines copying Process output
}

// Options tune a Supervisor. The zero value is the defaults.
type Options struct {
	// Clock times crash backoff. Defaults to the real clock.
	Clock clock.Clock
	// LogMaxBytes is the size a log file is rotated at (DefaultLogMaxBytes
	// if 0), and LogBackups how many rotated files are kept
	// (DefaultLogBackups if 0).
	LogMaxBytes int64
	LogBackups  int
}

// New returns a Supervisor that logs to log.
func New(log *slog.Logger, opts Options) *Supervisor {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.LogMaxBytes == 0 {
		opts.LogMaxBytes = DefaultLogMaxBytes
	}
	if opts.LogBackups == 0 {
		opts.LogBackups = DefaultLogBackups
	}
	return &Supervisor{
		log:        log,
		clock:      opts.Clock,
		opts:       opts,
		groups:     map[string]map[string]*Process{},
		outputs:    map[string]map[string]*output{},
		outChanged: make(chan struct{}),
	}
}

// StartGroup starts every Process in specs as group, first stopping whatever
// group was already running under that name. If one fails to start, the
// ones already started are stopped again.
func (s *Supervisor) StartGroup(ctx context.Context, group string, specs []Spec) error {
	s.StopGroup(ctx, group)
	procs := map[string]*Process{}
	for _, spec := range specs {
		p, err := s.start(group, spec)
		if err != nil {
			for _, started := range procs {
				started.stop(ctx)
			}
			return err
		}
		procs[spec.Name] = p
	}
	s.mu.Lock()
	s.groups[group] = procs
	s.mu.Unlock()
	return nil
}

// Process returns Process name in group, if the group is running.
func (s *Supervisor) Process(group, name string) (*Process, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.groups[group][name]
	return p, ok
}

// Status is a Process's name and what it is doing.
type Status struct {
	Name  string
	State State
}

// Processes returns the status of every Process in group, sorted by name.
func (s *Supervisor) Processes(group string) []Status {
	s.mu.Lock()
	procs := s.groups[group]
	s.mu.Unlock()
	var list []Status
	for _, name := range slices.Sorted(maps.Keys(procs)) {
		list = append(list, procs[name].status())
	}
	return list
}

// StopGroup stops every Process in group and waits for them to exit.
func (s *Supervisor) StopGroup(ctx context.Context, group string) {
	s.mu.Lock()
	procs := s.groups[group]
	delete(s.groups, group)
	s.mu.Unlock()
	stopAll(ctx, procs)
}

// StopAll stops every Process in every group and closes their log files.
func (s *Supervisor) StopAll(ctx context.Context) {
	s.mu.Lock()
	groups := s.groups
	s.groups = map[string]map[string]*Process{}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, procs := range groups {
		wg.Go(func() { stopAll(ctx, procs) })
	}
	wg.Wait()
	// The last output of the stopped Processes may still be on its way.
	readersDone := make(chan struct{})
	go func() { s.readers.Wait(); close(readersDone) }()
	select {
	case <-readersDone:
	case <-ctx.Done():
	}
	s.closeLogFiles()
}

func stopAll(ctx context.Context, procs map[string]*Process) {
	var wg sync.WaitGroup
	for _, p := range procs {
		wg.Go(func() { p.stop(ctx) })
	}
	wg.Wait()
}

// Crash policy. A Process that exits without being stopped has crashed. It is
// restarted after a backoff that doubles with each crash within CrashWindow
// (1s, 2s, 4s, 8s), and once it has crashed MaxCrashes times within
// CrashWindow it is marked failed and left alone until it's started again.
const (
	MaxCrashes   = 5
	CrashWindow  = 2 * time.Minute
	firstBackoff = time.Second
	maxBackoff   = 30 * time.Second
)

// backoff is how long to wait before restarting after the nth crash within
// CrashWindow.
func backoff(n int) time.Duration {
	return min(firstBackoff<<(n-1), maxBackoff)
}

// State is what a Process is doing.
type State string

const (
	// StateRunning: its command is running. It may not accept connections
	// yet.
	StateRunning State = "running"
	// StateRestarting: it crashed and is waiting out its backoff.
	StateRestarting State = "restarting"
	// StateFailed: it crashed MaxCrashes times within CrashWindow and won't
	// be restarted.
	StateFailed State = "failed"
	// StateStopped: it was stopped.
	StateStopped State = "stopped"
)

// Process is one supervised Process. It keeps its identity across crash
// restarts; each start of its command is a run.
type Process struct {
	s     *Supervisor
	group string
	spec  Spec
	log   *slog.Logger

	mu      sync.Mutex
	state   State
	run     *run        // the current run; nil unless running
	port    int         // the latest run's port
	crashes []time.Time // when it crashed, within the last CrashWindow
	timer   clock.Timer // the pending restart, if any
	stopped bool        // stop was called; nothing restarts it any more
}

// run is one start of a Process's command.
type run struct {
	port int
	cmd  *exec.Cmd

	exited  chan struct{} // closed once it has exited and its output is read
	exitErr error         // why it exited; read only after exited is closed
}

func (s *Supervisor) start(group string, spec Spec) (*Process, error) {
	p := &Process{s: s, group: group, spec: spec, log: s.log.With("group", group, "process", spec.Name)}
	s.outMu.Lock()
	s.outputOf(group, spec.Name).setFile(spec.LogFile, s.opts)
	s.outMu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.startRun(); err != nil {
		return nil, err
	}
	return p, nil
}

// startRun starts the Process's command on a fresh port. The caller holds
// p.mu.
func (p *Process) startRun() error {
	spec := p.spec
	var port int
	command, env := spec.Command, os.Environ()
	if spec.HTTP {
		var err error
		port, err = freePort()
		if err != nil {
			return fmt.Errorf("picking a port for Process %q: %w", spec.Name, err)
		}
		command = strings.ReplaceAll(spec.Command, "{port}", strconv.Itoa(port))
		env = append(env, "PORT="+strconv.Itoa(port))
	}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = spec.Dir
	cmd.Env = env
	// Stdout and stderr share one pipe we create ourselves (rather than
	// exec's copying goroutines), so Wait doesn't depend on grandchildren
	// closing it.
	outR, outW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("starting Process %q: %w", spec.Name, err)
	}
	cmd.Stdout, cmd.Stderr = outW, outW
	// Its own process group, so stopping it also stops whatever the shell
	// started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = cmd.Start()
	outW.Close()
	if err != nil {
		outR.Close()
		return fmt.Errorf("starting Process %q: %w", spec.Name, err)
	}
	r := &run{port: port, cmd: cmd, exited: make(chan struct{})}
	outputRead := make(chan struct{})
	p.s.readers.Go(func() {
		defer close(outputRead)
		defer outR.Close()
		p.s.copyOutput(p.group, spec.Name, outR)
	})
	go p.wait(r, outputRead)
	p.run, p.port, p.state = r, port, StateRunning
	p.log.Info("process started", "port", port, "command", command)
	return nil
}

// wait waits for run r to exit, then restarts the Process or marks it failed
// unless it was stopped.
func (p *Process) wait(r *run, outputRead <-chan struct{}) {
	r.exitErr = r.cmd.Wait()
	// Its last output goes in before anything reports the exit. A
	// grandchild still holding the pipe can't hold this up for long.
	select {
	case <-outputRead:
	case <-time.After(time.Second):
	}
	close(r.exited)
	p.log.Info("process exited", "err", r.exitErr)

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.run == r {
		p.run = nil
	}
	if p.stopped {
		p.state = StateStopped
		return
	}
	status := "exit status 0"
	if r.exitErr != nil {
		status = r.exitErr.Error()
	}
	p.crashed("exited (" + status + ")")
}

// crashed records a crash, then schedules a restart or marks the Process
// failed. The caller holds p.mu.
func (p *Process) crashed(what string) {
	now := p.s.clock.Now()
	p.crashes = slices.DeleteFunc(p.crashes, func(t time.Time) bool { return !t.After(now.Add(-CrashWindow)) })
	p.crashes = append(p.crashes, now)
	if len(p.crashes) >= MaxCrashes {
		p.state = StateFailed
		p.note("%s and has crashed %d times within %g minutes, so it won't be restarted "+
			"until `portby up` or `portby restart`", what, len(p.crashes), CrashWindow.Minutes())
		return
	}
	delay := backoff(len(p.crashes))
	p.state = StateRestarting
	p.note("%s; restarting in %s", what, delay)
	p.timer = p.s.clock.AfterFunc(delay, p.restart)
}

// restart starts the Process again after its backoff.
func (p *Process) restart() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped || p.state != StateRestarting {
		return
	}
	p.timer = nil
	if err := p.startRun(); err != nil {
		p.crashed(fmt.Sprintf("could not be restarted (%v)", err))
	}
}

// note adds a line from Portby itself to the Process's output.
func (p *Process) note(format string, args ...any) {
	p.s.writeLine(p.group, p.spec.Name, fmt.Sprintf("portby: Process %q ", p.spec.Name)+fmt.Sprintf(format, args...))
}

func (p *Process) status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Status{Name: p.spec.Name, State: p.state}
}

// freePort asks the kernel for a free loopback port. Something else could
// take it before the Process binds it, but a fresh one is picked on every
// start.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// Addr is the loopback address the Process was last told to listen on.
func (p *Process) Addr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(p.port))
}

// WaitUp waits until the Process accepts TCP connections on its port. It
// fails if the Process exits first, isn't running, or ctx is done.
func (p *Process) WaitUp(ctx context.Context) error {
	var dialer net.Dialer
	for {
		p.mu.Lock()
		r, state := p.run, p.state
		p.mu.Unlock()
		switch {
		case state == StateFailed:
			return fmt.Errorf("Process %q has failed: it crashed %d times within %g minutes; see its output with `portby logs %s`",
				p.spec.Name, MaxCrashes, CrashWindow.Minutes(), p.spec.Name)
		case r == nil:
			return fmt.Errorf("Process %q is %s", p.spec.Name, state)
		}
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(r.port))
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-r.exited:
			return fmt.Errorf("Process %q exited before accepting connections on port %d: %v",
				p.spec.Name, r.port, r.exitErr)
		case <-ctx.Done():
			return fmt.Errorf("Process %q did not accept connections on port %d: %w",
				p.spec.Name, r.port, context.Cause(ctx))
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// stop stops the Process for good: it cancels a pending restart, and sends
// SIGTERM to a running command's process group, then SIGKILL if it hasn't
// exited after StopTimeout or when ctx is done.
func (p *Process) stop(ctx context.Context) {
	p.mu.Lock()
	p.stopped = true
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	r := p.run
	if r == nil {
		p.state = StateStopped
	}
	p.mu.Unlock()
	if r == nil {
		return
	}
	pgid := -r.cmd.Process.Pid
	if err := syscall.Kill(pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		p.log.Warn("signalling process", "err", err)
	}
	timer := time.NewTimer(StopTimeout)
	defer timer.Stop()
	select {
	case <-r.exited:
	case <-timer.C:
	case <-ctx.Done():
	}
	// Kill the whole group even if the shell already exited: its children
	// may not have.
	syscall.Kill(pgid, syscall.SIGKILL)
	<-r.exited
}
