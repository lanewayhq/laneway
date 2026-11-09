package daemon

import (
	"github.com/lanewayhq/laneway/internal/prepare"
)

// When the daemon starts, it fetches the Catalog Plugins its Linked
// Projects need, as Linking them does (issue #78): an upgrade may have left
// the installed Plugins behind, such as one to a Laneway whose binary no
// longer carries the first-party ones. It does so in the background, so
// starting doesn't wait on the network, and then brings the Projects up to
// date with what it fetched.

// fetchLinkedProjectsPlugins fetches, in the background, the Catalog
// Plugins each Linked Project needs and doesn't have, loading the Catalog
// once for them all.
func (d *Daemon) fetchLinkedProjectsPlugins() {
	d.background(func() {
		ctx := d.stopping
		d.catalogMu.Lock()
		cat, loadErr := d.loadCatalog(ctx)
		d.catalogMu.Unlock()
		fetched := false
		for _, p := range d.registry.Projects() {
			// catalogMu is taken for each Project, not for them all, so
			// `lane link` and `lane status` don't wait for every download.
			d.catalogMu.Lock()
			res, _ := d.fetchCatalogPluginsFrom(ctx, cat, loadErr, p.Path, againLink)
			d.catalogMu.Unlock()
			if ctx.Err() != nil {
				return
			}
			fetched = fetched || len(res.fetched) > 0
		}
		if fetched {
			d.pluginsFetched()
		}
	})
}

// pluginsFetched brings the Linked Projects up to date with Catalog Plugins
// just fetched. Until then their config may not have resolved, so the
// Services whose Engines were missing weren't served, the Env Patch wasn't
// applied, always Processes didn't start and setups failed at step
// "config": those setups are retried, and the rest is done once each
// Worktree is Ready. A Worktree's lazy Processes need nothing: they read
// config anew when they start.
func (d *Daemon) pluginsFetched() {
	d.serveRecordedServices()
	for _, p := range d.registry.Projects() {
		worktrees := []worktreeRef{mainWorktreeRef(p)}
		for _, w := range d.registry.Worktrees(p.Path) {
			worktrees = append(worktrees, linkedWorktreeRef(p, w))
		}
		for _, w := range worktrees {
			if st := d.preparer.Status(w.dir); st.State == prepare.Failed && st.Step == prepare.StepConfig {
				d.prepare(p, w, true)
			}
			d.serveServicesOf(w.dir)
			if s := d.servedWorktree(w.domain); s != nil {
				d.catchUp(s)
			}
		}
	}
}

// catchUp applies the Env Patch of the Worktree served as s, restarting its
// running Processes if that changed .env, and starts its always Processes,
// in the background once it is Ready.
func (d *Daemon) catchUp(s *served) {
	d.background(func() {
		if err := d.waitForReady(s); err != nil {
			return
		}
		s.lifecycle.Lock()
		if s.ctx.Err() == nil && d.applyServedEnvPatch(s) {
			d.restartRunning(s, "restarting Processes for the changed .env")
		}
		s.lifecycle.Unlock()
		d.startAlways(s)
	})
}
