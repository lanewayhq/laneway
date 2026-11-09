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

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/prepare"
	"github.com/lanewayhq/laneway/internal/proxy"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/registry"
	"github.com/lanewayhq/laneway/internal/runtimes"
)

// setupPlan is what preparing the Worktree w does, in the spec's order:
//
//	copy → database → Test Database → env → setup steps (on_create)
//
// It runs when the Worktree's setup starts, so config is read then.
//
// The copy step (copy.go) copies the paths in config's copy from the main
// Worktree, only into the other Worktrees.
// The database step (database.go) gives a Project with a database engine
// its database; other engines extend it there, not here. The Test Database
// step (testdatabase.go) gives each Worktree an empty Test Database when
// config's Test Database decision is on. To add a step, append a prepare.Step where that order puts it. A
// step's Name is what `lane worktree ready`, `lane status` and the setup
// error page show when it fails; anything it writes to its output is shown
// with it, and prepare.Warn reports a problem it worked around. It must stop
// when its ctx is cancelled (the Worktree was removed or the daemon is
// stopping) and should be safe to run again: a Failed setup is retried from
// the first step, and so is one the daemon stopped in the middle of.
func (d *Daemon) setupPlan(p registry.Project, w worktreeRef) prepare.Plan {
	return func(ctx context.Context) ([]prepare.Step, error) {
		cfg, err := d.plugins.ResolveDir(w.dir)
		if err != nil {
			return nil, err
		}
		var steps []prepare.Step
		if !w.main && len(cfg.Copy) > 0 {
			steps = append(steps, copyStep(p, w, cfg.Copy))
		}
		if step, ok := d.databaseStep(p, w, cfg); ok {
			steps = append(steps, step)
		}
		if step, ok := d.testDatabaseStep(p, w, cfg); ok {
			steps = append(steps, step)
		}
		steps = append(steps, prepare.Step{Name: "env", Run: func(ctx context.Context, _ io.Writer) error {
			defer d.lockLifecycle(w.domain)()
			plan, err := d.planEnvPatch(p, w)
			if err != nil {
				return fmt.Errorf("the Env Patch for %s: %w", w.dir, err)
			}
			if err := d.applyPlan(p, plan); err != nil {
				return err
			}
			testPlan, missing, err := d.planTestEnvPatch(p, w)
			if err != nil {
				return fmt.Errorf("the Env Patch for %s: %w", filepath.Join(w.dir, testEnvFileName), err)
			}
			if missing != "" {
				prepare.Warn(ctx, testEnvWarning(p, missing))
			}
			return d.applyPlan(p, testPlan)
		}})
		env := d.setupEnv(ctx, p, w, w.dir, cfg)
		for _, command := range cfg.OnCreate {
			steps = append(steps, prepare.Shell("on_create: "+command, command, w.dir, env))
		}
		return steps, nil
	}
}

// prepare begins preparing the Worktree w unless it is Preparing or has
// been. With retry, a Failed Worktree is prepared again.
func (d *Daemon) prepare(p registry.Project, w worktreeRef, retry bool) {
	d.preparer.Begin(w.dir, d.setupPlan(p, w), retry)
}

// teardown forgets the setup of the removed Worktree w, stopping it if it is
// still running, and runs the Project's on_remove steps in the main
// Worktree's directory in the background.
func (d *Daemon) teardown(p registry.Project, w worktreeRef) {
	d.preparer.Forget(w.dir)
	d.preparer.Go(fmt.Sprintf("on_remove for Worktree %q of Project %q", w.name, p.Name),
		func(ctx context.Context) ([]prepare.Step, error) {
			cfg, err := d.plugins.ResolveDir(p.Path)
			if err != nil || len(cfg.OnRemove) == 0 {
				return nil, err
			}
			env := d.setupEnv(ctx, p, w, p.Path, cfg)
			var steps []prepare.Step
			for _, command := range cfg.OnRemove {
				steps = append(steps, prepare.Shell("on_remove: "+command, command, p.Path, env))
			}
			return steps, nil
		})
}

