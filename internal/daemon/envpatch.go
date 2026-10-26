package daemon

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/envpatch"
	"github.com/mrsafalpiya/portby.dev/internal/gitrepo"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
)

// envFileName is the env file the Env Patch goes into.
const envFileName = ".env"

// testEnvFileName is the env file a Laravel app's tests read on top of .env.
// With Test Databases on, it gets the Env Patch too, pointed at the
// Worktree's Test Database.
const testEnvFileName = ".env.testing"

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
// changing the URL, #13), call applyEnvPatch for the Worktree again. db is
// the Project's database config; {db} is only set if it has one.
func envVars(p registry.Project, w envWorktree, db recipe.Database) map[string]string {
	vars := map[string]string{
		"project":  p.Name,
		"worktree": w.name,
		"domain":   w.domain,
		"url":      "https://" + w.domain,
	}
	if name, ok := worktreeDatabase(p, w, db); ok {
		vars["db"] = name
	}
	return vars
}

// planEnvPatch works out how the Worktree's .env changes to carry its Env
// Patch, from the templates in the Worktree's own config. A Project without
// templates gets no Env Patch: its env files are never touched.
func (d *Daemon) planEnvPatch(p registry.Project, w envWorktree) (envpatch.Plan, error) {
	cfg, err := recipe.ResolveDir(w.dir)
	templates := cfg.EnvFor(w.main)
	if err != nil || len(templates) == 0 {
		return envpatch.Plan{}, err
	}
	values, err := envpatch.RenderAll(templates, d.envPatchVars(p, w, cfg))
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

// envPatchVars are the template variables' values for the Worktree w whose
// config is cfg: envVars, its Services' and its databases'.
func (d *Daemon) envPatchVars(p registry.Project, w envWorktree, cfg recipe.Resolved) map[string]string {
	vars := envVars(p, w, cfg.Database)
	services := d.serviceVars(cfg.Services)
	maps.Copy(vars, services)
	maps.Copy(vars, databaseVars(cfg, services))
	if name, ok := worktreeTestDatabase(p, w, cfg); ok {
		vars["test_db"] = name
	}
	return vars
}

// planTestEnvPatch works out how the Worktree's .env.testing changes to
// point its tests at its Test Database: it gets the same Env Patch as .env,
// rendered with {db} holding the Test Database. A linked Worktree without a
// .env.testing gets a copy of the main Worktree's. The plan is empty when
// Test Databases are off or there are no templates. Portby never makes a
// .env.testing from nothing: when neither the Worktree nor the main Worktree
// has one, test config is left alone and missing is the Test Database the
// tests won't use, for a warning.
func (d *Daemon) planTestEnvPatch(p registry.Project, w envWorktree) (plan envpatch.Plan, missing string, err error) {
	cfg, err := recipe.ResolveDir(w.dir)
	templates := cfg.EnvFor(w.main)
	if err != nil || len(templates) == 0 {
		return envpatch.Plan{}, "", err
	}
	testDB, ok := worktreeTestDatabase(p, w, cfg)
	if !ok {
		return envpatch.Plan{}, "", nil
	}
	vars := d.envPatchVars(p, w, cfg)
	vars["db"] = testDB
	values, err := envpatch.RenderAll(templates, vars)
	if err != nil {
		return envpatch.Plan{}, "", err
	}
	f := envpatch.File{Path: filepath.Join(w.dir, testEnvFileName)}
	if !w.main {
		f.Seeds = []string{filepath.Join(p.Path, testEnvFileName)}
	}
	plan, err = envpatch.Prepare(f, values)
	if err != nil {
		return envpatch.Plan{}, "", err
	}
	if plan.Create && plan.CopyFrom == "" {
		return envpatch.Plan{}, testDB, nil
	}
	return plan, "", nil
}

// testEnvWarning is the setup warning for a Worktree whose tests won't use
// its Test Database testDB because there is no .env.testing to patch.
func testEnvWarning(p registry.Project, testDB string) string {
	return fmt.Sprintf("the main Worktree has no %s, so Portby left this Worktree's test config alone and its tests "+
		"don't use its Test Database %s; create %s and it is patched the next time the Worktree starts or `portby link` runs",
		testEnvFileName, testDB, filepath.Join(p.Path, testEnvFileName))
}

// applyEnvPatch brings the Worktree's .env up to date with its Env Patch,
// logging failures. It runs whenever the daemon starts serving a Worktree
// (at daemon start, on Link and on discovery) and whenever a Worktree's
// Processes start, so template changes apply then. Re-applying a patch that
// is in place changes nothing. It reports whether it changed .env.
func (d *Daemon) applyEnvPatch(p registry.Project, w envWorktree) (changed bool) {
	plan, err := d.planEnvPatch(p, w)
	if err == nil {
		err = d.applyPlan(p, plan)
	}
	if err != nil {
		d.log.Error("applying the Env Patch", "project", p.Name, "worktree", w.dir, "err", err)
	} else if !plan.Empty() {
		d.log.Info("applied the Env Patch", "project", p.Name, "path", plan.Path)
		changed = true
	}
	testPlan, _, err := d.planTestEnvPatch(p, w)
	if err == nil {
		err = d.applyPlan(p, testPlan)
	}
	if err != nil {
		d.log.Error("applying the Env Patch to .env.testing", "project", p.Name, "worktree", w.dir, "err", err)
	} else if !testPlan.Empty() {
		d.log.Info("applied the Env Patch", "project", p.Name, "path", testPlan.Path)
	}
	return changed
}

// applyPlan applies an Env Patch plan of one of p's Worktrees. The backup
// of the original that the first patch makes holds the developer's secrets,
// and Laravel's .gitignore doesn't cover it, so its name goes into the
// repository's .git/info/exclude (not .gitignore, which is committed).
func (d *Daemon) applyPlan(p registry.Project, plan envpatch.Plan) error {
	if err := plan.Apply(); err != nil {
		return err
	}
	if plan.Backup != "" {
		err := gitrepo.Exclude(p.Path, "*"+envpatch.BackupSuffix, "Portby's backups of the env files it patches (they hold secrets)")
		if err != nil {
			d.log.Warn("excluding Env Patch backups from git", "project", p.Name, "err", err)
		}
	}
	return nil
}

// applyServedEnvPatch re-applies the Env Patch of the Worktree served as s,
// before its Processes start, so template changes apply at every start. It
// reports whether it changed .env.
func (d *Daemon) applyServedEnvPatch(s *served) bool {
	for _, p := range d.registry.Projects() {
		if p.Name != s.project {
			continue
		}
		if s.dir == p.Path {
			return d.applyEnvPatch(p, mainEnvWorktree(p))
		}
		for _, w := range d.registry.Worktrees(p.Path) {
			if w.Path == s.dir {
				return d.applyEnvPatch(p, linkedEnvWorktree(p, w))
			}
		}
	}
	return false
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
