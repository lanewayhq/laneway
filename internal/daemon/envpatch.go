package daemon

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"strings"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/envpatch"
	"github.com/lanewayhq/laneway/internal/gitrepo"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/registry"
)

// envFileName is the env file the Env Patch goes into.
const envFileName = ".env"

// worktreeRef identifies one Worktree of a Project: its names, directory and
// Domain. The Env Patch, setup and Orphans work from it, and every served
// Domain carries one.
type worktreeRef struct {
	name string // git's name; "" for the main Worktree
	// label is the Worktree's part of its Domain, unique within the Project
	// ("" for the main Worktree). Its database names come from it, so two
	// Worktrees whose names normalise alike get databases of their own.
	label  string
	dir    string
	domain string
	main   bool
}

func mainWorktreeRef(p registry.Project) worktreeRef {
	return worktreeRef{dir: p.Path, domain: domainOf(p), main: true}
}

func linkedWorktreeRef(p registry.Project, w registry.Worktree) worktreeRef {
	return worktreeRef{name: w.Name, label: w.Label, dir: w.Path, domain: worktreeDomain(p, w)}
}

// worktreesOf are the Project p's Worktrees, its main one first.
func (d *Daemon) worktreesOf(p registry.Project) []worktreeRef {
	worktrees := []worktreeRef{mainWorktreeRef(p)}
	for _, w := range d.registry.Worktrees(p.Path) {
		worktrees = append(worktrees, linkedWorktreeRef(p, w))
	}
	return worktrees
}

// envVars are the template variables' values for the Worktree w. {domain}
// and {url} are its Primary Domain's, so its Aliases are inputs too: when
// one changes them, primaryDomainMoved applies the Env Patch again. Its
// database names come from its label, so they stay. db is the Project's
// database config; {db} is only set if it has one.
func (d *Daemon) envVars(p registry.Project, w worktreeRef, db recipe.Database) map[string]string {
	primary := d.primaryDomain(p, w)
	vars := map[string]string{
		"project":  p.Name,
		"worktree": w.name,
		"domain":   primary,
		// No trailing slash, as Laravel's APP_URL has it, so templates
		// can append a path: "{url}/shop".
		"url": strings.TrimSuffix(domainURL(primary), "/"),
	}
	if name, ok := d.worktreeDatabase(p, w, db); ok {
		vars["db"] = name
	}
	return vars
}

// planEnvPatch works out how the Worktree's .env changes to carry its Env
// Patch, from the templates in the Worktree's own config. A Project without
// templates gets no Env Patch: its env files are never touched.
func (d *Daemon) planEnvPatch(p registry.Project, w worktreeRef) (envpatch.Plan, error) {
	cfg, err := d.plugins.ResolveDir(w.dir)
	templates := cfg.EnvFor(w.main)
	if err != nil || len(templates) == 0 {
		return envpatch.Plan{}, err
	}
	values, err := envpatch.RenderAll(templates, d.envPatchVars(p, w, cfg))
	if err != nil {
		return envpatch.Plan{}, err
	}
	return envpatch.Prepare(envFileOf(p, w), values)
}

// envFileOf is the Worktree's .env, as the Env Patch and env effects patch
// it.
func envFileOf(p registry.Project, w worktreeRef) envpatch.File {
	f := envpatch.File{Path: filepath.Join(w.dir, envFileName)}
	if !w.main {
		// A new Worktree shares the main Worktree's settings and app key.
		f.Seeds = append(f.Seeds, filepath.Join(p.Path, envFileName))
	}
	f.Seeds = append(f.Seeds, filepath.Join(w.dir, envFileName+".example"))
	return f
}

// envPatchVars are the template variables' values for the Worktree w whose
// config is cfg: envVars, its Services', its databases' and its Buckets'.
func (d *Daemon) envPatchVars(p registry.Project, w worktreeRef, cfg recipe.Resolved) map[string]string {
	vars := d.envVars(p, w, cfg.Database)
	services := d.serviceVars(cfg.Services)
	maps.Copy(vars, services)
	maps.Copy(vars, databaseVars(cfg, services))
	maps.Copy(vars, d.bucketVars(p, w, cfg))
	if name, ok := d.worktreeTestDatabase(p, w, cfg); ok {
		vars["test_db"] = name
	}
	return vars
}

