package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/provision"
	"github.com/mrsafalpiya/portby.dev/internal/proxy"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
	"github.com/mrsafalpiya/portby.dev/internal/runtimes"
)

// setupPlan is what setting up the Worktree w does, in the spec's order:
//
//	database → Test Database → env → setup steps (on_create)
//
// It runs when the Worktree's setup starts, so config is read then.
//
// The database step (database.go) gives a Project with a database engine
// its database; other engines extend it there, not here. The Test Database
// step (testdatabase.go) gives each Worktree an empty Test Database when
// config's Test Database decision is on. To add a step, append a provision.Step where that order puts it. A
// step's Name is what `portby worktree ready`, `portby status` and the setup
// error page show when it fails; anything it writes to its output is shown
// with it, and provision.Warn reports a problem it worked around. It must stop
// when its ctx is cancelled (the Worktree was removed or the daemon is
// stopping) and should be safe to run again: a Failed setup is retried from
// the first step, and so is one the daemon stopped in the middle of.
func (d *Daemon) setupPlan(p registry.Project, w envWorktree) provision.Plan {
	return func(ctx context.Context) ([]provision.Step, error) {
		cfg, err := recipe.ResolveDir(w.dir)
		if err != nil {
			return nil, err
		}
		var steps []provision.Step
		if step, ok := d.databaseStep(p, w, cfg); ok {
			steps = append(steps, step)
		}
		if step, ok := d.testDatabaseStep(p, w, cfg); ok {
			steps = append(steps, step)
		}
		steps = append(steps, provision.Step{Name: "env", Run: func(ctx context.Context, _ io.Writer) error {
			plan, err := d.planEnvPatch(p, w)
			if err != nil {
				return fmt.Errorf("the Env Patch for %s: %w", w.dir, err)
			}
			if err := plan.Apply(); err != nil {
				return err
			}
			testPlan, missing, err := d.planTestEnvPatch(p, w)
			if err != nil {
				return fmt.Errorf("the Env Patch for %s: %w", filepath.Join(w.dir, testEnvFileName), err)
			}
			if missing != "" {
				provision.Warn(ctx, testEnvWarning(p, missing))
			}
			return testPlan.Apply()
		}})
		env := d.setupEnv(ctx, p, w, w.dir, cfg)
		for _, command := range cfg.OnCreate {
			steps = append(steps, provision.Shell("on_create: "+command, command, w.dir, env))
		}
		return steps, nil
	}
}

// provision begins setting up the Worktree w unless it is being set up or
// has been. With retry, a Failed setup runs again.
func (d *Daemon) provision(p registry.Project, w envWorktree, retry bool) {
	d.provisioner.Begin(w.dir, d.setupPlan(p, w), retry)
}

// teardown forgets the setup of the removed Worktree w, stopping it if it is
// still running, and runs the Project's on_remove steps in the main
// Worktree's directory in the background.
func (d *Daemon) teardown(p registry.Project, w envWorktree) {
	d.provisioner.Forget(w.dir)
	d.provisioner.Go(fmt.Sprintf("on_remove for Worktree %q of Project %q", w.name, p.Name),
		func(ctx context.Context) ([]provision.Step, error) {
			cfg, err := recipe.ResolveDir(p.Path)
			if err != nil || len(cfg.OnRemove) == 0 {
				return nil, err
			}
			env := d.setupEnv(ctx, p, w, p.Path, cfg)
			var steps []provision.Step
			for _, command := range cfg.OnRemove {
				steps = append(steps, provision.Shell("on_remove: "+command, command, p.Path, env))
			}
			return steps, nil
		})
}

// setupEnv is the environment setup steps for the Worktree w run with in
// dir: the one Processes start from (daemon.Config.Env) with dir's Runtimes
// resolved into it, as for Processes, so `composer install` finds the
// Project's PHP; plus PORTBY_<VARIABLE> for every Env Patch template
// variable (PORTBY_PROJECT, PORTBY_WORKTREE, PORTBY_DOMAIN, PORTBY_URL,
// PORTBY_DB, PORTBY_TEST_DB, …) and PORTBY_WORKTREE_PATH, so on_remove
// steps know which Worktree went. cfg is the config the steps come from.
func (d *Daemon) setupEnv(ctx context.Context, p registry.Project, w envWorktree, dir string, cfg recipe.Resolved) []string {
	env := runtimes.Environ(ctx, dir, d.cfg.Env).Vars
	vars := envVars(p, w, cfg.Database)
	if name, ok := worktreeTestDatabase(p, w, cfg); ok {
		vars["test_db"] = name
	}
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		env = append(env, "PORTBY_"+strings.ToUpper(k)+"="+vars[k])
	}
	return append(env, "PORTBY_WORKTREE_PATH="+w.dir)
}

