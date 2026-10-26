// Package supervisor is the Process Supervisor: it starts and stops the
// Processes of each Worktree, picking a fresh free loopback port for every
// Process on every start and passing it through `{port}` in the command and
// the PORT env var.
//
// Each Process's last RecentLines lines of output (stdout and stderr) are
// kept in memory. Crash restarts, Start Modes and log files come in later
// tickets.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Spec says how to run one Process.
type Spec struct {
	Name string
	// Command is run with `sh -c`, after every `{port}` is replaced.
	Command string
	// Dir is the working directory, normally the Worktree's.
	Dir string
}

// StopTimeout is how long a Process gets to exit after SIGTERM before its
// process group is killed.
const StopTimeout = 5 * time.Second

// Supervisor runs Processes in groups, one group per Worktree. It is safe for
// concurrent use.
type Supervisor struct {
	log *slog.Logger

	mu     sync.Mutex
	groups map[string]map[string]*Process // group → Process name → Process
}

// New returns a Supervisor that logs to log.
func New(log *slog.Logger) *Supervisor {
	return &Supervisor{log: log, groups: map[string]map[string]*Process{}}
}

// StartGroup starts every Process in specs as group, first stopping whatever
// group was already running under that name. If one fails to start, the
// ones already started are stopped again.
func (s *Supervisor) StartGroup(ctx context.Context, group string, specs []Spec) error {
	s.StopGroup(ctx, group)
	procs := map[string]*Process{}
	for _, spec := range specs {
		p, err := start(spec, s.log.With("group", group, "process", spec.Name))
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

// Process returns the running Process name in group.
func (s *Supervisor) Process(group, name string) (*Process, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.groups[group][name]
	return p, ok
}

// StopGroup stops every Process in group and waits for them to exit.
func (s *Supervisor) StopGroup(ctx context.Context, group string) {
	s.mu.Lock()
	procs := s.groups[group]
	delete(s.groups, group)
	s.mu.Unlock()
	stopAll(ctx, procs)
}

// StopAll stops every Process in every group.
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
}

func stopAll(ctx context.Context, procs map[string]*Process) {
	var wg sync.WaitGroup
	for _, p := range procs {
		wg.Go(func() { p.stop(ctx) })
	}
	wg.Wait()
}

// Process is one started Process.
type Process struct {
	spec    Spec
	port    int
	command string // spec.Command with {port} replaced
	cmd     *exec.Cmd
	log     *slog.Logger
	output  *recent

	exited  chan struct{} // closed once the process has exited
	exitErr error         // why it exited; read only after exited is closed
}

func start(spec Spec, log *slog.Logger) (*Process, error) {
	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("picking a port for Process %q: %w", spec.Name, err)
	}
	command := strings.ReplaceAll(spec.Command, "{port}", strconv.Itoa(port))
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(), "PORT="+strconv.Itoa(port))
	output := &recent{}
	cmd.Stdout = output
	cmd.Stderr = output
	// Output reaches us through a pipe, which a grandchild can hold open
	// after the Process itself exits. Don't let that hold up Wait for long.
	cmd.WaitDelay = time.Second
	// Its own process group, so stopping it also stops whatever the shell
	// started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting Process %q: %w", spec.Name, err)
	}
	p := &Process{spec: spec, port: port, command: command, cmd: cmd, log: log, output: output, exited: make(chan struct{})}
	go func() {
		p.exitErr = cmd.Wait()
		close(p.exited)
		log.Info("process exited", "err", p.exitErr)
	}()
	log.Info("process started", "port", port, "command", command)
	return p, nil
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

// Addr is the loopback address the Process was told to listen on.
func (p *Process) Addr() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(p.port))
}

// Command is the command the Process was started with, as run.
func (p *Process) Command() string {
	return p.command
}

// Output returns the Process's last RecentLines lines of output.
func (p *Process) Output() []string {
	return p.output.snapshot()
}

// WaitUp waits until the Process accepts TCP connections on its port. It
// fails if the Process exits first or ctx is done.
func (p *Process) WaitUp(ctx context.Context) error {
	var dialer net.Dialer
	for {
		conn, err := dialer.DialContext(ctx, "tcp", p.Addr())
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-p.exited:
			return fmt.Errorf("Process %q exited before accepting connections on port %d: %v",
				p.spec.Name, p.port, p.exitErr)
		case <-ctx.Done():
			return fmt.Errorf("Process %q did not accept connections on port %d: %w",
				p.spec.Name, p.port, context.Cause(ctx))
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// stop sends SIGTERM to the Process's group, then SIGKILL if it hasn't
// exited after StopTimeout or when ctx is done.
func (p *Process) stop(ctx context.Context) {
	pgid := -p.cmd.Process.Pid
	if err := syscall.Kill(pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		p.log.Warn("signalling process", "err", err)
	}
	timer := time.NewTimer(StopTimeout)
	defer timer.Stop()
	select {
	case <-p.exited:
	case <-timer.C:
	case <-ctx.Done():
	}
	// Kill the whole group even if the shell already exited: its children
	// may not have.
	syscall.Kill(pgid, syscall.SIGKILL)
	<-p.exited
}
