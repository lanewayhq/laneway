package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/plugin"
)

// A Plugin added by hand (from a directory or a GitHub repository) is pinned
// to the hash its module had when added (spec #48, ADR-0007): Laneway runs
// the copy it installed, never its source, so a later change to the source
// changes nothing until `lane plugin update`, which shows the new hash.

// updateHandAdded installs what the source of the Plugin name, added by
// hand, has now, or with req.Preview only reads and checks it, so the
// developer can confirm the new version and hash first. Like a Catalog
// update, it respects the version pins in Linked Projects' config, and
// reports the generated files it leaves out of date. Catalog Plugins are
// updated from the Catalog instead (see updates.go).
func (d *Daemon) updateHandAdded(ctx context.Context, name string, req api.UpdateHandAddedRequest) (api.UpdateHandAddedResponse, error) {
	was, ok := d.plugins.Plugin(name)
	if !ok {
		return api.UpdateHandAddedResponse{}, &api.Error{Code: api.CodeUnknownPlugin, Message: fmt.Sprintf("no Plugin named %s is installed", name)}
	}
	if was.Source == plugin.CatalogSource {
		return api.UpdateHandAddedResponse{}, &api.Error{Code: api.CodeBadRequest,
			Message: fmt.Sprintf("Plugin %s came from the Catalog, so it is updated from the Catalog with `lane plugin update`", name)}
	}
	p, src, source, done, err := d.readHandAdded(ctx, was)
	if err != nil {
		return api.UpdateHandAddedResponse{}, err
	}
	defer done()
	res := api.UpdateHandAddedResponse{Plugin: apiPlugin(p), Was: apiPlugin(was)}
	if p.Version == was.Version && p.SHA256 == was.SHA256 {
		res.UpToDate = true
		return res, nil
	}
	if pins := d.pins()[name]; !allowedByAll(pins, p.Version) {
		res.Warning = fmt.Sprintf("Plugin %s %s, from %s, isn't allowed by every pin: %s; it stays at %s",
			name, p.Version, was.Source, describePins(pins), was.Version)
		return res, nil
	}
	if req.Preview {
		res.Digest = p.Digest
		return res, nil
	}
	if req.Digest == "" {
		return api.UpdateHandAddedResponse{}, &api.Error{Code: api.CodeBadRequest, Message: "digest must be the digest of the preview the developer confirmed"}
	}
	var generated []api.OutOfDateFile
	if len(was.Generators) > 0 {
		generated = d.generatedFiles(name)
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
	d.updateServices(ctx, p)
	res.Plugin = apiPlugin(p)
	res.OutOfDate = d.outOfDate(name, generated)
	return res, nil
}

// readHandAdded reads what the source of the installed Plugin was, added by
// hand, has now, from the directory src (which done removes): p, as it
// would be installed, recording source.
func (d *Daemon) readHandAdded(ctx context.Context, was plugin.Plugin) (p plugin.Plugin, src, source string, done func(), err error) {
	src, source, done, err = d.pluginSource(ctx, was.Source)
	if err != nil {
		return plugin.Plugin{}, "", "", nil, err
	}
	p, err = d.plugins.Inspect(src)
	if err != nil {
		done()
		return plugin.Plugin{}, "", "", nil, &api.Error{Code: api.CodeInvalidPlugin, Message: fmt.Sprintf("%s: %v", was.Source, err)}
	}
	if p.Name != was.Name {
		done()
		return plugin.Plugin{}, "", "", nil, &api.Error{Code: api.CodeInvalidPlugin,
			Message: fmt.Sprintf("%s now has Plugin %s, not %s; add it with `lane plugin add %s` if you want it", was.Source, p.Name, was.Name, was.Source)}
	}
	p.Source, p.Global = source, was.Global
	return p, src, source, done, nil
}

// handCheck is what the source of a Plugin added by hand had when last
// checked: the update it offered, if any, for the installed SHA-256.
type handCheck struct {
	installed string
	at        time.Time
	update    *api.PluginUpdate
}

// handAddedUpdates are the updates the sources of the Plugins added by hand
// offer, by Plugin name, as `lane plugin update` would find them: each
// source checked at most once in catalogMaxAge (a GitHub release is
// downloaded to check it), giving up after updateCheckTimeout.
func (d *Daemon) handAddedUpdates(ctx context.Context, pins map[string][]config.Pin) map[string]api.PluginUpdate {
	d.handMu.Lock()
	defer d.handMu.Unlock()
	if d.handChecked == nil {
		d.handChecked = map[string]handCheck{}
	}
	updates := map[string]api.PluginUpdate{}
	now := d.cfg.Clock.Now()
	for _, was := range d.plugins.List() {
		if was.Source == plugin.CatalogSource {
			continue
		}
		c, ok := d.handChecked[was.Name]
		if !ok || c.installed != was.SHA256+" "+was.Version || now.Sub(c.at) >= catalogMaxAge {
			c = handCheck{installed: was.SHA256 + " " + was.Version, at: now}
			ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
			p, _, _, done, err := d.readHandAdded(ctx, was)
			cancel()
			if err != nil {
				d.log.Warn("couldn't check a Plugin's source for updates", "plugin", was.Name, "source", was.Source, "err", err)
			} else {
				done()
				if p.Version != was.Version || p.SHA256 != was.SHA256 {
					c.update = &api.PluginUpdate{Name: was.Name, From: was.Version, To: p.Version, SHA256: p.SHA256}
				}
			}
			d.handChecked[was.Name] = c
		}
		if c.update != nil && allowedByAll(pins[was.Name], c.update.To) {
			updates[was.Name] = *c.update
		}
	}
	return updates
}
