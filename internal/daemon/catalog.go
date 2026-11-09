package daemon

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/catalog"
	"github.com/lanewayhq/laneway/internal/engine"
	"github.com/lanewayhq/laneway/internal/plugin"
	"github.com/lanewayhq/laneway/internal/recipe"
)

// fetchCatalogPlugins installs the Catalog Plugins the Project in dir needs
// and doesn't have yet, each at its latest release for this Laneway's Plugin
// API version (spec #48, ADR-0007): the Plugins its config names (see
// namedplugins.go); a Recipe that detects it, if no Recipe Laneway has does;
// each Integration that detects it; and the Engines its config names. Only
// Catalog Plugins are fetched this way: config naming any other fails,
// saying to add it by hand.
//
// A Catalog that can't be fetched falls back to the copy kept last time;
// with neither, nothing is fetched and the Project resolves with what is
// installed, with a warning (unchecked says why). A Catalog that is refused (its signature
// isn't Laneway's) is reported in warnings. It fails, naming them, if a
// Plugin the Project needs can't be fetched, such as when offline.
func (d *Daemon) fetchCatalogPlugins(ctx context.Context, dir string) (res catalogFetched, err error) {
	d.catalogMu.Lock()
	defer d.catalogMu.Unlock()
	cat, loadErr := d.loadCatalog(ctx)
	if loadErr != nil {
		d.log.Warn("couldn't fetch the Catalog", "url", d.catalog.URL(), "err", loadErr, "kept", cat != nil)
		if errors.Is(loadErr, catalog.ErrRefused) {
			res.warnings = append(res.warnings, loadErr.Error())
		}
	}
	named := d.missingNamedPlugins(dir)
	if cat == nil {
		if len(named) > 0 {
			return res, uncheckedNamedError(named, loadErr)
		}
		// Without a Catalog, Laneway can't tell which of its Plugins
		// detect the Project, so it can't name them: it says so.
		res.unchecked = loadErr
		res.warnings = append(res.warnings, fmt.Sprintf("Laneway has never been able to fetch the Catalog (%v), "+
			"so it couldn't fetch any Plugin from it this Project needs, such as its Recipe; "+
			"once Laneway can reach the Catalog, run `lane link` again", loadErr))
		return res, nil
	}
	f := catalogFetch{d: d, ctx: ctx, latest: latestAllowed(cat, d.pins(dir)), failed: map[string]error{}}
	for _, p := range d.plugins.List() {
		delete(f.latest, p.Name)
	}

	// The Plugins its config names first, since they may add its Recipe.
	var notInCatalog []namedPlugin
	for _, p := range named {
		switch _, ok := f.latest[p.name]; {
		case ok:
			f.fetch(p.name)
		case slices.ContainsFunc(cat.Plugins, func(c catalog.Plugin) bool { return c.Name == p.name }):
			f.failed[p.name] = fmt.Errorf("%s: no release in the Catalog is allowed by its version pins and speaks this Laneway's Plugin API", p.name)
		default:
			notInCatalog = append(notInCatalog, p)
		}
	}
	if len(notInCatalog) > 0 {
		return res, notInCatalogError(notInCatalog)
	}

	// Then the Recipe and Integrations: their config may name Engines.
	if !anyDetects(d.plugins.Recipes(), dir) {
	recipes:
		for _, name := range f.names() {
			for _, r := range f.latest[name].Recipes {
				if _, ok := r.Detect.Match(dir); ok {
					f.fetch(name)
					break recipes
				}
			}
		}
	}
	taken := map[string]bool{}
	for _, in := range d.plugins.Integrations() {
		taken[in.Name()] = true
	}
	for _, name := range f.names() {
		for _, in := range f.latest[name].Integrations {
			if _, ok := in.Detect.Match(dir); ok && !taken[in.Name] {
				f.fetch(name)
				break
			}
		}
	}
	for _, name := range f.enginePlugins(dir) {
		f.fetch(name)
	}
	res.fetched = f.fetched
	if len(f.failed) > 0 {
		return res, f.error(d.catalog.URL())
	}
	return res, nil
}

// catalogFetched is what fetching a Project's Catalog Plugins did.
type catalogFetched struct {
	fetched  []plugin.Plugin
	warnings []string
	// unchecked is why the Catalog couldn't be checked at all, if it
	// couldn't: the Project may need Plugins that weren't fetched.
	unchecked error
}

// explain adds to err, an error resolving the Project's config, that the
// Catalog couldn't be checked, if it couldn't, since a Plugin missing from it
// may be why.
func (r catalogFetched) explain(err error) string {
	if r.unchecked == nil {
		return err.Error()
	}
	return fmt.Sprintf("%v\n(Laneway couldn't check the Catalog for Plugins this Project needs: %v)", err, r.unchecked)
}

// anyDetects reports whether one of recipes detects the Project in dir.
func anyDetects(recipes []recipe.Recipe, dir string) bool {
	for _, r := range recipes {
		if _, ok := r.Detection().Match(dir); ok {
			return true
		}
	}
	return false
}

