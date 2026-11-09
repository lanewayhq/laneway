package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/envpatch"
	"github.com/lanewayhq/laneway/internal/gitrepo"
	"github.com/lanewayhq/laneway/internal/naming"
	"github.com/lanewayhq/laneway/internal/registry"
)

// link registers the repository containing req.Dir as a Project.
func (d *Daemon) link(ctx context.Context, req api.LinkRequest) (api.LinkResponse, error) {
	if !filepath.IsAbs(req.Dir) {
		return api.LinkResponse{}, &api.Error{Code: api.CodeBadRequest, Message: "dir must be an absolute path"}
	}
	checkout, err := gitrepo.Resolve(ctx, req.Dir)
	if errors.Is(err, gitrepo.ErrNotGitRepo) {
		return api.LinkResponse{}, &api.Error{
			Code: api.CodeNotGitRepo,
			Message: fmt.Sprintf("%s is not inside a git repository; "+
				"run `lane link` from a git repository (or `git init` one first)", req.Dir),
		}
	}
	if err != nil {
		return api.LinkResponse{}, err
	}
	cfg, err := d.plugins.ResolveDir(checkout.MainDir)
	if err != nil {
		return api.LinkResponse{}, &api.Error{Code: api.CodeInvalidConfig, Message: err.Error()}
	}
	name := req.Name
	if name == "" {
		name = cfg.Name
	}
	if name == "" {
		name = filepath.Base(checkout.MainDir)
	}
	res := api.LinkResponse{}
	project := registry.Project{Name: name, Path: checkout.MainDir}
	if existing, ok := d.registry.ProjectAt(checkout.MainDir); ok {
		project = existing
	} else if taken, ok := d.nameTaken(name); ok {
		return api.LinkResponse{}, nameTakenError(name, taken)
	} else if domainOf(project) == d.cfg.DashboardDomain {
		return api.LinkResponse{}, &api.Error{
			Code: api.CodeNameTaken,
			Message: fmt.Sprintf("the Project name %q would be served at https://%s, where the Dashboard is; "+
				"choose a different one with --name <name>", name, d.cfg.DashboardDomain),
		}
	}
	// Every Worktree's Env Patch is worked out (and, unless previewing,
	// applied) before anything is Linked or started, so a Project whose
	// templates are broken isn't Linked, and Processes never start against
	// an unpatched .env.
	if res.EnvPatch, err = d.linkEnvPatch(ctx, project, !req.Preview); err != nil {
		return api.LinkResponse{}, err
	}
	if req.Preview {
		_, res.AlreadyLinked = d.registry.ProjectAt(checkout.MainDir)
		res.Project = api.Project{Name: project.Name, Path: project.Path}
		res.Worktree = api.Worktree{Name: checkout.Name, Path: checkout.Dir, Main: checkout.Main()}
		return res, nil
	}
	err = d.registry.AddProject(project)
	switch {
	case errors.Is(err, registry.ErrAlreadyLinked):
		project, _ = d.registry.ProjectAt(checkout.MainDir)
		res.AlreadyLinked = true
		// Linking from a Worktree the Watcher hasn't reported yet finds it.
		d.syncWorktrees(ctx, project)
	case errors.Is(err, registry.ErrNameTaken):
		taken, _ := d.nameTaken(name)
		return api.LinkResponse{}, nameTakenError(name, taken)
	case err != nil:
		return api.LinkResponse{}, err
	default:
		d.serveProject(project)
		d.syncWorktrees(ctx, project)
	}
	res.Project = d.apiProject(project)
	res.Worktree = api.Worktree{Name: checkout.Name, Path: checkout.Dir, Main: checkout.Main()}
	for _, w := range res.Project.Worktrees {
		if w.Path == checkout.Dir {
			res.Worktree = w
		}
	}
	return res, nil
}

// nameTakenError refuses the Project name name, which clashes with the
// Linked Project taken.
func nameTakenError(name, taken string) error {
	if taken == "" || taken == name {
		return &api.Error{
			Code: api.CodeNameTaken,
			Message: fmt.Sprintf("the Project name %q is already taken by another repository; "+
				"choose a different one with --name <name>", name),
		}
	}
	return &api.Error{
		Code: api.CodeNameTaken,
		Message: fmt.Sprintf("the Project name %q would share its Domain and database with the Project %q; "+
			"choose a different one with --name <name>", name, taken),
	}
}

// nameTaken returns the name of the Linked Project whose Domain or database
// name would be name's (naming.Clash), if there is one.
func (d *Daemon) nameTaken(name string) (string, bool) {
	for _, p := range d.registry.Projects() {
		if naming.Clash(p.Name, name) {
			return p.Name, true
		}
	}
	return "", false
}

// linkEnvPatch works out the Env Patch of every Worktree of p and, with
// apply, applies it. It returns the env files that change.
func (d *Daemon) linkEnvPatch(ctx context.Context, p registry.Project, apply bool) ([]api.EnvFilePatch, error) {
	worktrees, err := d.projectWorktreeRefs(ctx, p)
	if err != nil {
		return nil, err
	}
	type filePlan struct {
		w    worktreeRef
		plan envpatch.Plan
	}
	var plans []filePlan
	for _, w := range worktrees {
		plan, err := d.planEnvPatch(p, w)
		if err != nil {
			return nil, &api.Error{Code: api.CodeInvalidConfig, Message: fmt.Sprintf("the Env Patch for %s: %v", w.dir, err)}
		}
		// With Test Databases on, the env file the tests read too.
		testPlan, _, err := d.planTestEnvPatch(p, w)
		if err != nil {
			return nil, &api.Error{Code: api.CodeInvalidConfig, Message: fmt.Sprintf("the Env Patch for %s: %v", w.dir, err)}
		}
		plans = append(plans, filePlan{w, plan}, filePlan{w, testPlan})
	}
	var res []api.EnvFilePatch
	for _, fp := range plans {
		if fp.plan.Empty() {
			continue
		}
		if apply {
			if err := d.applyPlan(p, fp.plan); err != nil {
				return nil, fmt.Errorf("applying the Env Patch to %s: %w", fp.plan.Path, err)
			}
			if filepath.Base(fp.plan.Path) == envFileName {
				// Its running Processes read .env when they started.
				d.restartServedForEnv(fp.w.domain)
			}
		}
		res = append(res, envPatchFor(fp.w, fp.plan))
	}
	return res, nil
}

// apiProject describes p and its Worktrees, the main one first.
func (d *Daemon) apiProject(p registry.Project) api.Project {
	res := api.Project{Name: p.Name, Path: p.Path, Worktrees: []api.Worktree{
		{Path: p.Path, Main: true, Domain: domainOf(p), Aliases: d.aliasDomains(p, ""),
			PrimaryDomain: d.primaryDomain(p, mainWorktreeRef(p))},
	}}
	for _, w := range d.registry.Worktrees(p.Path) {
		ref := linkedWorktreeRef(p, w)
		res.Worktrees = append(res.Worktrees, api.Worktree{
			Name: w.Name, Path: w.Path, Domain: ref.domain, Aliases: d.aliasDomains(p, w.Name),
			PrimaryDomain: d.primaryDomain(p, ref),
		})
	}
	return res
}
