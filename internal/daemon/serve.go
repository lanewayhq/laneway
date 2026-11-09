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

	"github.com/lanewayhq/laneway/internal/activity"
	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/naming"
	"github.com/lanewayhq/laneway/internal/prepare"
	"github.com/lanewayhq/laneway/internal/proxy"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/registry"
	"github.com/lanewayhq/laneway/internal/supervisor"
)

// StartTimeout is how long the Primary Process gets to accept connections
// when its Worktree starts, unless the Project configures start_timeout.
const StartTimeout = 60 * time.Second

// DefaultIdleTimeout is how long a Worktree may go without requests or open
// connections before it is Idle, unless idle_timeout is configured.
const DefaultIdleTimeout = 15 * time.Minute

// served is one Domain and the Worktree behind it. Its Processes are lazy:
// nothing runs until the first request for the Domain starts them.
type served struct {
	worktreeRef
	project string
	group   string // the Supervisor group running the Worktree's Processes

	// ctx is cancelled when the Domain stops being served, which ends a
	// start in progress.
	ctx    context.Context
	cancel context.CancelFunc

	// activity sees every request for the Domain and decides when the
	// Worktree is Idle.
	activity *activity.Tracker
	// lifecycle serialises changes to which Processes run (starts and
	// going Idle) and Laneway's writes to the Worktree's .env, so setup's
	// env step never writes it while a start or a moved Primary Domain
	// does.
	lifecycle sync.Mutex
	// alwaysServices ends the hold its always Processes have on the
	// Worktree's Services (holdAlwaysServices); nil when there is none.
	// Guarded by lifecycle.
	alwaysServices func()

	mu    sync.Mutex
	start *startAttempt // the running or last start; nil until the first request
	idle  bool          // it went Idle, and hasn't been started or stopped since
}

// state is what the API reports for the Worktree.
func (s *served) state() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.start != nil:
		select {
		case <-s.start.done:
			if s.start.err != nil {
				return api.WorktreeStopped
			}
			return api.WorktreeRunning
		default:
			return api.WorktreeStarting
		}
	case s.idle:
		return api.WorktreeIdle
	default:
		return api.WorktreeStopped
	}
}

// startAttempt is one attempt to start a Worktree's Processes.
type startAttempt struct {
	done    chan struct{} // closed once the Primary Process is up or has failed
	primary string        // the Primary Process's name, once up
	// routed are the other HTTP Processes, served at <process>.<domain>,
	// once up.
	routed map[string]bool
	err    error // why it failed
	// retry is set (under served.mu) once any request has been given the
	// failure; the next request of any kind then starts the Worktree
	// again, and requests that waited on one failed attempt arm a single
	// retry. No page reloads itself while a Ready Worktree starts, so this
	// can't restart a broken Worktree forever. A client that keeps
	// requesting one starts a new attempt on every other request.
	retry bool
	// lazy names the lazy Processes in the config it started from: the
	// ones Idle stops.
	lazy []string
	// manual are the manual Processes, which requests don't start.
	manual map[string]bool
	// static are the static directories of the HTTP Processes that have
	// one (absolute), by Process name.
	static map[string]string
	// release ends the running Worktree's use of its Services (see
	// services.go), once the start succeeded; nil before.
	release func()
}

// domainOf is the main Worktree's Domain.
func domainOf(p registry.Project) string {
	return naming.DomainLabel(p.Name) + ".localhost"
}

// serveProject applies the main Worktree's Env Patch, serves it at its
// Domain and begins preparing it if it isn't Ready.
func (d *Daemon) serveProject(p registry.Project) {
	w := mainWorktreeRef(p)
	d.applyEnvPatch(p, w)
	d.serveWorktree(p, w)
	d.reclaimOrphans(p, w)
	d.prepare(p, w, false)
}

// serveWorktree makes the Worktree w known to the proxy at its Domain and
// starts its always Processes once it is Ready. Its lazy Processes start on
// the first request for the Domain.
func (d *Daemon) serveWorktree(p registry.Project, w worktreeRef) {
	s := &served{worktreeRef: w, project: p.Name, group: w.dir}
	s.ctx, s.cancel = context.WithCancel(d.stopping)
	s.activity = activity.New(d.cfg.Clock, DefaultIdleTimeout, func() { d.goIdle(s) })
	d.servedMu.Lock()
	d.served[w.domain] = s
	d.servedMu.Unlock()
	d.serveServicesOf(w.dir)
	d.startAlways(s)
}

