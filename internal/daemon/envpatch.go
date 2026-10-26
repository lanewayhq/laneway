package daemon

import (
	"context"
	"maps"
	"path/filepath"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/envpatch"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
)

// envFileName is the env file the Env Patch goes into.
const envFileName = ".env"

// envWorktree is a Worktree as the Env Patch sees it.
type envWorktree struct {
	name   string // git's name; "" for the main Worktree
	dir    string
	domain string
	main   bool
}

func mainEnvWorktree(p registry.Project) envWorktree {
	return envWorktree{dir: p.Path, domain: domainOf(p), main: true}
}

func linkedEnvWorktree(p registry.Project, w registry.Worktree) envWorktree {
	return envWorktree{name: w.Name, dir: w.Path, domain: worktreeDomain(p, w)}
}

// envVars are the template variables' values for the Worktree w. They are
// the Env Patch's inputs besides the templates: when one changes (an Alias
// changing the URL, #13), call applyEnvPatch for the Worktree again.
func envVars(p registry.Project, w envWorktree) map[string]string {
	return map[string]string{
		"project":  p.Name,
		"worktree": w.name,
		"domain":   w.domain,
		"url":      "https://" + w.domain,
	}
}

// planEnvPatch works out how the Worktree's .env changes to carry its Env
// Patch, from the templates in the Worktree's own config. A Project without
// templates gets no Env Patch: its env files are never touched.
func (d *Daemon) planEnvPatch(p registry.Project, w envWorktree) (envpatch.Plan, error) {
	cfg, err := recipe.ResolveDir(w.dir)
	if err != nil || len(cfg.Env) == 0 {
		return envpatch.Plan{}, err
	}
	vars := envVars(p, w)
	maps.Copy(vars, d.serviceVars(cfg.Services))
	values, err := envpatch.RenderAll(cfg.Env, vars)
	if err != nil {
		return envpatch.Plan{}, err
	}
	f := envpatch.File{Path: filepath.Join(w.dir, envFileName)}
	if !w.main {
		// A new Worktree shares the main Worktree's settings and app key.
		f.Seeds = append(f.Seeds, filepath.Join(p.Path, envFileName))
	}
	f.Seeds = append(f.Seeds, filepath.Join(w.dir, envFileName+".example"))
	return envpatch.Prepare(f, values)
}

// applyEnvPatch brings the Worktree's .env up to date with its Env Patch,
// logging failures. It runs whenever the daemon starts serving a Worktree
// (at daemon start, on Link and on discovery) and whenever a Worktree's
// Processes start, so template changes apply then. Re-applying a patch that
// is in place changes nothing.
func (d *Daemon) applyEnvPatch(p registry.Project, w envWorktree) {
	plan, err := d.planEnvPatch(p, w)
	if err == nil {
		err = plan.Apply()
	}
	if err != nil {
		d.log.Error("applying the Env Patch", "project", p.Name, "worktree", w.dir, "err", err)
	} else if !plan.Empty() {
		d.log.Info("applied the Env Patch", "project", p.Name, "path", plan.Path)
	}
}

// applyServedEnvPatch re-applies the Env Patch of the Worktree served as s,
// before its Processes start, so template changes apply at every start.
func (d *Daemon) applyServedEnvPatch(s *served) {
	for _, p := range d.registry.Projects() {
		if p.Name != s.project {
			continue
		}
		if s.dir == p.Path {
			d.applyEnvPatch(p, mainEnvWorktree(p))
			return
		}
		for _, w := range d.registry.Worktrees(p.Path) {
			if w.Path == s.dir {
				d.applyEnvPatch(p, linkedEnvWorktree(p, w))
				return
			}
		}
	}
}

// projectEnvWorktrees are the Project's Worktrees as they will be once
// syncWorktrees has run: the main one, then the linked ones git knows,
// including those the Registry hasn't recorded yet. It changes nothing.
func (d *Daemon) projectEnvWorktrees(ctx context.Context, p registry.Project) ([]envWorktree, error) {
	d.syncMu.Lock()
	defer d.syncMu.Unlock()
	found, _, err := d.foundWorktrees(ctx, p)
	if err != nil {
		return nil, err
	}
	changes := d.registry.PlanWorktrees(p.Path, found, reservedLabels(p))
	removed := map[string]bool{}
	for _, w := range changes.Removed {
		removed[w.Path] = true
	}
	out := []envWorktree{mainEnvWorktree(p)}
	for _, w := range d.registry.Worktrees(p.Path) {
		if !removed[w.Path] {
			out = append(out, linkedEnvWorktree(p, w))
		}
	}
	for _, w := range changes.Added {
		out = append(out, linkedEnvWorktree(p, w))
	}
	return out, nil
}

// envPatchFor describes a planned patch of the Worktree w for the API.
func envPatchFor(w envWorktree, plan envpatch.Plan) api.EnvFilePatch {
	res := api.EnvFilePatch{
		Worktree: api.Worktree{Name: w.name, Path: w.dir, Main: w.main, Domain: w.domain},
		Path:     plan.Path,
		Create:   plan.Create,
		CopyFrom: plan.CopyFrom,
		Backup:   plan.Backup,
		Changes:  []api.EnvChange{},
	}
	for _, c := range plan.Changes {
		res.Changes = append(res.Changes, api.EnvChange{Key: c.Key, Old: c.Old, New: c.New, Added: c.Added})
	}
	return res
}