// catalogFetch is one Link's fetching from the Catalog.
type catalogFetch struct {
	d   *Daemon
	ctx context.Context
	// latest are the releases of the Catalog Plugins not installed, by
	// name: each Plugin's latest for this Laneway.
	latest  map[string]catalog.Release
	fetched []plugin.Plugin
	failed  map[string]error // by Plugin name
}

// names are the Catalog Plugins not installed or tried yet, sorted.
func (f *catalogFetch) names() []string {
	return slices.Sorted(maps.Keys(f.latest))
}

// fetch downloads and installs the Catalog Plugin name.
func (f *catalogFetch) fetch(name string) {
	r := f.latest[name]
	delete(f.latest, name)
	p, err := f.d.installRelease(f.ctx, name, r)
	if err != nil {
		f.d.log.Warn("couldn't fetch a Plugin from the Catalog", "plugin", name, "version", r.Version, "err", err)
		f.failed[name] = fmt.Errorf("%s %s: %w", name, r.Version, err)
		return
	}
	f.d.log.Info("Plugin fetched from the Catalog", "plugin", p.Name, "version", p.Version, "sha256", p.SHA256)
	f.fetched = append(f.fetched, p)
}

// installRelease downloads the Catalog release r of the Plugin name and
// installs it, replacing the installed version, if any.
func (d *Daemon) installRelease(ctx context.Context, name string, r catalog.Release) (plugin.Plugin, error) {
	return d.withRelease(ctx, name, r, func(dir string) (plugin.Plugin, error) {
		p, _, err := d.plugins.AddFrom(dir, plugin.CatalogSource)
		return p, err
	})
}

// withRelease downloads the Catalog release r of the Plugin name into a
// temporary directory, checks it is the release the Catalog lists, and calls
// use with it. The directory is removed afterwards.
func (d *Daemon) withRelease(ctx context.Context, name string, r catalog.Release, use func(dir string) (plugin.Plugin, error)) (plugin.Plugin, error) {
	plugins := d.cfg.Paths.Plugins()
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		return plugin.Plugin{}, err
	}
	tmp, err := os.MkdirTemp(plugins, ".fetch-"+name+"-")
	if err != nil {
		return plugin.Plugin{}, err
	}
	defer os.RemoveAll(tmp)
	if err := d.catalog.Fetch(ctx, r, tmp); err != nil {
		return plugin.Plugin{}, err
	}
	m, err := plugin.LoadManifest(tmp)
	if err != nil {
		return plugin.Plugin{}, err
	}
	if m.Name != name || m.Version != r.Version {
		return plugin.Plugin{}, fmt.Errorf("the release is Plugin %s %s, but the Catalog lists it as %s %s", m.Name, m.Version, name, r.Version)
	}
	return use(tmp)
}

// enginePlugins are the Catalog Plugins (not installed) adding the Engines
// that the Project in dir names in its resolved config, as a Service or as
// its database's engine. The config is resolved as if every Catalog Engine
// were installed; if it doesn't resolve for another reason, none are
// needed, and Linking reports that reason.
func (f *catalogFetch) enginePlugins(dir string) []string {
	engines := f.d.plugins.Engines()
	var assumed engine.Set
	by := map[string]string{} // Engine name → Plugin
	for _, name := range f.names() {
		for _, e := range f.latest[name].Engines {
			if _, ok := engines.Lookup(e); ok {
				continue
			}
			if _, ok := assumed.Lookup(e); ok {
				continue
			}
			by[e] = name
			assumed = append(assumed, placeholderEngine(e))
		}
	}
	if len(by) == 0 {
		return nil
	}
	res, err := f.d.plugins.ResolveDirAssuming(dir, assumed)
	if err != nil {
		return nil
	}
	var names []string
	for _, s := range res.Services {
		if p, ok := by[s.Engine]; ok && !slices.Contains(names, p) {
			names = append(names, p)
		}
	}
	return names
}

// placeholderEngine stands for a Catalog Engine not yet fetched: its name
// alone, and anything config may ask of an Engine (users, databases), so
// config naming it resolves until the real one is installed.
func placeholderEngine(name string) *engine.Manifest {
	return &engine.Manifest{Name: name, DefaultVersion: "latest", User: "placeholder", Database: &engine.Databases{}}
}

// error names the Plugins that couldn't be fetched, and why.
func (f *catalogFetch) error(url string) error {
	names := slices.Sorted(maps.Keys(f.failed))
	var b strings.Builder
	fmt.Fprintf(&b, "this Project needs Plugins from the Catalog that Laneway couldn't fetch: %s", strings.Join(names, ", "))
	for _, name := range names {
		fmt.Fprintf(&b, "\n  %v", f.failed[name])
	}
	fmt.Fprintf(&b, "\nonce Laneway can reach the Catalog (%s), run `lane link` again", url)
	return &api.Error{Code: api.CodePluginsUnavailable, Message: b.String()}
}
