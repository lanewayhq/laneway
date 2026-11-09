package daemon

import (
	"context"
	"fmt"
	"io"
	"slices"
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
// its failures become warnings. To fire a new event, describe it as an
// occurrence (worktreeHooks, projectHooks or loggedHooks) and pass it to
// fireHooks; lifecycle_hooks.go fires the events after Ready.

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
				t := d.hookTarget(pl, plugin.EventPreparing, p, w, cfg, "")
				warn := func(w string) { prepare.Warn(ctx, w) }
				return d.runHook(ctx, t, d.hookEvent(plugin.EventPreparing, p, w), out, warn)
			},
		})
	}
	return steps
}

// hookTarget is where the effects of pl's hook for event apply: the
// Worktree w, or once it is gone (see effectTarget.gone) the main Worktree.
// cfg is the config of that directory.
func (d *Daemon) hookTarget(pl plugin.Plugin, event string, p registry.Project, w worktreeRef, cfg recipe.Resolved, gone string) *effectTarget {
	t := &effectTarget{plugin: pl, project: p, worktree: w, dir: w.dir, gone: gone, cfg: &cfg,
		source: plugin.HookSource(pl, event)}
	t.warningPrefix = t.source + ": "
	if gone != "" {
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

// occurrence is one time an event happened, whose hooks fireHooks runs.
type occurrence struct {
	event   string
	project registry.Project
	// worktree is the Worktree it happened to, where effects apply; the
	// main Worktree for an event about the whole Project.
	worktree worktreeRef
	// wholeProject is true for an event about the whole Project (Link,
	// Unlink, Service events): the event names no Worktree.
	wholeProject bool
	// gone is why the Worktree (or Project) has no .env Laneway manages
	// any more, such as "the Worktree was removed"; "" while it has. Exec
	// effects then run in the main Worktree.
	gone string
	// previousDomain, process and service describe the event, for the
	// events that have them.
	previousDomain string
	process        *plugin.EventProcess
	service        *plugin.EventService
	// warn gets each Plugin's failure, and each warning.
	warn func(string)
}

// event is o as its hooks are told it.
func (d *Daemon) event(o occurrence) plugin.Event {
	ev := d.hookEvent(o.event, o.project, o.worktree)
	if o.wholeProject {
		ev.Worktree = nil
	} else {
		ev.Worktree.PreviousDomain = o.previousDomain
	}
	ev.Process, ev.Service = o.process, o.service
	return ev
}

// fireHooks runs the hooks of o's event in the background: those of the
// Plugins its Project uses, with the Worktree's directory mounted (the main
// Worktree's once it is gone). A Service event's hooks run only in Projects
// whose config uses the Service. None of them can block or fail anything:
// each Plugin's failure, and each warning, goes to o.warn.
func (d *Daemon) fireHooks(o occurrence) {
	if !d.plugins.Hooked(o.event) {
		return // most events, most of the time: don't read config for nothing
	}
	// The event is worked out now, while the Registry still has what it
	// says (an Unlinked Project's Aliases).
	ev := d.event(o)
	dir := o.worktree.dir
	if o.gone != "" {
		dir = o.project.Path
	}
	what := fmt.Sprintf("%s hooks for Project %q", plugin.EventTitle(o.event), o.project.Name)
	if !o.wholeProject {
		what = fmt.Sprintf("%s hooks for Worktree %q of Project %q", plugin.EventTitle(o.event), o.worktree.name, o.project.Name)
	}
	d.preparer.Go(what, func(ctx context.Context) ([]prepare.Step, error) {
		cfg, err := d.plugins.ResolveDir(dir)
		if err != nil {
			o.warn(fmt.Sprintf("%s hooks didn't run: %v", plugin.EventTitle(o.event), err))
			return nil, nil
		}
		if o.service != nil && !slices.ContainsFunc(cfg.Services, func(s recipe.Service) bool {
			return s.Engine == o.service.Engine && s.Version == o.service.Version
		}) {
			return nil, nil
		}
		var steps []prepare.Step
		for _, pl := range d.plugins.Hooking(o.event, cfg) {
			t := d.hookTarget(pl, o.event, o.project, o.worktree, cfg, o.gone)
			steps = append(steps, prepare.Step{Name: t.source, Run: func(ctx context.Context, _ io.Writer) error {
				out := &outputTail{}
				if err := d.runHook(ctx, t, ev, out, o.warn); err != nil && ctx.Err() == nil {
					o.warn(failureWarning(err, out))
				}
				return nil // one Plugin's failure doesn't stop another's hook
			}})
		}
		return steps, nil
	})
}

// worktreeHooks is an occurrence of event in the Worktree w of the Project
// p, whose hooks' warnings join w's setup's (see Preparer.AddWarning), as
// `lane worktree ready` and `lane status` show them, and the daemon log.
func (d *Daemon) worktreeHooks(event string, p registry.Project, w worktreeRef) occurrence {
	return occurrence{event: event, project: p, worktree: w, warn: func(warning string) {
		d.log.Warn(plugin.EventTitle(event)+" hook", "project", p.Name, "worktree", w.dir, "warning", warning)
		d.preparer.AddWarning(w.dir, warning)
	}}
}

// projectHooks is an occurrence of event in the whole Project p, whose
// hooks' warnings join its main Worktree's setup's.
func (d *Daemon) projectHooks(event string, p registry.Project) occurrence {
	o := d.worktreeHooks(event, p, mainWorktreeRef(p))
	o.wholeProject = true
	return o
}

// loggedHooks is an occurrence of event in the Worktree w of the Project p,
// which is gone (gone says why), so its hooks' warnings only go to the
// daemon log: there's no setup left to show them.
func (d *Daemon) loggedHooks(event string, p registry.Project, w worktreeRef, gone string) occurrence {
	return occurrence{event: event, project: p, worktree: w, gone: gone, warn: func(warning string) {
		d.log.Warn(plugin.EventTitle(event)+" hook", "project", p.Name, "worktree", w.dir, "warning", warning)
	}}
}

// worktreeBecameReady fires the Ready hooks of the Worktree at dir, once
// its setup is Ready.
func (d *Daemon) worktreeBecameReady(dir string) {
	for _, p := range d.registry.Projects() {
		if w, ok := d.worktreeRefOf(p, dir); ok {
			d.fireHooks(d.worktreeHooks(plugin.EventReady, p, w))
			return
		}
	}
}

// worktreeRemoved fires the Worktree removed hooks of the Worktree w,
// which is gone.
func (d *Daemon) worktreeRemoved(p registry.Project, w worktreeRef) {
	d.fireHooks(d.loggedHooks(plugin.EventWorktreeRemoved, p, w, "the Worktree was removed"))
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
