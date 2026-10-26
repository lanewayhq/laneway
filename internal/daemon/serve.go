package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/config"
	"github.com/mrsafalpiya/portby.dev/internal/naming"
	"github.com/mrsafalpiya/portby.dev/internal/proxy"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
	"github.com/mrsafalpiya/portby.dev/internal/supervisor"
)

// StartTimeout is how long the Primary Process gets to accept connections
// when its Worktree starts, unless the Project configures start_timeout.
const StartTimeout = 60 * time.Second

// served is one Domain and the Worktree behind it. Its Processes are lazy:
// nothing runs until the first request for the Domain starts them.
type served struct {
	project string
	domain  string
	dir     string // the Worktree's directory
	group   string // the Supervisor group running the Worktree's Processes

	// ctx is cancelled when the Domain stops being served, which ends a
	// start in progress.
	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	start *start // the running or last start; nil until the first request
}

// start is one attempt to start a Worktree's Processes.
type start struct {
	done    chan struct{} // closed once the Primary Process is up or has failed
	primary string        // the Primary Process's name, once up
	// routed are the other HTTP Processes, served at <process>.<domain>,
	// once up.
	routed map[string]bool
	err    error // why it failed
	// retry is set (under served.mu) once a browser has been shown the
	// failure. Until then every request gets the failure, so the starting
	// page's refreshes end on the error page instead of starting again; the
	// next request after that (a reload) starts the Worktree again.
	retry bool
}

// domainOf is the main Worktree's Domain.
func domainOf(p registry.Project) string {
	return naming.DomainLabel(p.Name) + ".localhost"
}

// serveProject applies the main Worktree's Env Patch and serves it at its
// Domain.
func (d *Daemon) serveProject(p registry.Project) {
	d.applyEnvPatch(p, mainEnvWorktree(p))
	d.serveWorktree(p, domainOf(p), p.Path)
}

// serveWorktree makes the Worktree at dir known to the proxy at domain,
// without starting anything: the first request for the Domain does that.
func (d *Daemon) serveWorktree(p registry.Project, domain, dir string) {
	s := &served{project: p.Name, domain: domain, dir: dir, group: dir}
	s.ctx, s.cancel = context.WithCancel(d.stopping)
	d.servedMu.Lock()
	d.served[domain] = s
	d.servedMu.Unlock()
}

// stopWorktree stops serving domain and stops the Processes of the Worktree
// at dir, including a start in progress.
func (d *Daemon) stopWorktree(ctx context.Context, domain, dir string) {
	d.servedMu.Lock()
	s := d.served[domain]
	delete(d.served, domain)
	d.servedMu.Unlock()
	if s != nil {
		s.cancel()
		s.mu.Lock()
		st := s.start
		s.mu.Unlock()
		if st != nil {
			<-st.done
		}
	}
	d.supervisor.StopGroup(ctx, dir)
}

// starting returns the Worktree's current start, beginning one if there is
// none, so simultaneous first requests share a single start.
func (d *Daemon) starting(s *served) *start {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.start == nil || s.start.retry {
		st := &start{done: make(chan struct{})}
		s.start = st
		d.startsMu.Lock()
		defer d.startsMu.Unlock()
		if d.shuttingDown {
			st.err = errors.New("the Portby daemon is shutting down")
			close(st.done)
			return st
		}
		d.starts.Go(func() {
			defer close(st.done)
			st.primary, st.routed, st.err = d.bootWorktree(s)
			if st.err != nil {
				d.log.Error("starting Worktree", "project", s.project, "dir", s.dir, "err", st.err)
			}
		})
	}
	return s.start
}

// bootWorktree resolves the Worktree's config, starts the Worktree's Processes
// and waits for the Primary Process to accept connections. It returns the
// Primary Process's name and the other HTTP Processes.
func (d *Daemon) bootWorktree(s *served) (string, map[string]bool, error) {
	d.applyServedEnvPatch(s)
	cfg, err := recipe.ResolveDir(s.dir)
	if err != nil {
		return "", nil, fmt.Errorf("Project %q could not be started: %w", s.project, err)
	}
	for _, w := range cfg.Warnings {
		d.log.Warn("config", "project", s.project, "warning", w)
	}
	var primary string
	routed := map[string]bool{}
	var specs []supervisor.Spec
	for _, proc := range cfg.Processes {
		if proc.Primary {
			primary = proc.Name
		} else if proc.HTTP {
			routed[proc.Name] = true
		}
		specs = append(specs, supervisor.Spec{
			Name:    proc.Name,
			Command: proc.Command,
			HTTP:    proc.HTTP,
			Dir:     s.dir,
			LogFile: filepath.Join(d.cfg.Paths.Logs(), s.domain, proc.Name+".log"),
		})
	}
	if primary == "" {
		return "", nil, fmt.Errorf("Project %q has no Primary Process: declare one in %s, "+
			"for example [processes.web] with command = \"…\" and primary = true", s.project, config.FileName)
	}
	timeout := cmp.Or(cfg.StartTimeout, StartTimeout)
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()
	if err := d.supervisor.StartGroup(ctx, s.group, specs); err != nil {
		return "", nil, fmt.Errorf("Project %q could not be started: %w", s.project, err)
	}
	proc, _ := d.supervisor.Process(s.group, primary)
	err = proc.WaitUp(ctx)
	if err == nil {
		return primary, routed, nil
	}
	// The start failed: stop what did start, so a later start begins afresh.
	d.supervisor.StopGroup(context.Background(), s.group)
	se := &proxy.StartError{Process: primary, Command: proc.Command(), Output: d.lastOutput(s.group, primary), Err: err}
	if errors.Is(err, context.DeadlineExceeded) {
		se.TimedOut = true
		se.Err = fmt.Errorf("Process %q did not accept connections within %s; "+
			"if it needs longer, raise start_timeout in %s", primary, timeout, config.FileName)
	}
	return "", nil, se
}