// setupOf is the API view of the setup state of the Worktree at dir.
func (d *Daemon) setupOf(dir string) *api.Setup {
	st := d.provisioner.Status(dir)
	return &api.Setup{State: string(st.State), Step: st.Step, Error: st.Error, Output: st.Output, Warnings: st.Warnings}
}

// worktreeReady finds the Worktree containing req.Dir (discovering it at once
// if git has it but the Worktree Watcher hasn't reported it), begins or
// retries its setup and, with req.Wait, waits until it is Ready or Failed.
func (d *Daemon) worktreeReady(ctx context.Context, req api.ReadyRequest) (api.WorktreeInfo, error) {
	p, checkout, err := d.worktreeAt(ctx, req.Dir)
	if err != nil {
		return api.WorktreeInfo{}, err
	}
	w, ok := d.envWorktreeOf(p, checkout.Dir)
	if !ok {
		d.syncWorktrees(ctx, p)
		if w, ok = d.envWorktreeOf(p, checkout.Dir); !ok {
			return api.WorktreeInfo{}, &api.Error{
				Code:    api.CodeUnknownWorktree,
				Message: fmt.Sprintf("git doesn't list %s as a worktree of %s yet", checkout.Dir, p.Path),
			}
		}
	}
	retried := req.Retry && d.provisioner.Status(w.dir).State == provision.Failed
	d.provision(p, w, req.Retry)
	if retried {
		// The always Processes didn't start while setup was Failed.
		d.servedMu.Lock()
		s := d.served[w.domain]
		d.servedMu.Unlock()
		if s != nil {
			d.startAlways(s)
		}
	}
	if req.Wait {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			select {
			case <-d.closing:
				cancel()
			case <-ctx.Done():
			}
		}()
		_, err := d.provisioner.Wait(ctx, w.dir)
		switch {
		case errors.Is(err, provision.ErrForgotten):
			return api.WorktreeInfo{}, &api.Error{Code: api.CodeUnknownWorktree, Message: fmt.Sprintf("%s was removed", w.dir)}
		case errors.Is(err, provision.ErrClosed) || isClosing(d.closing):
			return api.WorktreeInfo{}, &api.Error{Code: api.CodeShuttingDown, Message: "the Portby daemon stopped while waiting"}
		case err != nil:
			return api.WorktreeInfo{}, err
		}
	}
	info, err := d.worktreeInfo(ctx, req.Dir)
	if err != nil {
		return api.WorktreeInfo{}, err
	}
	return info, nil
}

func isClosing(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// envWorktreeOf is the Project's Worktree at dir, if the Registry knows it.
func (d *Daemon) envWorktreeOf(p registry.Project, dir string) (envWorktree, bool) {
	if dir == p.Path {
		return mainEnvWorktree(p), true
	}
	for _, w := range d.registry.Worktrees(p.Path) {
		if w.Path == dir {
			return linkedEnvWorktree(p, w), true
		}
	}
	return envWorktree{}, false
}

// waitForSetup waits until the served Worktree s is Ready, so a start never
// begins Processes in a Worktree that isn't. It returns a *proxy.SetupError
// if its setup failed.
func (d *Daemon) waitForSetup(s *served) error {
	st, err := d.provisioner.Wait(s.ctx, s.dir)
	if err != nil {
		return fmt.Errorf("Worktree %s of Project %q isn't set up: %w", s.dir, s.project, err)
	}
	if st.State == provision.Failed {
		return &proxy.SetupError{Step: st.Step, Output: st.Output, Err: fmt.Errorf(
			"setup of Worktree %s of Project %q failed at step %q: %s", s.dir, s.project, st.Step, st.Error)}
	}
	return nil
}

// setupRetried reports whether s's last start failed because its setup
// had, and setup has been retried since, so the next request starts it
// again. The caller holds s.mu.
func (d *Daemon) setupRetried(s *served) bool {
	select {
	case <-s.start.done:
	default:
		return false
	}
	_, failed := errors.AsType[*proxy.SetupError](s.start.err)
	return failed && d.provisioner.Status(s.dir).State != provision.Failed
}
