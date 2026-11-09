package daemon

import (
	"context"
	"slices"
	"strings"

	"github.com/lanewayhq/laneway/internal/catalog"
	"github.com/lanewayhq/laneway/internal/prepare"
	"github.com/lanewayhq/laneway/internal/registry"
)

// When the daemon starts, it fetches the Catalog Plugins its Linked
// Projects need, as Linking them does (issue #78): an upgrade may have left
// the installed Plugins behind, such as one to a Laneway whose binary no
// longer carries the first-party ones. It does so in the background, so
// starting doesn't wait on the network, and then brings the Projects up to
// date with what it fetched. What it couldn't fetch is a warning on the
// Project and on each of its Worktrees until a later fetch for the Project
// succeeds, such as `lane doctor`'s or the next start's.

// againStart is what a warning about Catalog Plugins Laneway couldn't fetch
// when it started says to do once Laneway can reach the Catalog.
const againStart = "run `lane doctor` in the Project to fetch them; Laneway also tries again when it restarts"

// fetchLinkedProjectsPlugins fetches, in the background, the Catalog
// Plugins each Linked Project needs and doesn't have, loading the Catalog
// once for them all.
func (d *Daemon) fetchLinkedProjectsPlugins() {
	projects := d.registry.Projects()
	if len(projects) == 0 {
		return
	}
	d.background(func() {
		ctx := d.stopping
		d.catalogMu.Lock()
		cat, loadErr := d.loadCatalog(ctx)
		d.catalogMu.Unlock()
		fetched := false
		for _, p := range projects {
			if ctx.Err() != nil {
				return
			}
			fetched = d.fetchOnStart(ctx, cat, loadErr, p) || fetched
		}
		if fetched {
			d.pluginsFetched()
		}
	})
}

// fetchOnStart fetches the Catalog Plugins the Linked Project p needs from
// cat (see fetchCatalogPluginsFrom), records what it couldn't fetch, and
// reports whether it fetched any. It takes catalogMu for the Project alone,
// so `lane link` and `lane status` don't wait for every Project's
// downloads, and records the outcome before releasing it, so it can't undo
// what a fetch right after, such as `lane doctor`'s, records.
func (d *Daemon) fetchOnStart(ctx context.Context, cat *catalog.Catalog, loadErr error, p registry.Project) bool {
	d.catalogMu.Lock()
	defer d.catalogMu.Unlock()
	res, err := d.fetchCatalogPluginsFrom(ctx, cat, loadErr, p.Path, againStart)
	if ctx.Err() != nil {
		return false
	}
	switch {
	case err != nil:
		d.setUnfetched(p.Path, err.Error())
	case res.unchecked != nil:
		// Without a Catalog, Laneway can't tell what the Project
		// needs: it only warns if something seems missing.
		if _, err := d.plugins.ResolveDir(p.Path); err != nil {
			d.setUnfetched(p.Path, strings.Join(res.warnings, "\n"))
		}
	}
	return len(res.fetched) > 0
}

// setUnfetched records why Laneway couldn't fetch the Catalog Plugins the
// Project at path needs; "" forgets it.
func (d *Daemon) setUnfetched(path, why string) {
	d.unfetchedMu.Lock()
	defer d.unfetchedMu.Unlock()
	if why == "" {
		delete(d.unfetched, path)
		return
	}
	if d.unfetched == nil {
		d.unfetched = map[string]string{}
	}
	d.unfetched[path] = why
}

// unfetchedWarnings are the warnings about Catalog Plugins Laneway couldn't
// fetch for the Project with a Worktree at dir, its main one's included.
func (d *Daemon) unfetchedWarnings(dir string) []string {
	d.unfetchedMu.Lock()
	defer d.unfetchedMu.Unlock()
	for path, why := range d.unfetched {
		if path == dir || slices.ContainsFunc(d.registry.Worktrees(path), func(w registry.Worktree) bool { return w.Path == dir }) {
			return []string{why}
		}
	}
	return nil
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
