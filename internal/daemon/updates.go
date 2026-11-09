package daemon

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/catalog"
	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/fileblock"
	"github.com/lanewayhq/laneway/internal/plugin"
)

// Plugin updates (spec #48, ADR-0007): a Catalog Plugin stays at the version
// first installed until `lane plugin update`, since a new version can change
// the Env Patch of every Worktree, including ones a Harness is working in.

func (d *Daemon) handleUpdatePlugins(w http.ResponseWriter, r *http.Request) {
	var req api.UpdatePluginsRequest
	if !readJSON(w, r, &req) {
		return
	}
	res, err := d.updatePlugins(r.Context(), req)
	if err != nil {
		d.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// updatePlugins finds the installed Catalog Plugins' updates (req.Name's
// only, if set) or, unless previewing, installs the confirmed ones.
func (d *Daemon) updatePlugins(ctx context.Context, req api.UpdatePluginsRequest) (api.UpdatePluginsResponse, error) {
	d.catalogMu.Lock()
	defer d.catalogMu.Unlock()
	installed, err := d.updatable(req.Name)
	if err != nil {
		return api.UpdatePluginsResponse{}, err
	}
	cat, err := d.loadCatalog(ctx)
	if err != nil {
		return api.UpdatePluginsResponse{}, &api.Error{Code: api.CodePluginsUnavailable,
			Message: fmt.Sprintf("Laneway couldn't check for Plugin updates: %v", err)}
	}
	res := api.UpdatePluginsResponse{Updates: []api.PluginUpdate{}}
	pins := d.pins()
	if req.Preview {
		for _, p := range installed {
			r, ok, warning := updateFor(cat, p, pins[p.Name])
			if warning != "" {
				res.Warnings = append(res.Warnings, warning)
			}
			if !ok {
				continue
			}
			u, err := d.previewUpdate(ctx, p, r)
			if err != nil {
				return api.UpdatePluginsResponse{}, err
			}
			res.Updates = append(res.Updates, u)
		}
		return res, nil
	}
	for _, u := range req.Confirmed {
		i := slices.IndexFunc(installed, func(p plugin.Plugin) bool { return p.Name == u.Name })
		if i < 0 {
			return res, &api.Error{Code: api.CodeBadRequest, Message: fmt.Sprintf("Plugin %s isn't one to update", u.Name)}
		}
		r, ok := releaseOf(cat, u)
		if !ok || !allowedByAll(pins[u.Name], r.Version) {
			return res, &api.Error{Code: api.CodeCatalogChanged, Message: fmt.Sprintf(
				"the Catalog no longer lists Plugin %s %s with SHA-256 %s; run `lane plugin update` again", u.Name, u.To, u.SHA256)}
		}
		var generated []api.OutOfDateFile
		if len(installed[i].Generators) > 0 {
			generated = d.generatedFiles(u.Name)
		}
		p, err := d.installRelease(ctx, u.Name, r)
		if err != nil {
			return res, &api.Error{Code: api.CodePluginsUnavailable, Message: fmt.Sprintf(
				"couldn't update Plugin %s to %s: %v", u.Name, u.To, err)}
		}
		d.log.Info("Plugin updated", "plugin", p.Name, "from", installed[i].Version, "to", p.Version, "sha256", p.SHA256)
		res.Updates = append(res.Updates, u)
		res.OutOfDate = append(res.OutOfDate, d.outOfDate(u.Name, generated)...)
	}
	return res, nil
}

// generatedFiles are the files in Linked Projects' Worktrees that the Plugin
// name's Generators wrote: those `lane generate <name>` would leave as they
// are. A file they would change, or create, may be the developer's own, or
// was never generated, so it isn't one.
func (d *Daemon) generatedFiles(name string) []api.OutOfDateFile {
	var files []api.OutOfDateFile
	for _, dir := range d.worktreeDirs() {
		merged, err := d.plugins.Generated(name, dir)
		if err != nil {
			d.log.Warn("couldn't check a Plugin's generated files", "plugin", name, "worktree", dir, "err", err)
			continue
		}
		for _, f := range merged {
			if f.Old != nil && !f.Changed() {
				files = append(files, api.OutOfDateFile{Plugin: name, Worktree: dir, Path: f.Path})
			}
		}
	}
	return files
}

// outOfDate are those of generated (see generatedFiles) that the Plugin
// name's Generators, since it was updated, would change. Laneway never
// rewrites them itself: they are committed files, and only a developer's
// `lane generate` may change them.
func (d *Daemon) outOfDate(name string, generated []api.OutOfDateFile) []api.OutOfDateFile {
	var stale []api.OutOfDateFile
	checked := map[string][]fileblock.File{} // by Worktree
	for _, g := range generated {
		merged, ok := checked[g.Worktree]
		if !ok {
			var err error
			if merged, err = d.plugins.Generated(name, g.Worktree); err != nil {
				d.log.Warn("couldn't check a Plugin's generated files", "plugin", name, "worktree", g.Worktree, "err", err)
			}
			checked[g.Worktree] = merged
		}
		if slices.ContainsFunc(merged, func(f fileblock.File) bool { return f.Path == g.Path && f.Changed() }) {
			stale = append(stale, g)
		}
	}
	return stale
}

// worktreeDirs are the directories of every Linked Project's Worktrees.
func (d *Daemon) worktreeDirs() []string {
	var dirs []string
	for _, p := range d.registry.Projects() {
		dirs = append(dirs, p.Path)
		for _, w := range d.registry.Worktrees(p.Path) {
			if !slices.Contains(dirs, w.Path) {
				dirs = append(dirs, w.Path)
			}
		}
	}
	return dirs
}

// updatable are the installed Plugins `lane plugin update` updates: those
// fetched from the Catalog, or only name if set, which must be one.
func (d *Daemon) updatable(name string) ([]plugin.Plugin, error) {
	var plugins []plugin.Plugin
	for _, p := range d.plugins.List() {
		if name != "" && p.Name != name {
			continue
		}
		if p.Source != plugin.CatalogSource {
			if name != "" {
				return nil, &api.Error{Code: api.CodeNotFromCatalog, Message: fmt.Sprintf(
					"Plugin %s was added by hand from %s, so it isn't updated from the Catalog, but from there, with `lane plugin update %[1]s`",
					p.Name, p.Source)}
			}
			continue
		}
		plugins = append(plugins, p)
	}
	if name != "" && len(plugins) == 0 {
		return nil, &api.Error{Code: api.CodeUnknownPlugin, Message: fmt.Sprintf("no Plugin named %s is installed", name)}
	}
	return plugins, nil
}

// updateFor is the release the installed Catalog Plugin p would be updated
// to, if there is one: the Catalog's latest for this Laneway's Plugin API
// that every pin allows, if it is newer than p, or p's version isn't
// allowed. warning says why there is none, if pins allow no release.
func updateFor(cat *catalog.Catalog, p plugin.Plugin, pins []config.Pin) (r catalog.Release, ok bool, warning string) {
	releases := cat.Releases(p.Name, plugin.APIVersion)
	r, ok = newestAllowed(releases, pins)
	if !ok {
		if len(releases) > 0 && len(pins) > 0 {
			warning = fmt.Sprintf("no release of Plugin %s is allowed by every pin: %s; it stays at %s", p.Name, describePins(pins), p.Version)
		}
		return r, false, warning
	}
	if r.Version == p.Version || catalog.CompareVersions(r.Version, p.Version) < 0 && allowedByAll(pins, p.Version) {
		return r, false, ""
	}
	return r, true, ""
}

// newestAllowed is the newest of releases (newest first) that every pin
// allows.
func newestAllowed(releases []catalog.Release, pins []config.Pin) (catalog.Release, bool) {
	for _, r := range releases {
		if allowedByAll(pins, r.Version) {
			return r, true
		}
	}
	return catalog.Release{}, false
}

func allowedByAll(pins []config.Pin, version string) bool {
	for _, p := range pins {
		if !p.Allows(version) {
			return false
		}
	}
	return true
}

func describePins(pins []config.Pin) string {
	described := make([]string, len(pins))
	for i, p := range pins {
		described[i] = fmt.Sprintf("%q in %s", p.Range, p.Source)
	}
	return strings.Join(described, ", ")
}

// pins are the version pins (see config.PluginPins) of every Linked
// Project and of the Projects in dirs, such as one being Linked, by Plugin
// name, in a stable order. A Project whose config can't be read pins
// nothing.
func (d *Daemon) pins(dirs ...string) map[string][]config.Pin {
	for _, p := range d.registry.Projects() {
		if !slices.Contains(dirs, p.Path) {
			dirs = append(dirs, p.Path)
		}
	}
	slices.Sort(dirs)
	all := map[string][]config.Pin{}
	for _, dir := range dirs {
		pins, err := config.PluginPins(dir)
		if err != nil {
			d.log.Warn("couldn't read a Project's Plugin version pins", "dir", dir, "err", err)
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(pins)) {
			all[name] = append(all[name], pins[name])
		}
	}
	return all
}

// latestAllowed is, for each Plugin the Catalog lists, its newest release
// for this Laneway's Plugin API that pins allow, by Plugin name. A Plugin
// with no such release is left out.
func latestAllowed(cat *catalog.Catalog, pins map[string][]config.Pin) map[string]catalog.Release {
	latest := map[string]catalog.Release{}
	for _, p := range cat.Plugins {
		if r, ok := newestAllowed(cat.Releases(p.Name, plugin.APIVersion), pins[p.Name]); ok {
			latest[p.Name] = r
		}
	}
	return latest
}

// previewUpdate downloads the release r of the installed Plugin p, without
// installing it, to find what it may do that p may not.
func (d *Daemon) previewUpdate(ctx context.Context, p plugin.Plugin, r catalog.Release) (api.PluginUpdate, error) {
	u := api.PluginUpdate{Name: p.Name, From: p.Version, To: r.Version, SHA256: r.SHA256}
	next, err := d.withRelease(ctx, p.Name, r, d.plugins.Inspect)
	if err != nil {
		return u, &api.Error{Code: api.CodePluginsUnavailable, Message: fmt.Sprintf(
			"couldn't download Plugin %s %s to check what it may do: %v", p.Name, r.Version, err)}
	}
	for _, kind := range next.Capabilities.Effects {
		if !slices.Contains(p.Capabilities.Effects, kind) {
			u.NewEffects = append(u.NewEffects, api.PluginEffect{Kind: kind, Description: plugin.EffectKinds[kind]})
		}
	}
	for _, host := range next.Capabilities.HTTP {
		if !slices.Contains(p.Capabilities.HTTP, host) {
			u.NewHTTPHosts = append(u.NewHTTPHosts, host)
		}
	}
	return u, nil
}

// releaseOf is the release the Catalog lists for the confirmed update u, if
// it still lists it.
func releaseOf(cat *catalog.Catalog, u api.PluginUpdate) (catalog.Release, bool) {
	for _, r := range cat.Releases(u.Name, plugin.APIVersion) {
		if r.Version == u.To && r.SHA256 == u.SHA256 {
			return r, true
		}
	}
	return catalog.Release{}, false
}

// catalogMaxAge is how long Laneway trusts the Catalog it loaded last to
// list the latest releases, for showing available updates, before it loads
// it again.
const catalogMaxAge = time.Hour

// updateCheckTimeout is how long showing available updates waits for the
// Catalog, falling back to the copy kept last time.
const updateCheckTimeout = 5 * time.Second

// checkedCatalog is the Catalog last loaded, and when.
type checkedCatalog struct {
	cat *catalog.Catalog
	at  time.Time
}

// loadCatalog loads the Catalog (see catalog.Source.Load), remembering it
// for availableUpdates. The caller holds catalogMu.
func (d *Daemon) loadCatalog(ctx context.Context) (*catalog.Catalog, error) {
	cat, err := d.catalog.Load(ctx)
	d.checked = checkedCatalog{cat: cat, at: d.cfg.Clock.Now()}
	if cat != nil {
		d.lastCatalog.Store(cat)
	}
	return cat, err
}

// availableUpdates are the updates `lane plugin update` would offer, by
// Plugin name: Catalog Plugins' from the Catalog loaded within
// catalogMaxAge, or else loaded now, and hand-added Plugins' from their
// sources (see handAddedUpdates). They don't say what new capabilities a
// release asks for: that needs downloading it.
func (d *Daemon) availableUpdates(ctx context.Context) map[string]api.PluginUpdate {
	pins := d.pins()
	updates := d.handAddedUpdates(ctx, pins)
	installed, _ := d.updatable("")
	if len(installed) == 0 {
		return updates // nothing to check the Catalog for
	}
	d.catalogMu.Lock()
	defer d.catalogMu.Unlock()
	cat := d.checked.cat
	if cat == nil || d.cfg.Clock.Now().Sub(d.checked.at) >= catalogMaxAge {
		ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
		defer cancel()
		var err error
		if cat, err = d.loadCatalog(ctx); err != nil {
			d.log.Warn("couldn't check the Catalog for Plugin updates", "url", d.catalog.URL(), "err", err, "kept", cat != nil)
		}
	}
	if cat == nil {
		return updates
	}
	for _, p := range installed {
		if r, ok, _ := updateFor(cat, p, pins[p.Name]); ok {
			updates[p.Name] = api.PluginUpdate{Name: p.Name, From: p.Version, To: r.Version, SHA256: r.SHA256}
		}
	}
	return updates
}
