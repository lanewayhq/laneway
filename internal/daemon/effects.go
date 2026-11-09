package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/lanewayhq/laneway/internal/envpatch"
	"github.com/lanewayhq/laneway/internal/plugin"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/registry"
	"github.com/lanewayhq/laneway/internal/runtimes"
)

// Effects (ADR-0007): what a Plugin's hooks and commands ask Laneway to do.
// Laneway applies them itself, under its usual rules, and only the kinds the
// Plugin's manifest declares. Hooks (hooks.go) and commands
// (plugincommands.go) both apply them here: checkEffects, then applyEffect
// for each in order. To add an effect kind, add its case to applyEffect.

// effectTarget is the Worktree a Plugin's effects apply in.
type effectTarget struct {
	plugin   plugin.Plugin
	project  registry.Project
	worktree worktreeRef
	// dir is where exec effects run: the Worktree's directory, or the
	// main Worktree's once it is gone.
	dir string
	// gone is why the Worktree has no .env for Laneway to set any more,
	// such as "the Worktree was removed"; "" while it has.
	gone string
	// source names what returned the effects in errors and warnings, such
	// as "Plugin tasks" or "Plugin notify's Ready hook".
	source string
	// warningPrefix goes before a warning effect's message, for where the
	// warning is shown without saying who it's from.
	warningPrefix string

	cfg *recipe.Resolved // the config of dir, once read
	env []string         // exec effects' environment, once worked out
}

// config is the config of the directory effects apply in, read once.
func (d *Daemon) effectConfig(t *effectTarget) (recipe.Resolved, error) {
	if t.cfg == nil {
		cfg, err := d.plugins.ResolveDir(t.dir)
		if err != nil {
			return recipe.Resolved{}, err
		}
		t.cfg = &cfg
	}
	return *t.cfg, nil
}

// checkEffects keeps the effects t's Plugin may return, warning about each
// it refuses.
func checkEffects(t *effectTarget, effects []plugin.Effect, warn func(string)) []plugin.Effect {
	allowed, refused := t.plugin.CheckEffects(t.source, effects)
	for _, w := range refused {
		warn(w)
	}
	return allowed
}

// applyEffects checks effects and applies them in order (see applyEffect),
// stopping at the first that fails.
func (d *Daemon) applyEffects(ctx context.Context, t *effectTarget, effects []plugin.Effect, out io.Writer, warn func(string)) error {
	for _, e := range checkEffects(t, effects, warn) {
		if err := d.applyEffect(ctx, t, e, out, warn); err != nil {
			return err
		}
	}
	return nil
}

// applyEffect applies one checked effect for t. Commands' output goes to
// out. Warning effects, and effects Laneway can't apply, go to warn. An
// error is why the effect failed, such as a command exiting non-zero.
func (d *Daemon) applyEffect(ctx context.Context, t *effectTarget, e plugin.Effect, out io.Writer, warn func(string)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch e.Kind {
	case plugin.EffectWarning:
		warn(t.warningPrefix + e.Message)
		return nil
	case plugin.EffectExec:
		return d.execEffect(ctx, t, e.Command, out)
	case plugin.EffectEnv:
		if err := d.envEffect(t, e.Env, warn); err != nil {
			return fmt.Errorf("setting %s in .env: %w", strings.Join(slices.Sorted(maps.Keys(e.Env)), ", "), err)
		}
		return nil
	}
	warn(fmt.Sprintf("%s returned %s effect, but Laneway doesn't apply %s effects yet, so it was skipped",
		t.source, plugin.Article(e.Kind), e.Kind))
	return nil
}

// execEffect runs command (no shell) in t's directory as setup steps run:
// with its Runtimes and the LANEWAY_* variables (see setupEnv).
func (d *Daemon) execEffect(ctx context.Context, t *effectTarget, command []string, out io.Writer) error {
	if t.env == nil {
		cfg, err := d.effectConfig(t)
		if err != nil {
			return err
		}
		t.env = d.setupEnv(ctx, t.project, t.worktree, t.dir, cfg)
	}
	path, err := runtimes.Environment{Dir: t.dir, Vars: t.env}.LookPath(command[0])
	if err != nil {
		return runtimes.NotFound(t.dir, command[0])
	}
	cmd := exec.CommandContext(ctx, path, command[1:]...)
	cmd.Args[0] = command[0]
	cmd.Dir = t.dir
	cmd.Env = t.env
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	// A command that leaves something running in the background holding
	// its output open doesn't hold up what's waiting for it.
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return fmt.Errorf("`%s` exited with status %d", strings.Join(command, " "), exitErr.ExitCode())
	}
	return err
}

// envEffect sets env in the Worktree's .env the way the Env Patch sets its
// keys: changing only those keys, creating .env from the same seeds, and
// backing up the original first. Keys the Env Patch owns are left to it,
// with a warning, since laneway.toml's env decides them.
func (d *Daemon) envEffect(t *effectTarget, env map[string]string, warn func(string)) error {
	if t.gone != "" {
		warn(fmt.Sprintf("%s set %s, but %s, so there is no .env to set them in",
			t.source, strings.Join(slices.Sorted(maps.Keys(env)), ", "), t.gone))
		return nil
	}
	cfg, err := d.effectConfig(t)
	if err != nil {
		return err
	}
	owned := cfg.EnvFor(t.worktree.main)
	values := map[string]string{}
	for _, k := range slices.Sorted(maps.Keys(env)) {
		if _, ok := owned[k]; ok {
			warn(fmt.Sprintf("%s set %s, which the Env Patch sets, so Laneway left it to the Env Patch; "+
				"set env.%s in laneway.toml or laneway.local.toml to change it", t.source, k, k))
			continue
		}
		values[k] = env[k]
	}
	if len(values) == 0 {
		return nil
	}
	defer d.lockLifecycle(t.worktree.domain)()
	plan, err := envpatch.Prepare(envFileOf(t.project, t.worktree), values)
	if err != nil {
		return err
	}
	return d.applyPlan(t.project, plan)
}
