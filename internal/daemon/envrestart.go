package daemon

import (
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/supervisor"
)

// Processes read .env when they start, and some never look at it again
// (Laravel's php artisan serve --no-reload, queue:work), so when Laneway
// changes a running Worktree's .env it restarts the Worktree's running
// Processes.

// restartForEnv restarts the Worktree's running Processes, after Laneway
// changed its .env, with cfg, the config they start from now. The caller
// holds s.lifecycle.
func (d *Daemon) restartForEnv(s *served, cfg worktreeConfig) {
	var procs []recipe.Process
	var names []string
	for _, proc := range cfg.Processes {
		if p, ok := d.supervisor.Process(s.group, proc.Name); ok && p.State() != supervisor.StateFailed {
			procs = append(procs, proc)
			names = append(names, proc.Name)
		}
	}
	if len(procs) == 0 {
		return
	}
	d.log.Info("restarting Processes for the changed .env", "project", s.project, "dir", s.dir, "processes", names)
	d.stopProcesses(s, names)
	for _, spec := range d.specs(s, cfg, procs) {
		if _, err := d.startProcess(s, spec, true); err != nil {
			d.log.Error("restarting Process for the changed .env", "project", s.project, "dir", s.dir, "err", err)
		}
	}
}

// restartRunning restarts the running Processes of the served Worktree s,
// if it is still served and running, so they read its changed .env. The
// caller holds s.lifecycle; why is what the log says on failure.
func (d *Daemon) restartRunning(s *served, why string) {
	if s.ctx.Err() != nil || len(d.supervisor.Processes(s.group)) == 0 {
		return
	}
	cfg, err := d.resolveWorktree(s.ctx, s.dir)
	if err != nil {
		d.log.Error(why, "project", s.project, "dir", s.dir, "err", err)
		return
	}
	d.restartForEnv(s, cfg)
}

// servedWorktree is the Worktree served at its own Domain domain, or nil.
func (d *Daemon) servedWorktree(domain string) *served {
	d.servedMu.Lock()
	defer d.servedMu.Unlock()
	return d.served[domain]
}

// lockLifecycle takes the lifecycle lock of the Worktree served at its own
// Domain domain, if it is served, and returns what releases it.
func (d *Daemon) lockLifecycle(domain string) (unlock func()) {
	s := d.servedWorktree(domain)
	if s == nil {
		return func() {}
	}
	s.lifecycle.Lock()
	return s.lifecycle.Unlock
}

// primaryDomainMoved brings the Worktree served at domain (its own Domain)
// up to date with its new Primary Domain, in the background: once it is
// Ready, its Env Patch is applied again and, if it is running, its running
// Processes restart, since they read .env and their hosts when they start.
// A Failed setup gets the new Primary Domain when retried. Setup's env step
// takes the lifecycle lock too, so the two never write .env at once. A
// stopped Worktree isn't started: its next start uses the new Primary
// Domain.
func (d *Daemon) primaryDomainMoved(domain string) {
	s := d.servedWorktree(domain)
	if s == nil {
		return
	}
	d.background(func() {
		// Setup can take minutes, and its env step takes the lifecycle
		// lock: wait for it first.
		if err := d.waitForReady(s); err != nil {
			return
		}
		s.lifecycle.Lock()
		defer s.lifecycle.Unlock()
		if s.ctx.Err() != nil {
			return
		}
		d.applyServedEnvPatch(s)
		d.restartRunning(s, "restarting Processes for the new Primary Domain")
	})
}

// restartServedForEnv restarts the running Processes of the Worktree served
// at domain (its own Domain), if it is running, in the background, after
// Laneway changed its .env outside a start (`lane link`).
func (d *Daemon) restartServedForEnv(domain string) {
	s := d.servedWorktree(domain)
	if s == nil {
		return
	}
	d.background(func() {
		s.lifecycle.Lock()
		defer s.lifecycle.Unlock()
		d.restartRunning(s, "restarting Processes for the changed .env")
	})
}
