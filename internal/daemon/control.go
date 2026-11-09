package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/proxy"
	"github.com/lanewayhq/laneway/internal/recipe"
)

// control carries out `lane up`, `down` or `restart` (action) on the
// Worktree req names, or on one of its Processes, and returns the Worktree
// afterwards.
func (d *Daemon) control(ctx context.Context, action string, req api.ControlRequest) (api.Worktree, error) {
	if action != "up" && action != "down" && action != "restart" {
		return api.Worktree{}, &api.Error{Code: api.CodeBadRequest, Message: fmt.Sprintf("unknown action %q", action)}
	}
	dir, err := d.worktreeDir(ctx, req.Dir, req.Worktree)
	if err != nil {
		return api.Worktree{}, err
	}
	p, checkout, err := d.worktreeAt(ctx, dir)
	if err != nil {
		return api.Worktree{}, err
	}
	i := slices.IndexFunc(d.apiProject(p).Worktrees, func(w api.Worktree) bool { return w.Path == checkout.Dir })
	if i < 0 {
		return api.Worktree{}, &api.Error{Code: api.CodeUnknownWorktree, Message: fmt.Sprintf("the Worktree at %s isn't served yet", checkout.Dir)}
	}
	w := d.apiProject(p).Worktrees[i]
	d.servedMu.Lock()
	s, ok := d.served[w.Domain]
	d.servedMu.Unlock()
	if !ok {
		return api.Worktree{}, &api.Error{Code: api.CodeUnknownWorktree, Message: fmt.Sprintf("the Worktree at %s isn't served yet", w.Path)}
	}
	cfg, err := d.plugins.ResolveDir(s.dir)
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

// up starts proc, or the whole Worktree if proc is nil. A manual Process
// starts on its own. Lazy and always Processes run with their Worktree, so
// for them the Worktree starts the way a request starts it, also when it is
// already running: that starts any of its Processes that stopped or failed.
// It waits for the Primary Process to accept connections.
func (d *Daemon) up(ctx context.Context, s *served, proc *recipe.Process) error {
	if proc != nil && proc.StartMode == recipe.Manual {
		s.lifecycle.Lock()
		defer s.lifecycle.Unlock()
		envChanged := d.applyServedEnvPatch(s)
		cfg, err := d.resolveWorktree(ctx, s.dir)
		if err != nil {
			return &api.Error{Code: api.CodeInvalidConfig, Message: err.Error()}
		}
		if envChanged {
			d.restartForEnv(s, cfg)
		}
		_, err = d.startProcess(s, d.specs(s, cfg, []recipe.Process{*proc})[0], true)
		return err
	}
	s.mu.Lock()
	if s.start != nil && isDone(s.start) {
		s.start.releaseServices()
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
			"the Primary Process %q %s (%v); see its output with `lane logs %s`", se.Process, what, se.Err, se.Process)}
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
		if proc.StartMode == recipe.Always && !d.runsAlways(s) {
			releaseAlwaysServices(s)
		}
		return
	}
	releaseAlwaysServices(s)
	s.mu.Lock()
	if s.start != nil && isDone(s.start) {
		s.start.releaseServices()
	}
	s.start, s.idle = nil, false
	s.mu.Unlock()
	s.activity.Reset()
	d.supervisor.StopGroup(ctx, s.group)
}

// runsAlways reports whether any of the Worktree's always Processes runs.
func (d *Daemon) runsAlways(s *served) bool {
	cfg, err := d.plugins.ResolveDir(s.dir)
	if err != nil {
		return false
	}
	for _, p := range cfg.Processes {
		if _, running := d.supervisor.Process(s.group, p.Name); running && p.StartMode == recipe.Always {
			return true
		}
	}
	return false
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

// withState fills in the setup and run state of Worktree w and its running
// Processes.
func (d *Daemon) withState(w api.Worktree) api.Worktree {
	w.Setup = d.setupOf(w.Path)
	w.State = d.worktreeState(w.Domain)
	w.Processes = nil
	for _, st := range d.supervisor.Processes(w.Path) {
		w.Processes = append(w.Processes, api.Process{Name: st.Name, State: string(st.State)})
	}
	return w
}
