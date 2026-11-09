package daemon

import (
	"bytes"
	"context"
	"fmt"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/plugin"
)

// maxCommandOutput is how much of an exec effect's output a Plugin command's
// result keeps.
const maxCommandOutput = 1 << 20

// runPluginCommand runs a Plugin's command, `lane <plugin> <command>`, in the
// Worktree req names, and applies the effects it returns, in order. A
// command that fails, or an effect that does, ends it: the result says why,
// and what the effects before it did.
func (d *Daemon) runPluginCommand(ctx context.Context, name, command string, req api.PluginCommandRequest) (api.PluginCommandResult, error) {
	p, ok := d.plugins.Plugin(name)
	if !ok {
		return api.PluginCommandResult{}, &api.Error{Code: api.CodeUnknownPlugin,
			Message: fmt.Sprintf("no Plugin called %s is installed; `lane plugin list` lists them", name)}
	}
	if !p.HasCommand(command) {
		return api.PluginCommandResult{}, &api.Error{Code: api.CodeUnknownCommand,
			Message: fmt.Sprintf("Plugin %s has no command %s; `lane %s --help` lists its commands", name, command, name)}
	}
	dir, err := d.worktreeDir(ctx, req.Dir, req.Worktree)
	if err != nil {
		return api.PluginCommandResult{}, err
	}
	project, checkout, err := d.worktreeAt(ctx, dir)
	if err != nil {
		return api.PluginCommandResult{}, err
	}
	w, ok := d.worktreeRefOf(project, checkout.Dir)
	if !ok {
		// One git has but the Worktree Watcher hasn't reported yet.
		d.syncWorktrees(ctx, project)
		if w, ok = d.worktreeRefOf(project, checkout.Dir); !ok {
			return api.PluginCommandResult{}, &api.Error{Code: api.CodeUnknownWorktree,
				Message: fmt.Sprintf("%s isn't a Worktree of Project %s yet; try again in a moment", checkout.Dir, project.Name)}
		}
	}

	res := api.PluginCommandResult{Effects: []api.AppliedEffect{}, Warnings: []string{}}
	effects, err := d.plugins.Command(p, command, req.Args, w.dir)
	if err != nil {
		res.Error = err.Error()
		return res, nil
	}
	// The CLI says which Plugin warnings are from (lane <plugin>: warning:).
	t := &effectTarget{plugin: p, project: project, worktree: w, dir: w.dir, source: "Plugin " + p.Name}
	warn := func(w string) { res.Warnings = append(res.Warnings, w) }
	for _, e := range checkEffects(t, effects, warn) {
		out := &cappedBuffer{max: maxCommandOutput}
		err := d.applyEffect(ctx, t, e, out, warn)
		if e.Kind == plugin.EffectExec {
			res.Effects = append(res.Effects, api.AppliedEffect{Kind: e.Kind, Command: e.Command, Output: out.String()})
		}
		if err != nil {
			res.Error = err.Error()
			break
		}
	}
	return res, nil
}

// cappedBuffer keeps the first max bytes written to it, and says if it
// dropped any.
type cappedBuffer struct {
	bytes.Buffer
	max     int
	dropped bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.max - b.Len()
	if room < len(p) {
		b.dropped = true
	}
	b.Buffer.Write(p[:max(0, min(len(p), room))])
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	if b.dropped {
		return b.Buffer.String() + "\n[output cut at 1 MiB]\n"
	}
	return b.Buffer.String()
}