// errorPageLines is how much of a Process's output the error page shows.
const errorPageLines = 50

// lastOutput is the last errorPageLines lines of the Process's output.
func (d *Daemon) lastOutput(group, process string) []string {
	lines, _ := d.supervisor.Output(group, process, 0)
	lines = lines[max(0, len(lines)-errorPageLines):]
	text := make([]string, len(lines))
	for i, l := range lines {
		text[i] = l.Text
	}
	return text
}

// router is the daemon's proxy.Router.
type router struct{ d *Daemon }

func (r router) Upstream(ctx context.Context, domain string, wait bool) (string, error) {
	d := r.d
	s, process, ok := d.route(domain)
	if !ok {
		return "", fmt.Errorf("%s: %w", domain, proxy.ErrUnknownDomain)
	}
	st := d.starting(s)
	select {
	case <-st.done:
	default:
		if !wait {
			return "", proxy.ErrStarting
		}
		select {
		case <-st.done:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if st.err != nil {
		if !wait {
			s.mu.Lock()
			st.retry = true
			s.mu.Unlock()
		}
		return "", st.err
	}
	switch {
	case process == "":
		process = st.primary
	case !st.routed[process]:
		// The config the Worktree started with has no such HTTP Process.
		return "", fmt.Errorf("%s: %w", domain, proxy.ErrUnknownDomain)
	}
	// Once started, the Supervisor keeps the Process going: a crash
	// restarts it on a new port, or marks it failed.
	proc, ok := d.supervisor.Process(s.group, process)
	if !ok {
		return "", fmt.Errorf("Process %q of Project %q is not running", process, s.project)
	}
	ctx, cancel := context.WithTimeout(ctx, StartTimeout)
	defer cancel()
	if err := proc.WaitUp(ctx); err != nil {
		return "", err
	}
	return proc.Addr(), nil
}

// route finds the Worktree serving host. At a Worktree's Domain or one of its
// Aliases that is the Primary Process (process is ""); at <process>.<domain>
// it is one of the Worktree's other HTTP Processes. Either way, a request
// there starts the Worktree.
//
// A Domain or Alias wins over <process>.<domain>: Worktree labels and
// Aliases are kept clear of the main Worktree's HTTP Process names when they
// are given out, but config can add a Process later.
func (d *Daemon) route(host string) (s *served, process string, ok bool) {
	if s, ok := d.servedAt(host); ok {
		return s, "", true
	}
	process, domain, found := strings.Cut(host, ".")
	if s, ok := d.servedAt(domain); found && ok && d.routesProcess(s, process) {
		return s, process, true
	}
	return nil, "", false
}

// servedAt finds the Worktree whose Domain, or one of whose Aliases, is
// domain.
func (d *Daemon) servedAt(domain string) (*served, bool) {
	if target, ok := d.aliasTarget(domain); ok {
		domain = target
	}
	d.servedMu.Lock()
	defer d.servedMu.Unlock()
	s, ok := d.served[domain]
	return s, ok
}

// routesProcess reports whether process is one of the Worktree's HTTP Processes
// other than the Primary Process: as started, once the Worktree is up, and
// otherwise from its config, so a mistyped host doesn't start a Worktree.
func (d *Daemon) routesProcess(s *served, process string) bool {
	s.mu.Lock()
	st := s.start
	s.mu.Unlock()
	if st != nil {
		select {
		case <-st.done:
			if st.err == nil {
				return st.routed[process]
			}
		default:
		}
	}
	cfg, err := recipe.ResolveDir(s.dir)
	if err != nil {
		return false
	}
	for _, proc := range cfg.Processes {
		if proc.Name == process {
			return proc.HTTP && !proc.Primary
		}
	}
	return false
}
