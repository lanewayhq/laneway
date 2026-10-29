package daemon

import (
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
	"github.com/mrsafalpiya/portby.dev/internal/supervisor"
)

// Processes read .env when they start, and some never look at it again
// (Laravel's php artisan serve --no-reload, queue:work), so when Portby
// changes a running Worktree's .env it restarts the Worktree's running
// Processes.

// restartForEnv restarts the Worktree's running Processes, after Portby
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

// primaryDomainMoved brings the Worktree served at domain (its own Domain)
// up to date with its new Primary Domain, in the background: once it is
// Ready, its Env Patch is applied again and, if it is running, its running
// Processes restart, since they read .env and their hosts when they start.
// Waiting for Ready means Preparing's own Env Patch, which renders the
// Primary Domain it finds, never writes .env at the same time; a Failed
// setup gets it when retried. A stopped Worktree isn't started: its next
// start uses the new Primary Domain.
func (d *Daemon) primaryDomainMoved(domain string) {
	d.servedMu.Lock()
	s := d.served[domain]
	d.servedMu.Unlock()
	if s == nil {
		return
	}
	d.background(func() {
		if err := d.waitForReady(s); err != nil {
			return
		}
		s.lifecycle.Lock()
		defer s.lifecycle.Unlock()
		if s.ctx.Err() != nil {
			return
		}
		d.applyServedEnvPatch(s)
		if len(d.supervisor.Processes(s.group)) == 0 {
			return
		}
		cfg, err := d.resolveWorktree(s.ctx, s.dir)
		if err != nil {
			d.log.Error("restarting Processes for the new Primary Domain", "project", s.project, "dir", s.dir, "err", err)
			return
		}
		d.restartForEnv(s, cfg)
	})
}

// restartServedForEnv restarts the running Processes of the Worktree at dir,
// if it is served and running, in the background, after Portby changed its
// .env outside a start (`portby link`).
func (d *Daemon) restartServedForEnv(dir string) {
	d.servedMu.Lock()
	var s *served
	for _, candidate := range d.served {
		if candidate.dir == dir {
			s = candidate
		}
	}
	d.servedMu.Unlock()
	if s == nil {
		return
	}
	d.background(func() {
		s.lifecycle.Lock()
		defer s.lifecycle.Unlock()
		if s.ctx.Err() != nil || len(d.supervisor.Processes(s.group)) == 0 {
			return
		}
		cfg, err := d.resolveWorktree(s.ctx, s.dir)
		if err != nil {
			d.log.Error("restarting Processes for the changed .env", "project", s.project, "dir", s.dir, "err", err)
			return
		}
		d.restartForEnv(s, cfg)
	})
}
