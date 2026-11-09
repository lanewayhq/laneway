package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/plugin"
	"github.com/lanewayhq/laneway/internal/registry"
	"github.com/lanewayhq/laneway/internal/runtimes"
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
	a := commandApplier{d: d, plugin: p, project: project, worktree: w}
	for _, e := range effects {
		if err := a.apply(ctx, e, &res); err != nil {
			res.Error = err.Error()
			break
		}
	}
	return res, nil
}

// commandApplier applies the effects a Plugin's command returns in a
// Worktree. It applies exec and warning effects; Plugin hooks (#59) bring the
// others.
type commandApplier struct {
	d        *Daemon
	plugin   plugin.Plugin
	project  registry.Project
	worktree worktreeRef
	env      []string // for exec effects, once one runs
}

// apply applies e, adding what it did to res. An effect of a kind the
// Plugin's manifest doesn't declare is refused with a warning, not applied.
func (a *commandApplier) apply(ctx context.Context, e plugin.Effect, res *api.PluginCommandResult) error {
	if _, ok := plugin.EffectKinds[e.Kind]; !ok {
		res.Warnings = append(res.Warnings, fmt.Sprintf("Plugin %s returned an effect of kind %q, which isn't an effect kind; it was ignored", a.plugin.Name, e.Kind))
		return nil
	}
	if !a.plugin.Declares(e.Kind) {
		res.Warnings = append(res.Warnings, fmt.Sprintf("Plugin %s returned an %s effect, but its manifest doesn't declare it may %s, so Laneway refused it",
			a.plugin.Name, e.Kind, plugin.EffectKinds[e.Kind]))
		return nil
	}
	switch e.Kind {
	case plugin.EffectWarning:
		res.Warnings = append(res.Warnings, e.Message)
		return nil
	case plugin.EffectExec:
		return a.exec(ctx, e.Command, res)
	}
	res.Warnings = append(res.Warnings, fmt.Sprintf("Plugin %s returned an %s effect, but Laneway doesn't apply %s effects from commands yet, so it was skipped",
		a.plugin.Name, e.Kind, e.Kind))
	return nil
}

// exec runs command in the Worktree as setup steps run: with its Runtimes
// and the LANEWAY_* variables (see setupEnv).
func (a *commandApplier) exec(ctx context.Context, command []string, res *api.PluginCommandResult) error {
	if len(command) == 0 {
		return fmt.Errorf("Plugin %s returned an exec effect without a command", a.plugin.Name)
	}
	dir := a.worktree.dir
	if a.env == nil {
		cfg, err := a.d.plugins.ResolveDir(dir)
		if err != nil {
			return err
		}
		a.env = a.d.setupEnv(ctx, a.project, a.worktree, dir, cfg)
	}
	path, err := runtimes.Environment{Dir: dir, Vars: a.env}.LookPath(command[0])
	if err != nil {
		return runtimes.NotFound(dir, command[0])
	}
	out := &cappedBuffer{max: maxCommandOutput}
	cmd := exec.CommandContext(ctx, path, command[1:]...)
	cmd.Args[0] = command[0]
	cmd.Dir = dir
	cmd.Env = a.env
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	res.Effects = append(res.Effects, api.AppliedEffect{Kind: plugin.EffectExec, Command: command, Output: out.String()})
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("`%s` exited with status %d", strings.Join(command, " "), exitErr.ExitCode())
	}
	return err
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
