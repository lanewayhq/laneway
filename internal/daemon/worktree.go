package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/gitrepo"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
)

// worktreeAt describes the Worktree containing dir, which must be inside a
// Linked Project.
func (d *Daemon) worktreeAt(ctx context.Context, dir string) (api.WorktreeInfo, error) {
	if !filepath.IsAbs(dir) {
		return api.WorktreeInfo{}, &api.Error{Code: api.CodeBadRequest, Message: "dir must be an absolute path"}
	}
	notLinked := &api.Error{
		Code:    api.CodeNotLinked,
		Message: fmt.Sprintf("%s is not inside a Linked Project; run `portby link` in the repository first", dir),
	}
	checkout, err := gitrepo.Resolve(ctx, dir)
	if errors.Is(err, gitrepo.ErrNotGitRepo) {
		return api.WorktreeInfo{}, notLinked
	}
	if err != nil {
		return api.WorktreeInfo{}, err
	}
	p, ok := d.registry.ProjectAt(checkout.MainDir)
	if !ok {
		return api.WorktreeInfo{}, notLinked
	}
	info := api.WorktreeInfo{
		Project:  apiProject(p.Name, p.Path),
		Worktree: api.Worktree{Name: checkout.Name, Path: checkout.Dir, Main: checkout.Main()},
		Domain:   worktreeDomain(p, checkout),
	}
	if info.Domain != "" {
		info.URL = "https://" + info.Domain + "/"
	}
	return info, nil
}

// worktreeDomain is the Domain a Worktree is served at, or "" if it has none.
// Only the main Worktree is served so far; Worktree Domains are ticket #12.
func worktreeDomain(p registry.Project, c gitrepo.Checkout) string {
	if !c.Main() {
		return ""
	}
	return domainOf(p)
}
