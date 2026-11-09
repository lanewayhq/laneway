package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/catalog"
	"github.com/lanewayhq/laneway/internal/gitrepo"
)

// fetchPlugins is POST /v1/plugins/fetch: it installs from the Catalog,
// without Linking anything, what commands that read a Project's files
// without the daemon need (`lane init`, `lane doctor`, `lane generate`):
// the Catalog Plugins the Project in req.Dir needs, as Linking it would, or
// the Catalog Plugin req.Name. The daemon fetches them, rather than the
// CLI, since it owns the installed Plugins.
func (d *Daemon) fetchPlugins(ctx context.Context, req api.FetchPluginsRequest) (api.FetchPluginsResponse, error) {
	checkout, err := gitrepo.Resolve(ctx, req.Dir)
	if errors.Is(err, gitrepo.ErrNotGitRepo) {
		return api.FetchPluginsResponse{}, &api.Error{Code: api.CodeNotGitRepo, Message: fmt.Sprintf("%s is not inside a git repository", req.Dir)}
	}
	if err != nil {
		return api.FetchPluginsResponse{}, err
	}
	if req.Name != "" {
		return d.fetchNamedPlugin(ctx, req.Name, checkout.MainDir)
	}
	fetched, err := d.fetchCatalogPlugins(ctx, checkout.MainDir, againLink)
	if err != nil {
		return api.FetchPluginsResponse{}, err
	}
	res := api.FetchPluginsResponse{Warnings: fetched.warnings}
	if len(fetched.fetched) > 0 {
		// Linked Projects may have been waiting for them, such as when
		// the daemon couldn't fetch them when it started.
		d.background(d.pluginsFetched)
	}
	for _, p := range fetched.fetched {
		res.Fetched = append(res.Fetched, apiPlugin(p))
	}
	return res, nil
}

// fetchNamedPlugin installs the Catalog Plugin name, unless it is
// installed, at its latest release the version pins of the Project in dir
// and every Linked Project allow.
func (d *Daemon) fetchNamedPlugin(ctx context.Context, name, dir string) (api.FetchPluginsResponse, error) {
	d.catalogMu.Lock()
	defer d.catalogMu.Unlock()
	if d.plugins.Has(name) {
		return api.FetchPluginsResponse{}, nil
	}
	var res api.FetchPluginsResponse
	cat, loadErr := d.loadCatalog(ctx)
	if loadErr != nil {
		d.log.Warn("couldn't fetch the Catalog", "url", d.catalog.URL(), "err", loadErr, "kept", cat != nil)
		if errors.Is(loadErr, catalog.ErrRefused) {
			res.Warnings = append(res.Warnings, loadErr.Error())
		}
	}
	if cat == nil {
		return res, &api.Error{Code: api.CodePluginsUnavailable,
			Message: fmt.Sprintf("no Plugin named %s is installed, and Laneway couldn't fetch the Plugin %s from the Catalog: %v", name, name, loadErr)}
	}
	r, ok := latestAllowed(cat, d.pins(dir))[name]
	switch {
	case !ok && slices.ContainsFunc(cat.Plugins, func(p catalog.Plugin) bool { return p.Name == name }):
		return res, &api.Error{Code: api.CodePluginsUnavailable,
			Message: fmt.Sprintf("no release of the Plugin %s in the Catalog is allowed by its version pins and speaks this Laneway's Plugin API", name)}
	case !ok:
		return res, &api.Error{Code: api.CodeUnknownPlugin,
			Message: fmt.Sprintf("no Plugin named %s is installed, and the Catalog doesn't list it; add it with `lane plugin add <dir|github.com/<owner>/<repo>>`", name)}
	}
	p, err := d.installRelease(ctx, name, r)
	if err != nil {
		d.log.Warn("couldn't fetch a Plugin from the Catalog", "plugin", name, "version", r.Version, "err", err)
		return res, &api.Error{Code: api.CodePluginsUnavailable,
			Message: fmt.Sprintf("Laneway couldn't fetch the Plugin %s from the Catalog (%s): %s %s: %v", name, d.catalog.URL(), name, r.Version, err)}
	}
	d.log.Info("Plugin fetched from the Catalog", "plugin", p.Name, "version", p.Version, "sha256", p.SHA256)
	res.Fetched = append(res.Fetched, apiPlugin(p))
	return res, nil
}
