package daemon

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/lanewayhq/laneway/internal/catalog"
	"github.com/lanewayhq/laneway/internal/envpatch"
	"github.com/lanewayhq/laneway/internal/plugin"
	"github.com/lanewayhq/laneway/internal/prepare"
	"github.com/lanewayhq/laneway/internal/recipe"
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

// remedyStart is what a warning about Catalog Plugins Laneway couldn't fetch
// when it started says to do once Laneway can reach the Catalog.
const remedyStart = "run `lane doctor` in the Project to fetch them; Laneway also tries again when it restarts"

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
		for _, p := range projects {
			if ctx.Err() != nil {
				return
			}
			d.fetchOnStart(ctx, cat, loadErr, p)
			d.retryConfigFailures(p)
		}
	})
}

// retryConfigFailures retries the setups of the Linked Project p's
// Worktrees that failed at the config step and whose config now resolves.
// Such a failure carries over from when the daemon last ran: the Plugins
// its config needed may have been installed since, such as by a fetch the
// daemon stopped right after, before bringing the Worktrees up to date with
// it (see pluginsFetched), so this start fetches nothing for them.
func (d *Daemon) retryConfigFailures(p registry.Project) {
	for _, w := range d.worktreesOf(p) {
		if st := d.preparer.Status(w.dir); st.State != prepare.Failed || st.Step != prepare.StepConfig {
			continue
		}
		if _, err := d.plugins.ResolveDir(w.dir); err == nil {
			d.prepare(p, w, true)
		}
	}
}

// fetchOnStart fetches the Catalog Plugins the Linked Project p needs from
// cat (see fetchCatalogPluginsFrom) and records what it couldn't fetch. It
// takes catalogMu for the Project alone, so `lane link` and `lane status`
// don't wait for every Project's downloads, and records the outcome before
// releasing it, so it can't undo what a fetch right after, such as `lane
// doctor`'s, records.
func (d *Daemon) fetchOnStart(ctx context.Context, cat *catalog.Catalog, loadErr error, p registry.Project) {
	d.catalogMu.Lock()
	defer d.catalogMu.Unlock()
	res, err := d.fetchCatalogPluginsFrom(ctx, cat, loadErr, p.Path, remedyStart)
	if ctx.Err() != nil {
		return
	}
	switch {
	case err != nil:
		d.setUnfetched(p.Path, err.Error())
	case res.unchecked != nil:
		// Without a Catalog, Laneway can't tell what the Project needs: it
		// only warns if its config doesn't resolve, and then names the
		// error, since a missing Plugin may not be why.
		if _, err := d.plugins.ResolveDir(p.Path); err != nil {
			d.setUnfetched(p.Path, fmt.Sprintf("its config doesn't resolve, maybe for want of a Plugin from the Catalog, "+
				"which Laneway has never been able to fetch (%v): %v\nonce Laneway can reach the Catalog, %s",
				res.unchecked, err, remedyStart))
		}
	}
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
	// A copy, so the registry isn't asked with unfetchedMu held.
	d.unfetchedMu.Lock()
	unfetched := maps.Clone(d.unfetched)
	d.unfetchedMu.Unlock()
	for path, why := range unfetched {
		if path == dir || slices.ContainsFunc(d.registry.Worktrees(path), func(w registry.Worktree) bool { return w.Path == dir }) {
			return []string{why}
		}
	}
	return nil
}

// pluginsFetched brings the Worktrees of Linked Projects that use the
// Catalog Plugins just fetched up to date with them. Until then their
// config didn't resolve, or resolved without them: the Services whose
// Engines were missing weren't served, and the Worktree may have been set up
// and served without them (see catchUp). Worktrees that don't use them are
// left as they are, so an always Process the developer stopped stays
// stopped. A Worktree's lazy Processes need nothing: they read config anew
// when they start.
func (d *Daemon) pluginsFetched(fetched []plugin.Plugin) {
	gen := d.pluginsGen.Load()
	d.serveRecordedServices()
	for _, p := range d.registry.Projects() {
		for _, w := range d.worktreesOf(p) {
			cfg, err := d.plugins.ResolveDir(w.dir)
			if err != nil || !slices.ContainsFunc(fetched, func(pl plugin.Plugin) bool { return usesPlugin(cfg, pl) }) {
				continue
			}
			d.serveServicesOf(w.dir)
			d.catchUp(p, w, gen)
		}
	}
}

// usesPlugin reports whether a Worktree whose resolved config is cfg uses
// the Plugin p: as p.UsedBy says, or in an env template, through a Service
// variable of one of its Engines, such as {redis_port}.
func usesPlugin(cfg recipe.Resolved, p plugin.Plugin) bool {
	if len(p.UsedBy(cfg)) > 0 {
		return true
	}
	templates := slices.Concat(slices.Collect(maps.Values(cfg.Env)), slices.Collect(maps.Values(cfg.LinkedEnv)))
	for _, e := range p.Engines {
		for _, v := range e.Variables() {
			if slices.ContainsFunc(templates, func(t string) bool { return envpatch.Uses(t, v) }) {
				return true
			}
		}
	}
	return false
}

// catchUp brings the Worktree w of the Project p, whose config uses Catalog
// Plugins fetched (pluginsGen reached gen), up to date with them, in the
// background. Its setup is retried if it failed, and run again if it read
// config before they were fetched; then, if it was served before they were,
// its Env Patch is applied, restarting its running Processes if that
// changed .env, and its always Processes start, once it is Ready. Each is
// done once for gen, however many fetches reach it.
func (d *Daemon) catchUp(p registry.Project, w worktreeRef, gen uint64) {
	again := d.setupCatchesUp(w.dir, gen)
	s := d.servedWorktree(w.domain)
	if s != nil && !s.catchesUp(gen) {
		s = nil
	}
	d.background(func() {
		switch {
		case d.preparer.Status(w.dir).State == prepare.Failed:
			d.prepare(p, w, true)
		case again:
			// A setup still running finishes first.
			if _, err := d.preparer.Wait(d.stopping, w.dir); err != nil {
				return
			}
			d.preparer.Again(w.dir, d.setupPlan(p, w))
		}
		if s == nil {
			return
		}
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

// setupReadsConfig records that the setup of the Worktree at dir is
// reading its config, with the Plugins installed as of pluginsGen.
func (d *Daemon) setupReadsConfig(dir string) {
	d.setupGensMu.Lock()
	defer d.setupGensMu.Unlock()
	if d.setupGens == nil {
		d.setupGens = map[string]uint64{}
	}
	d.setupGens[dir] = d.pluginsGen.Load()
}

// setupCatchesUp reports whether the setup of the Worktree at dir read its
// config, while this daemon ran, before pluginsGen reached gen, and if so
// records that it is caught up to gen.
func (d *Daemon) setupCatchesUp(dir string, gen uint64) bool {
	d.setupGensMu.Lock()
	defer d.setupGensMu.Unlock()
	if read, ok := d.setupGens[dir]; !ok || read >= gen {
		return false
	}
	d.setupGens[dir] = gen
	return true
}

// catchesUp reports whether s was served before pluginsGen reached gen,
// and if so records that it is caught up to gen.
func (s *served) catchesUp(gen uint64) bool {
	served := s.pluginsGen.Load()
	return served < gen && s.pluginsGen.CompareAndSwap(served, gen)
}
