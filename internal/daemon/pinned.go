package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/plugin"
)

// A Plugin added by hand (from a directory or a GitHub repository) is pinned
// to the hash its module had when added (spec #48, ADR-0007): Laneway runs
// the copy it installed, never its source, so a later change to the source
// changes nothing until `lane plugin update`, which shows the new hash.

// updateHandAdded installs what the source of the Plugin name, added by
// hand, has now, or with req.Preview only reads and checks it, so the
// developer can confirm the new version and hash first. Catalog Plugins are
// updated from the Catalog instead (see updates.go).
func (d *Daemon) updateHandAdded(ctx context.Context, name string, req api.UpdateHandAddedRequest) (api.UpdateHandAddedResponse, error) {
	was, ok := d.plugins.Plugin(name)
	if !ok {
		return api.UpdateHandAddedResponse{}, &api.Error{Code: api.CodeUnknownPlugin, Message: fmt.Sprintf("no Plugin %s is installed", name)}
	}
	if was.Source == plugin.CatalogSource {
		return api.UpdateHandAddedResponse{}, &api.Error{Code: api.CodeBadRequest,
			Message: fmt.Sprintf("Plugin %s came from the Catalog, so it is updated from the Catalog (POST /v1/plugins/update)", name)}
	}
	src, source, done, err := d.pluginSource(ctx, was.Source)
	if err != nil {
		return api.UpdateHandAddedResponse{}, err
	}
	defer done()
	p, err := d.plugins.Inspect(src)
	if err != nil {
		return api.UpdateHandAddedResponse{}, &api.Error{Code: api.CodeInvalidPlugin, Message: fmt.Sprintf("%s: %v", was.Source, err)}
	}
	if p.Name != name {
		return api.UpdateHandAddedResponse{}, &api.Error{Code: api.CodeInvalidPlugin,
			Message: fmt.Sprintf("%s now has Plugin %s, not %s; add it with `lane plugin add %s` if you want it", was.Source, p.Name, name, was.Source)}
	}
	p.Source, p.Global = source, was.Global
	res := api.UpdateHandAddedResponse{Plugin: apiPlugin(p), Was: apiPlugin(was)}
	if p.Version == was.Version && p.SHA256 == was.SHA256 {
		res.UpToDate = true
		return res, nil
	}
	if req.Preview {
		res.Digest = p.Digest
		return res, nil
	}
	if req.Digest == "" {
		return api.UpdateHandAddedResponse{}, &api.Error{Code: api.CodeBadRequest, Message: "digest must be the digest of the preview the developer confirmed"}
	}
	p, _, err = d.plugins.AddPreviewed(src, source, was.Global, req.Digest) // a machine-wide Plugin stays machine-wide
	if errors.Is(err, plugin.ErrChanged) {
		return api.UpdateHandAddedResponse{}, &api.Error{Code: api.CodeSourceChanged,
			Message: fmt.Sprintf("%s changed since it was shown, so Plugin %s wasn't updated; run `lane plugin update %s` again to see what it has now", source, name, name)}
	}
	if err != nil {
		return api.UpdateHandAddedResponse{}, &api.Error{Code: api.CodeInvalidPlugin, Message: err.Error()}
	}
	d.log.Info("Plugin updated", "plugin", p.Name, "from", was.Version, "to", p.Version, "source", p.Source, "sha256", p.SHA256)
	res.Plugin = apiPlugin(p)
	return res, nil
}
