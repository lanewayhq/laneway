package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/envpatch"
	"github.com/mrsafalpiya/portby.dev/internal/gitrepo"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
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
				"run `portby link` from a git repository (or `git init` one first)", req.Dir),
		}
	}
	if err != nil {
		return api.LinkResponse{}, err
	}
	cfg, err := recipe.ResolveDir(checkout.MainDir)
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
	} else if d.nameTaken(name) {
		return api.LinkResponse{}, nameTakenError(name)
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
		return api.LinkResponse{}, nameTakenError(name)
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

func nameTakenError(name string) error {
	return &api.Error{
		Code: api.CodeNameTaken,
		Message: fmt.Sprintf("the Project name %q is already taken by another repository; "+
			"choose a different one with --name <name>", name),
	}
}

// nameTaken reports whether a Project is Linked as name.
func (d *Daemon) nameTaken(name string) bool {
	for _, p := range d.registry.Projects() {
		if p.Name == name {
			return true
		}
	}
	return false
}

// linkEnvPatch works out the Env Patch of every Worktree of p and, with
// apply, applies it. It returns the env files that change.
func (d *Daemon) linkEnvPatch(ctx context.Context, p registry.Project, apply bool) ([]api.EnvFilePatch, error) {
	worktrees, err := d.projectEnvWorktrees(ctx, p)
	if err != nil {
		return nil, err
	}
	var plans []envpatch.Plan
	for _, w := range worktrees {
		plan, err := d.planEnvPatch(p, w)
		if err != nil {
			return nil, &api.Error{Code: api.CodeInvalidConfig, Message: fmt.Sprintf("the Env Patch for %s: %v", w.dir, err)}
		}
		plans = append(plans, plan)
	}
	var res []api.EnvFilePatch
	for i, plan := range plans {
		if plan.Empty() {
			continue
		}
		if apply {
			if err := plan.Apply(); err != nil {
				return nil, fmt.Errorf("applying the Env Patch to %s: %w", plan.Path, err)
			}
		}
		res = append(res, envPatchFor(worktrees[i], plan))
	}
	return res, nil
}

// apiProject describes p and its Worktrees, the main one first.
func (d *Daemon) apiProject(p registry.Project) api.Project {
	res := api.Project{Name: p.Name, Path: p.Path, Worktrees: []api.Worktree{
		{Path: p.Path, Main: true, Domain: domainOf(p), Aliases: d.aliasDomains(p, "")},
	}}
	for _, w := range d.registry.Worktrees(p.Path) {
		res.Worktrees = append(res.Worktrees, api.Worktree{
			Name: w.Name, Path: w.Path, Domain: worktreeDomain(p, w), Aliases: d.aliasDomains(p, w.Name),
		})
	}
	return res
}