// planTestEnvPatch works out how the env file the Worktree's tests read
// (recipe.Resolved.TestEnv) changes to point them at its Test Database: it
// gets the same Env Patch as .env, rendered with {db} holding the Test
// Database. A linked Worktree without one gets a copy of the main
// Worktree's. The plan is empty when Test Databases are off or there are no
// templates. Unless the tests read the file over .env (TestEnv.Layered),
// Laneway never makes it from nothing: when neither the Worktree nor the
// main Worktree has one, test config is left alone and warning says why
// the tests won't use the Test Database.
func (d *Daemon) planTestEnvPatch(p registry.Project, w worktreeRef) (plan envpatch.Plan, warning string, err error) {
	cfg, err := d.plugins.ResolveDir(w.dir)
	templates := cfg.EnvFor(w.main)
	if err != nil || len(templates) == 0 {
		return envpatch.Plan{}, "", err
	}
	testDB, ok := d.worktreeTestDatabase(p, w, cfg)
	if !ok {
		return envpatch.Plan{}, "", nil
	}
	vars := d.envPatchVars(p, w, cfg)
	vars["db"] = testDB
	values, err := envpatch.RenderAll(templates, vars)
	if err != nil {
		return envpatch.Plan{}, "", err
	}
	name := cfg.TestEnv.File
	f := envpatch.File{Path: filepath.Join(w.dir, name)}
	if !w.main {
		f.Seeds = []string{filepath.Join(p.Path, name)}
	}
	plan, err = envpatch.Prepare(f, values)
	if err != nil {
		return envpatch.Plan{}, "", err
	}
	if plan.Create && plan.CopyFrom == "" && !cfg.TestEnv.Layered {
		return envpatch.Plan{}, testEnvWarning(p, name, testDB), nil
	}
	return plan, "", nil
}

// testEnvWarning is the setup warning for a Worktree whose tests won't use
// its Test Database testDB because there is no test env file name to patch.
func testEnvWarning(p registry.Project, name, testDB string) string {
	return fmt.Sprintf("the main Worktree has no %s, so Laneway left this Worktree's test config alone and its tests "+
		"don't use its Test Database %s; create %s and it is patched the next time the Worktree starts or `lane link` runs",
		name, testDB, filepath.Join(p.Path, name))
}

// applyEnvPatch brings the Worktree's .env up to date with its Env Patch,
// logging failures. It runs whenever the daemon starts serving a Worktree
// (at daemon start, on Link and on discovery) and whenever a Worktree's
// Processes start, so template changes apply then. Re-applying a patch that
// is in place changes nothing. It reports whether it changed .env.
func (d *Daemon) applyEnvPatch(p registry.Project, w worktreeRef) (changed bool) {
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
		d.log.Error("applying the Env Patch to the test env file", "project", p.Name, "worktree", w.dir, "err", err)
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
		err := gitrepo.Exclude(p.Path, "*"+envpatch.BackupSuffix, "Laneway's backups of the env files it patches (they hold secrets)")
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
	p, ok := d.servedProject(s)
	if !ok {
		return false
	}
	w, ok := d.worktreeRefOf(p, s.dir)
	return ok && d.applyEnvPatch(p, w)
}

// projectWorktreeRefs are the Project's Worktrees as they will be once
// syncWorktrees has run: the main one, then the linked ones git knows,
// including those the Registry hasn't recorded yet. It changes nothing.
func (d *Daemon) projectWorktreeRefs(ctx context.Context, p registry.Project) ([]worktreeRef, error) {
	d.syncMu.Lock()
	defer d.syncMu.Unlock()
	found, _, err := d.foundWorktrees(ctx, p)
	if err != nil {
		return nil, err
	}
	changes := d.registry.PlanWorktrees(p.Path, found, d.reservedLabels(p))
	removed := map[string]bool{}
	for _, w := range changes.Removed {
		removed[w.Path] = true
	}
	out := []worktreeRef{mainWorktreeRef(p)}
	for _, w := range d.registry.Worktrees(p.Path) {
		if !removed[w.Path] {
			out = append(out, linkedWorktreeRef(p, w))
		}
	}
	for _, w := range changes.Added {
		out = append(out, linkedWorktreeRef(p, w))
	}
	return out, nil
}

// envPatchFor describes a planned patch of the Worktree w for the API.
func envPatchFor(w worktreeRef, plan envpatch.Plan) api.EnvFilePatch {
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
