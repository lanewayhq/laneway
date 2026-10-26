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
	info.Worktree.Aliases = d.aliasDomains(p, checkout.Name)
	if info.Domain != "" {
		info.URL = domainURL(info.Domain)
		info.Worktree.Setup = d.setupOf(checkout.Dir)
		info.Database = d.databaseInfo(p, checkout.Dir)
	}
	return info, nil
}

// domainURL is the URL of the page served at domain, as the API and
// `portby info` give it: "https://shop.localhost/".
func domainURL(domain string) string {
	return "https://" + domain + "/"
}

// checkoutDomain is the Domain the checkout is served at, or "" if the
// Worktree Watcher hasn't recorded it yet.
func (d *Daemon) checkoutDomain(p registry.Project, c gitrepo.Checkout) string {
	if c.Main() {
		return domainOf(p)
	}
	return d.namedWorktreeDomain(p, c.Name)
}

// namedWorktreeDomain is the Domain of the Project's Worktree called name
// ("" for the main Worktree), or "" if there is no such Worktree.
func (d *Daemon) namedWorktreeDomain(p registry.Project, name string) string {
	if name == "" {
		return domainOf(p)
	}
	for _, w := range d.registry.Worktrees(p.Path) {
		if w.Name == name {
			return worktreeDomain(p, w)
		}
	}
	return ""
}
