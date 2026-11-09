package daemon

import (
	"github.com/lanewayhq/laneway/internal/plugin"
	"github.com/lanewayhq/laneway/internal/registry"
	"github.com/lanewayhq/laneway/internal/service"
	"github.com/lanewayhq/laneway/internal/supervisor"
)

// The lifecycle events other than Preparing, Ready and Worktree removed
// (hooks.go): Link, Unlink, Primary Domain changed, Process started, stopped
// and crashed, Idle, woken, and Service started and stopped. Their hooks
// all run in the background (fireHooks), so none of them can block or fail
// what fired them.

// projectLinked fires the Link hooks of the Project p, which was just
// Linked.
func (d *Daemon) projectLinked(p registry.Project) {
	d.fireHooks(d.projectHooks(plugin.EventLink, p))
}

// projectUnlinked fires the Unlink hooks of the Project p, which was just
// Unlinked. It has no setup left to show their warnings, and no .env
// Laneway manages.
func (d *Daemon) projectUnlinked(p registry.Project) {
	o := d.loggedHooks(plugin.EventUnlink, p, mainWorktreeRef(p), "the Project was Unlinked")
	o.wholeProject = true
	d.fireHooks(o)
}

// primaryDomainChanged fires the Primary Domain changed hooks of the
// Project p's Worktree w, whose Primary Domain was previous.
func (d *Daemon) primaryDomainChanged(p registry.Project, w worktreeRef, previous string) {
	o := d.worktreeHooks(plugin.EventPrimaryDomainChanged, p, w)
	o.previousDomain = previous
	d.fireHooks(o)
}

// processEvents are the hooked events for the Supervisor's.
var processEvents = map[supervisor.EventKind]string{
	supervisor.Started: plugin.EventProcessStarted,
	supervisor.Stopped: plugin.EventProcessStopped,
	supervisor.Crashed: plugin.EventProcessCrashed,
}

// processEvent fires the hooks of what the Supervisor says happened to a
// Process (supervisor.Options.OnEvent), if its Worktree is served: one
// being removed or Unlinked, or the daemon shutting down, stops its
// Processes without firing anything.
func (d *Daemon) processEvent(ev supervisor.Event) {
	s := d.servedGroup(ev.Group)
	if s == nil {
		return
	}
	p, ok := d.servedProject(s)
	if !ok {
		return
	}
	o := d.worktreeHooks(processEvents[ev.Kind], p, s.worktreeRef)
	o.process = &plugin.EventProcess{Name: ev.Process, Command: ev.Command, Port: ev.Port, Exit: ev.Exit, Failed: ev.Failed}
	d.fireHooks(o)
}

// servedGroup is the served Worktree whose Processes run in the Supervisor
// group, or nil.
func (d *Daemon) servedGroup(group string) *served {
	d.servedMu.Lock()
	defer d.servedMu.Unlock()
	for _, s := range d.served {
		if s.group == group {
			return s
		}
	}
	return nil
}

// servedHooks fires the hooks of event for the served Worktree s, unless
// its Project was Unlinked.
func (d *Daemon) servedHooks(event string, s *served) {
	if p, ok := d.servedProject(s); ok {
		d.fireHooks(d.worktreeHooks(event, p, s.worktreeRef))
	}
}

// serviceEvent fires the hooks of event (Service started or stopped) of the
// Service st, in every Linked Project whose config uses it.
func (d *Daemon) serviceEvent(event string, st service.Status) {
	svc := &plugin.EventService{Name: st.Name(), Engine: st.Engine.Name, Version: st.Version, Host: st.Host, Port: st.Port}
	for _, p := range d.registry.Projects() {
		o := d.projectHooks(event, p)
		o.service = svc
		d.fireHooks(o)
	}
}