// startAlways starts the Worktree's always Processes that aren't running,
// in the background once the Worktree is Ready. If its setup fails they
// don't start; worktreeReady starts them after a successful --retry.
func (d *Daemon) startAlways(s *served) {
	d.background(func() {
		if err := d.waitForReady(s); err != nil {
			if s.ctx.Err() == nil {
				d.log.Error("not starting always Processes", "project", s.project, "dir", s.dir, "err", err)
			}
			return
		}
		s.lifecycle.Lock()
		defer s.lifecycle.Unlock()
		if s.ctx.Err() != nil {
			return // stopWorktree
		}
		cfg, err := d.resolveWorktree(s.ctx, s.dir)
		if err != nil {
			d.log.Error("starting always Processes", "project", s.project, "dir", s.dir, "err", err)
			return
		}
		var always []recipe.Process
		for _, proc := range cfg.Processes {
			if proc.StartMode == recipe.Always {
				always = append(always, proc)
			}
		}
		d.holdAlwaysServices(s, cfg.Resolved)
		for _, spec := range d.specs(s, cfg, always) {
			if _, err := d.startProcess(s, spec, false); err != nil {
				d.log.Error("starting always Process", "project", s.project, "dir", s.dir, "err", err)
			}
		}
	})
}

// background runs fn in d.starts, like a start, unless the daemon is
// shutting down, so it can't start Processes after StopAll.
func (d *Daemon) background(fn func()) {
	d.startsMu.Lock()
	defer d.startsMu.Unlock()
	if !d.shuttingDown {
		d.starts.Go(fn)
	}
}

// specs are the Supervisor specs for procs, some of cfg's Processes, with
// the Worktree's environment. The Primary Process's spec, if it is one of
// them, carries cfg's Runtime warnings.
func (d *Daemon) specs(s *served, cfg worktreeConfig, procs []recipe.Process) []supervisor.Spec {
	var primary string
	var specs []supervisor.Spec
	for _, proc := range procs {
		if proc.Primary {
			primary = proc.Name
		}
		command, err := d.withViteConfig(s, proc)
		if err != nil {
			d.log.Error("writing the Vite config", "project", s.project, "process", proc.Name, "err", err)
			command = "echo " + shellQuote("laneway: could not write the Vite config: "+err.Error()) + " >&2; exit 1"
		}
		if command, err = d.withPHPIniDir(command); err != nil {
			d.log.Error("writing the PHP ini", "project", s.project, "process", proc.Name, "err", err)
			command = "echo " + shellQuote("laneway: could not write the PHP ini: "+err.Error()) + " >&2; exit 1"
		}
		specs = append(specs, supervisor.Spec{
			Name:    proc.Name,
			Command: command,
			HTTP:    proc.HTTP,
			Dir:     s.dir,
			LogFile: filepath.Join(d.cfg.Paths.Logs(), s.domain, proc.Name+".log"),
		})
	}
	if primary == "" {
		// Without the Primary Process, apply would give the warnings to
		// the first spec.
		cfg.notes = nil
	}
	cfg.apply(specs, primary)
	return specs
}

// startProcess starts spec's Process unless it is already running (or, with
// restartFailed false, has failed), and reports whether it started it. The
// caller holds s.lifecycle.
func (d *Daemon) startProcess(s *served, spec supervisor.Spec, restartFailed bool) (bool, error) {
	if p, ok := d.supervisor.Process(s.group, spec.Name); ok {
		if !restartFailed || p.State() != supervisor.StateFailed {
			return false, nil
		}
	}
	if err := d.supervisor.Start(s.ctx, s.group, spec); err != nil {
		return false, fmt.Errorf("Project %q could not be started: %w", s.project, err)
	}
	return true, nil
}

// stopWorktree stops serving domain and stops the Processes of the Worktree
// at dir, including a start in progress.
func (d *Daemon) stopWorktree(ctx context.Context, domain, dir string) {
	d.servedMu.Lock()
	s := d.served[domain]
	delete(d.served, domain)
	d.servedMu.Unlock()
	if s != nil {
		s.activity.Reset()
		s.cancel()
		s.mu.Lock()
		st := s.start
		s.mu.Unlock()
		if st != nil {
			<-st.done
			st.releaseServices()
		}
		// A startAlways that got the lock first finishes before StopGroup;
		// one that gets it later sees s.ctx cancelled.
		s.lifecycle.Lock()
		releaseAlwaysServices(s)
		s.lifecycle.Unlock()
	}
	d.supervisor.StopGroup(ctx, dir)
}

// starting returns the Worktree's current start, beginning one if there is
// none, so simultaneous first requests share a single start.
func (d *Daemon) starting(s *served) *startAttempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.start == nil || s.start.retry || d.setupRetried(s) {
		st := &startAttempt{done: make(chan struct{})}
		s.start = st
		s.idle = false
		d.startsMu.Lock()
		defer d.startsMu.Unlock()
		if d.shuttingDown {
			st.err = errors.New("the Laneway daemon is shutting down")
			close(st.done)
			return st
		}
		d.starts.Go(func() {
			defer close(st.done)
			st.primary, st.err = d.bootWorktree(s, st)
			if st.err != nil {
				d.log.Error("starting Worktree", "project", s.project, "dir", s.dir, "err", st.err)
			}
		})
	}
	return s.start
}

