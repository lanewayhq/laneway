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
		Project:  api.Project{Name: p.Name, Path: p.Path},
		Worktree: api.Worktree{Name: checkout.Name, Path: checkout.Dir, Main: checkout.Main()},
		Domain:   d.checkoutDomain(p, checkout),
	}
	info.Worktree.Domain = info.Domain
	if info.Domain != "" {
		info.URL = "https://" + info.Domain + "/"
		info.Worktree.Setup = d.setupOf(checkout.Dir)
	}
	return info, nil
}

// checkoutDomain is the Domain the checkout is served at, or "" if the
// Worktree Watcher hasn't recorded it yet.
func (d *Daemon) checkoutDomain(p registry.Project, c gitrepo.Checkout) string {
	if c.Main() {
		return domainOf(p)
	}
	for _, w := range d.registry.Worktrees(p.Path) {
		if w.Name == c.Name {
			return worktreeDomain(p, w)
		}
	}
	return ""
}
