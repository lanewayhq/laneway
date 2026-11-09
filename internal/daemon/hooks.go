package daemon

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/lanewayhq/laneway/internal/plugin"
	"github.com/lanewayhq/laneway/internal/prepare"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/registry"
)

// Plugins' hooks on lifecycle events (ADR-0007). A hook fires only in
// Projects that use its Plugin (plugin.Host.Hooking), gets the event as
// JSON, and returns effects the daemon applies (effects.go). Only Preparing
// can block: its hooks are setup steps, so a failure fails setup like any
// step and is retried with it. Every other hook runs in the background, and
// its failures become warnings. To fire a new event, build its
// plugin.Event and run its hooks with fireHooks.

// hookEvent is the event name about the Worktree w of the Project p.
func (d *Daemon) hookEvent(name string, p registry.Project, w worktreeRef) plugin.Event {
	vars := d.envVars(p, w, recipe.Database{})
	return plugin.Event{
		Name:    name,
		Project: plugin.EventProject{Name: p.Name, Path: p.Path},
		Worktree: &plugin.EventWorktree{
			Name: w.name, Path: w.dir, Main: w.main, Domain: vars["domain"], URL: vars["url"],
		},
	}
}

// preparingHookSteps are the setup steps running the Preparing hooks of the
// Plugins the Worktree w's Project uses (cfg is its config), one step per
// Plugin. A hook that fails, or an effect of one that does, fails its step.
func (d *Daemon) preparingHookSteps(p registry.Project, w worktreeRef, cfg recipe.Resolved) []prepare.Step {
	var steps []prepare.Step
	for _, pl := range d.plugins.Hooking(plugin.EventPreparing, cfg) {
		steps = append(steps, prepare.Step{
			Name: "Preparing hook of Plugin " + pl.Name,
			Run: func(ctx context.Context, out io.Writer) error {
				t := d.hookTarget(pl, plugin.EventPreparing, p, w, cfg, false)
				warn := func(w string) { prepare.Warn(ctx, w) }
				return d.runHook(ctx, t, d.hookEvent(plugin.EventPreparing, p, w), out, warn)
			},
		})
	}
	return steps
}

// hookTarget is where the effects of pl's hook for event apply: the
// Worktree w, or for a removed one the main Worktree. cfg is the config of
// that directory.
func (d *Daemon) hookTarget(pl plugin.Plugin, event string, p registry.Project, w worktreeRef, cfg recipe.Resolved, removed bool) *effectTarget {
	t := &effectTarget{plugin: pl, project: p, worktree: w, dir: w.dir, removed: removed, cfg: &cfg,
		source: plugin.HookSource(pl, event)}
	t.warningPrefix = t.source + ": "
	if removed {
		t.dir = p.Path
	}
	return t
}

// runHook runs the hook of t's Plugin for ev, with t's directory mounted as
// the Project, and applies the effects it returns. An error names the hook.
func (d *Daemon) runHook(ctx context.Context, t *effectTarget, ev plugin.Event, out io.Writer, warn func(string)) error {
	effects, err := d.plugins.Hook(t.plugin, ev, t.dir)
	if err != nil {
		return err
	}
	if err := d.applyEffects(ctx, t, effects, out, warn); err != nil {
		return fmt.Errorf("%s: %w", t.source, err)
	}
	return nil
}

// fireHooks runs the hooks of event in the background, for the Worktree w
// of the Project p: in the Worktree, or for a removed one in the main
// Worktree. Each Plugin's failure, and each warning, goes to warn.
func (d *Daemon) fireHooks(event string, p registry.Project, w worktreeRef, removed bool, warn func(string)) {
	dir := w.dir
	if removed {
		dir = p.Path
	}
	d.preparer.Go(fmt.Sprintf("%s hooks for Worktree %q of Project %q", plugin.EventTitle(event), w.name, p.Name),
		func(ctx context.Context) ([]prepare.Step, error) {
			cfg, err := d.plugins.ResolveDir(dir)
			if err != nil {
				warn(fmt.Sprintf("%s hooks didn't run: %v", plugin.EventTitle(event), err))
				return nil, nil
			}
			var steps []prepare.Step
			for _, pl := range d.plugins.Hooking(event, cfg) {
				t := d.hookTarget(pl, event, p, w, cfg, removed)
				steps = append(steps, prepare.Step{Name: t.source, Run: func(ctx context.Context, _ io.Writer) error {
					out := &outputTail{}
					if err := d.runHook(ctx, t, d.hookEvent(event, p, w), out, warn); err != nil && ctx.Err() == nil {
						warn(failureWarning(err, out))
					}
					return nil // one Plugin's failure doesn't stop another's hook
				}})
			}
			return steps, nil
		})
}

// worktreeBecameReady fires the Ready hooks of the Worktree at dir, once
// its setup is Ready. Their warnings join its setup's.
func (d *Daemon) worktreeBecameReady(dir string) {
	for _, p := range d.registry.Projects() {
		if w, ok := d.worktreeRefOf(p, dir); ok {
			d.fireHooks(plugin.EventReady, p, w, false, func(warning string) {
				d.log.Warn("Ready hook", "project", p.Name, "worktree", dir, "warning", warning)
				d.preparer.AddWarning(dir, warning)
			})
			return
		}
	}
}

// worktreeRemoved fires the Worktree removed hooks of the Worktree w,
// which is gone. Their warnings are logged, since the Worktree has no
// setup left to show them.
func (d *Daemon) worktreeRemoved(p registry.Project, w worktreeRef) {
	d.fireHooks(plugin.EventWorktreeRemoved, p, w, true, func(warning string) {
		d.log.Warn("Worktree removed hook", "project", p.Name, "worktree", w.dir, "warning", warning)
	})
}

// failureWarning is the warning for a background hook that failed: its
// error and the last lines of its commands' output.
func failureWarning(err error, out *outputTail) string {
	msg := err.Error()
	if lines := out.lines(5); len(lines) > 0 {
		msg += "\n" + strings.Join(lines, "\n")
	}
	return msg
}

// outputTail keeps the end of a background hook's command output, for its
// warning.
type outputTail struct {
	buf []byte
}

// maxHookOutput is how much of a background hook's output outputTail keeps.
const maxHookOutput = 16 << 10

func (o *outputTail) Write(b []byte) (int, error) {
	o.buf = append(o.buf, b...)
	if len(o.buf) > maxHookOutput {
		o.buf = o.buf[len(o.buf)-maxHookOutput:]
	}
	return len(b), nil
}

func (o *outputTail) lines(n int) []string {
	s := strings.TrimRight(string(o.buf), "\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	return lines[max(0, len(lines)-n):]
}