// bootWorktree waits for the Worktree to be Ready, applies its Env Patch,
// resolves its config, starts its lazy and always Processes that aren't
// running and waits for the Primary Process to accept connections. It
// returns the Primary Process's name, and records the other HTTP Processes
// and the lazy and manual ones in st.
func (d *Daemon) bootWorktree(s *served, st *startAttempt) (string, error) {
	// Setup can take minutes: wait for it before taking the lifecycle
	// lock, so `lane down` doesn't wait for it too.
	if err := d.waitForReady(s); err != nil {
		return "", err
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	current := s.start == st
	s.mu.Unlock()
	if !current {
		return "", fmt.Errorf("Project %q was stopped while it was starting", s.project)
	}
	envChanged := d.applyServedEnvPatch(s)
	cfg, err := d.resolveWorktree(s.ctx, s.dir)
	if err != nil {
		return "", fmt.Errorf("Project %q could not be started: %w", s.project, err)
	}
	if envChanged {
		d.restartForEnv(s, cfg)
	}
	for _, w := range cfg.Warnings {
		d.log.Warn("config", "project", s.project, "warning", w)
	}
	d.serveServices(cfg.Services)
	d.holdAlwaysServices(s, cfg.Resolved)
	global, err := config.LoadGlobal(d.cfg.Paths.GlobalConfig())
	if err != nil {
		return "", fmt.Errorf("Project %q could not be started: %w", s.project, err)
	}
	for _, w := range global.Warnings {
		d.log.Warn("config", "warning", w)
	}
	// Starting counts as activity, so a Worktree started with no request
	// in flight still goes Idle.
	s.activity.SetTimeout(cmp.Or(cfg.IdleTimeout, ptrValue(global.IdleTimeout), DefaultIdleTimeout))
	s.activity.Touch()
	var primary string
	var started []recipe.Process
	st.routed, st.manual, st.static = map[string]bool{}, map[string]bool{}, map[string]string{}
	for _, proc := range cfg.Processes {
		if proc.StaticDir != "" {
			st.static[proc.Name] = filepath.Join(s.dir, proc.StaticDir)
		}
		if proc.Primary {
			primary = proc.Name
		} else if proc.HTTP {
			st.routed[proc.Name] = true
		}
		switch proc.StartMode {
		case recipe.Manual:
			st.manual[proc.Name] = true
			continue
		case recipe.Lazy:
			st.lazy = append(st.lazy, proc.Name)
		}
		started = append(started, proc)
	}
	if primary == "" {
		return "", fmt.Errorf("Project %q has no Primary Process: declare one in %s, "+
			"for example [processes.web] with command = \"…\" and primary = true", s.project, config.FileName)
	}
	timeout := cmp.Or(cfg.StartTimeout, StartTimeout)
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()
	for _, spec := range d.specs(s, cfg, started) {
		if _, err := d.startProcess(s, spec, true); err != nil {
			d.stopProcesses(s, st.lazy)
			return "", err
		}
	}
	proc, _ := d.supervisor.Process(s.group, primary)
	err = proc.WaitUp(ctx)
	if err == nil {
		d.waitForRouted(ctx, s, st, started)
		st.release = d.useServices(cfg.Services)
		return primary, nil
	}
	// The start failed: stop what it started, so a later start begins
	// afresh.
	d.stopProcesses(s, st.lazy)
	se := &proxy.StartError{Process: primary, Command: proc.Command(), Output: d.lastOutput(s.group, primary), Err: err}
	if errors.Is(err, context.DeadlineExceeded) {
		se.TimedOut = true
		se.Err = fmt.Errorf("Process %q did not accept connections within %s; "+
			"if it needs longer, raise start_timeout in %s", primary, timeout, config.FileName)
	}
	return "", se
}

// waitForRouted waits, within ctx, for the other HTTP Processes in started
// to accept connections, so the start's first page sees them up: a Laravel
// page names Vite's address only once Vite has written public/hot, which it
// does when it starts listening (spike #2). Only the Primary Process decides
// whether the start succeeds; one that doesn't come up is just logged.
func (d *Daemon) waitForRouted(ctx context.Context, s *served, st *startAttempt, started []recipe.Process) {
	var wg sync.WaitGroup
	for _, proc := range started {
		p, ok := d.supervisor.Process(s.group, proc.Name)
		if !st.routed[proc.Name] || !ok {
			continue
		}
		wg.Go(func() {
			if err := p.WaitUp(ctx); err != nil {
				d.log.Warn("Process didn't come up with its Worktree", "project", s.project, "process", proc.Name, "err", err)
			}
		})
	}
	wg.Wait()
}

// stopProcesses stops the Worktree's Processes named in names.
func (d *Daemon) stopProcesses(s *served, names []string) {
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Go(func() { d.supervisor.Stop(context.Background(), s.group, name) })
	}
	wg.Wait()
}

