package daemon

import (
	"context"

	"github.com/mrsafalpiya/portby.dev/internal/recipe"
	"github.com/mrsafalpiya/portby.dev/internal/runtimes"
	"github.com/mrsafalpiya/portby.dev/internal/supervisor"
)

// worktreeConfig is a Worktree's resolved config plus the environment its
// Processes run with.
type worktreeConfig struct {
	recipe.Resolved
	// env is d.cfg.Env with the Worktree's Runtimes resolved into it.
	env []string
	// notes are the Runtime warnings, for the Processes' output.
	notes []string
}

// resolveWorktree resolves the Runtimes and the config of the Worktree in
// dir (through mise, else PATH; see package runtimes) and logs any warnings.
// Like the config, Runtimes are resolved on every start.
func (d *Daemon) resolveWorktree(ctx context.Context, dir string) (worktreeConfig, error) {
	env := runtimes.Environ(ctx, dir, d.cfg.Env)
	res, err := recipe.ResolveDir(dir, env.Find(ctx)...)
	if err != nil {
		return worktreeConfig{}, err
	}
	w := worktreeConfig{Resolved: res, env: env.Vars}
	for _, warning := range append(env.Warnings, res.RuntimeWarnings...) {
		d.log.Warn("Runtime", "dir", dir, "warning", warning)
		w.notes = append(w.notes, "Runtime warning: "+warning.String())
	}
	return w, nil
}

// apply gives specs the Worktree's environment, and writes the Runtime
// warnings into the Primary Process's output (the first Process's if there is
// no Primary), so `portby logs` and the start error page show them.
func (w worktreeConfig) apply(specs []supervisor.Spec, primary string) {
	for i := range specs {
		specs[i].Env = w.env
		if specs[i].Name == primary || (primary == "" && i == 0) {
			specs[i].Notes = w.notes
		}
	}
}