// setupEnv is the environment setup steps for the Worktree w run with in
// dir: the one Processes start from (daemon.Config.Env) with dir's Runtimes
// resolved into it, as for Processes, so `composer install` finds the
// Project's PHP; plus LANEWAY_<VARIABLE> for every Env Patch template
// variable (LANEWAY_PROJECT, LANEWAY_WORKTREE, LANEWAY_DOMAIN, LANEWAY_URL,
// LANEWAY_DB, LANEWAY_TEST_DB, …) and LANEWAY_WORKTREE_PATH, so on_remove
// steps know which Worktree went. cfg is the config the steps come from.
func (d *Daemon) setupEnv(ctx context.Context, p registry.Project, w worktreeRef, dir string, cfg recipe.Resolved) []string {
	env := runtimes.Environ(ctx, dir, d.cfg.Env).Vars
	vars := d.envVars(p, w, cfg.Database)
	if name, ok := worktreeTestDatabase(p, w, cfg); ok {
		vars["test_db"] = name
	}
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		env = append(env, "LANEWAY_"+strings.ToUpper(k)+"="+vars[k])
	}
	return append(env, "LANEWAY_WORKTREE_PATH="+w.dir)
}

// setupOf is the API view of the setup state of the Worktree at dir.
func (d *Daemon) setupOf(dir string) *api.Setup {
	st := d.preparer.Status(dir)
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
	w, ok := d.worktreeRefOf(p, checkout.Dir)
	if !ok {
		d.syncWorktrees(ctx, p)
		if w, ok = d.worktreeRefOf(p, checkout.Dir); !ok {
			return api.WorktreeInfo{}, &api.Error{
				Code:    api.CodeUnknownWorktree,
				Message: fmt.Sprintf("git doesn't list %s as a worktree of %s yet", checkout.Dir, p.Path),
			}
		}
	}
	retried := req.Retry && d.preparer.Status(w.dir).State == prepare.Failed
	d.prepare(p, w, req.Retry)
	if retried {
		// The always Processes didn't start while setup was Failed.
		if s := d.servedWorktree(w.domain); s != nil {
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
		_, err := d.preparer.Wait(ctx, w.dir)
		switch {
		case errors.Is(err, prepare.ErrForgotten):
			return api.WorktreeInfo{}, &api.Error{Code: api.CodeUnknownWorktree, Message: fmt.Sprintf("%s was removed", w.dir)}
		case errors.Is(err, prepare.ErrClosed) || isClosing(d.closing):
			return api.WorktreeInfo{}, &api.Error{Code: api.CodeShuttingDown, Message: "the Laneway daemon stopped while waiting"}
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

// worktreeRefOf is the Project's Worktree at dir, if the Registry knows it.
func (d *Daemon) worktreeRefOf(p registry.Project, dir string) (worktreeRef, bool) {
	if dir == p.Path {
		return mainWorktreeRef(p), true
	}
	for _, w := range d.registry.Worktrees(p.Path) {
		if w.Path == dir {
			return linkedWorktreeRef(p, w), true
		}
	}
	return worktreeRef{}, false
}

// waitForReady waits until the served Worktree s is Ready, so a start never
// begins Processes in a Worktree that isn't. It returns a
// *proxy.PreparationError if preparing it failed.
func (d *Daemon) waitForReady(s *served) error {
	st, err := d.preparer.Wait(s.ctx, s.dir)
	if err != nil {
		return fmt.Errorf("Worktree %s of Project %q isn't Ready: %w", s.dir, s.project, err)
	}
	if st.State == prepare.Failed {
		return &proxy.PreparationError{Step: st.Step, Output: st.Output, Err: fmt.Errorf(
			"preparing Worktree %s of Project %q failed at step %q: %s", s.dir, s.project, st.Step, st.Error)}
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
	_, failed := errors.AsType[*proxy.PreparationError](s.start.err)
	return failed && d.preparer.Status(s.dir).State != prepare.Failed
}