// goIdle is called by the Worktree's Activity Tracker once it has had no
// traffic for its idle timeout: its lazy Processes stop, and the next
// request starts them again.
func (d *Daemon) goIdle(s *served) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	st := s.start
	started := st != nil && isDone(st) && st.err == nil
	if started {
		s.start, s.idle = nil, true
	}
	s.mu.Unlock()
	if !started {
		return
	}
	st.releaseServices()
	d.log.Info("Worktree is Idle", "project", s.project, "domain", s.domain)
	d.stopProcesses(s, st.lazy)
}

func isDone(st *startAttempt) bool {
	select {
	case <-st.done:
		return true
	default:
		return false
	}
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

// Begin reports a request for domain (a Domain, an Alias or
// <process>.<domain>) to its Worktree's Activity Tracker.
func (r router) Begin(domain string) func() {
	if domain == r.d.mailDomain() {
		return r.d.useMail()
	}
	s, _, ok := r.d.route(domain)
	if !ok {
		return func() {}
	}
	return s.activity.Begin()
}

func (r router) Upstream(ctx context.Context, domain string, waitForPreparation bool) (string, error) {
	d := r.d
	if domain == d.cfg.DashboardDomain {
		return d.dashboardUpstream(), nil
	}
	if domain == d.mailDomain() {
		return d.mailUpstream(ctx)
	}
	s, process, ok := d.route(domain)
	if !ok {
		return "", fmt.Errorf("%s: %w", domain, proxy.ErrUnknownDomain)
	}
	st := d.starting(s)
	select {
	case <-st.done:
	default:
		if !waitForPreparation && d.preparer.Status(s.dir).State != prepare.Ready {
			return "", proxy.ErrPreparing
		}
		select {
		case <-st.done:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if st.err != nil {
		s.mu.Lock()
		st.retry = true
		s.mu.Unlock()
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
		s.mu.Lock()
		manual := st.manual[process]
		s.mu.Unlock()
		if manual {
			return "", fmt.Errorf("Process %q of Project %q isn't running; it is manual, so start it with `lane up %s`",
				process, s.project, process)
		}
		// `lane down <process>` stopped it: start it again, as after
		// Idle.
		s.mu.Lock()
		if s.start == st {
			s.start = nil
		}
		s.mu.Unlock()
		return r.Upstream(ctx, domain, waitForPreparation)
	}
	ctx, cancel := context.WithTimeout(ctx, StartTimeout)
	defer cancel()
	if err := proc.WaitUp(ctx); err != nil {
		if ctx.Err() != nil {
			return "", err
		}
		// It crashed: restarting, or failed for good.
		return "", &proxy.StartError{Process: process, Command: proc.Command(),
			Output: d.lastOutput(s.group, process), Crashed: true, Err: err}
	}
	return proc.Addr(), nil
}

// StaticDir is the static directory of the Process requests for domain go
// to, from the config the Worktree started with ("" if it has none or isn't
// running).
func (r router) StaticDir(domain string) string {
	s, process, ok := r.d.route(domain)
	if !ok {
		return ""
	}
	s.mu.Lock()
	st := s.start
	s.mu.Unlock()
	if st == nil || !isDone(st) || st.err != nil {
		return ""
	}
	if process == "" {
		process = st.primary
	}
	return st.static[process]
}

// RedirectTarget is the Primary Domain of the Worktree whose Domain or
// Alias is domain, when that isn't domain itself: page loads there are
// redirected to it. <process>.<domain> hosts are no Worktree's Domain or
// Alias, so they are never redirected.
func (r router) RedirectTarget(domain string) (string, bool) {
	s, ok := r.d.servedAt(domain)
	if !ok {
		return "", false
	}
	primary, ok := r.d.servedPrimaryDomain(s)
	return primary, ok && primary != domain
}

var _ proxy.StaticRouter = router{}

// worktreeState is the state of the Worktree served at domain.
func (d *Daemon) worktreeState(domain string) string {
	d.servedMu.Lock()
	s, ok := d.served[domain]
	d.servedMu.Unlock()
	if !ok {
		return ""
	}
	return s.state()
}

// ptrValue is *p, or the zero value if p is nil.
func ptrValue[T any](p *T) T {
	var v T
	if p != nil {
		v = *p
	}
	return v
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
	cfg, err := d.plugins.ResolveDir(s.dir)
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
