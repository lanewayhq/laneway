package daemon

import (
	"context"
	"fmt"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/plugin"
)

// A Plugin added by hand (from a directory or a GitHub repository) is pinned
// to the hash its module had when added (spec #48, ADR-0007): Laneway runs
// the copy it installed, never its source, so a later change to the source
// changes nothing until `lane plugin update`, which shows the new hash.

// updatePlugin installs what the source of the installed Plugin name has
// now, or with req.Preview only reads and checks it, so the developer can
// confirm the new version and hash first.
func (d *Daemon) updatePlugin(ctx context.Context, name string, req api.UpdatePluginRequest) (api.UpdatePluginResponse, error) {
	was, ok := d.plugins.Plugin(name)
	if !ok {
		return api.UpdatePluginResponse{}, &api.Error{Code: api.CodeUnknownPlugin, Message: fmt.Sprintf("no Plugin %s is installed", name)}
	}
	if was.Source == plugin.CatalogSource {
		return api.UpdatePluginResponse{}, &api.Error{Code: api.CodeBadRequest,
			Message: fmt.Sprintf("Plugin %s came from the Catalog, and Laneway can't update Catalog Plugins yet", name)}
	}
	src, source, done, err := d.pluginSource(ctx, was.Source)
	if err != nil {
		return api.UpdatePluginResponse{}, err
	}
	defer done()
	p, err := d.plugins.Inspect(src)
	if err != nil {
		return api.UpdatePluginResponse{}, &api.Error{Code: api.CodeInvalidPlugin, Message: fmt.Sprintf("%s: %v", was.Source, err)}
	}
	if p.Name != name {
		return api.UpdatePluginResponse{}, &api.Error{Code: api.CodeInvalidPlugin,
			Message: fmt.Sprintf("%s now has Plugin %s, not %s; add it with `lane plugin add %s` if you want it", was.Source, p.Name, name, was.Source)}
	}
	p.Source = source
	res := api.UpdatePluginResponse{Plugin: apiPlugin(p), Was: apiPlugin(was)}
	if p.Version == was.Version && p.SHA256 == was.SHA256 {
		res.UpToDate = true
		return res, nil
	}
	if req.Preview {
		return res, nil
	}
	p, _, err = d.plugins.AddFrom(src, source)
	if err != nil {
		return api.UpdatePluginResponse{}, &api.Error{Code: api.CodeInvalidPlugin, Message: err.Error()}
	}
	d.log.Info("Plugin updated", "plugin", p.Name, "from", was.Version, "to", p.Version, "source", p.Source, "sha256", p.SHA256)
	res.Plugin = apiPlugin(p)
	return res, nil
}
