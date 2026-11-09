package daemon

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/lanewayhq/laneway/internal/envpatch"
	"github.com/lanewayhq/laneway/internal/plugin"
	"github.com/lanewayhq/laneway/internal/prepare"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/registry"
)

// effectTarget is where a Plugin's effects apply (ADR-0007): Laneway does
// what they ask under its usual rules, in the Worktree they are about.
type effectTarget struct {
	project  registry.Project
	worktree worktreeRef
	// cfg is the Project's config, as the Worktree's setup steps see it.
	cfg recipe.Resolved
	// dir is where exec effects run: the Worktree's directory, or the
	// main Worktree's for a removed Worktree.
	dir string
	// removed is true when the Worktree is gone, so it has no .env to set.
	removed bool
	// source names what returned the effects in errors and warnings, such
	// as "Plugin notify's Ready hook".
	source string
}

// applyEffects applies effects for t in order. Commands' output goes to
// out. Warning effects, and effects Laneway can't apply here, go to warn.
// It stops at the first effect that fails, such as a command exiting
// non-zero, and returns why. To add an effect kind, add its case here.
func (d *Daemon) applyEffects(ctx context.Context, t effectTarget, effects []plugin.Effect, out io.Writer, warn func(string)) error {
	var env []string // exec effects' environment, worked out once
	for _, e := range effects {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch e.Kind {
		case plugin.EffectEnv:
			if err := d.applyEnvEffect(t, e.Env, warn); err != nil {
				return fmt.Errorf("%s: setting %s in .env: %w", t.source, strings.Join(slices.Sorted(maps.Keys(e.Env)), ", "), err)
			}
		case plugin.EffectExec:
			if env == nil {
				env = d.setupEnv(ctx, t.project, t.worktree, t.dir, t.cfg)
			}
			fmt.Fprintf(out, "$ %s\n", e.Command)
			if err := prepare.Shell(e.Command, e.Command, t.dir, env).Run(ctx, out); err != nil {
				return fmt.Errorf("%s: the command %q failed: %w", t.source, e.Command, err)
			}
		case plugin.EffectWarning:
			warn(t.source + ": " + e.Message)
		default:
			warn(fmt.Sprintf("%s returned an effect of kind %s, which Laneway doesn't apply here, so it was ignored", t.source, e.Kind))
		}
	}
	return nil
}

// applyEnvEffect sets env in the Worktree's .env the way the Env Patch sets
// its keys: changing only those keys, creating .env from the same seeds,
// and backing up the original first. Keys the Env Patch owns are left to
// it, with a warning, since laneway.toml's env decides them.
func (d *Daemon) applyEnvEffect(t effectTarget, env map[string]string, warn func(string)) error {
	if t.removed {
		warn(fmt.Sprintf("%s set %s, but the Worktree was removed, so there is no .env to set them in",
			t.source, strings.Join(slices.Sorted(maps.Keys(env)), ", ")))
		return nil
	}
	owned := t.cfg.EnvFor(t.worktree.main)
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
