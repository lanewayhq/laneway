package daemon

import (
	"context"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/gitrepo"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
)

// worktreeInfo describes the Worktree containing dir, which must be inside a
// Linked Project, and where it is served.
func (d *Daemon) worktreeInfo(ctx context.Context, dir string) (api.WorktreeInfo, error) {
	p, checkout, err := d.worktreeAt(ctx, dir)
	if err != nil {
		return api.WorktreeInfo{}, err
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
