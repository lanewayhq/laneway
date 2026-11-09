package daemon

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/engine"
	"github.com/lanewayhq/laneway/internal/plugin"
)

// removePlugin removes the installed Plugin called name, unless a Linked
// Project uses it (spec #48): removing it would break the Project.
func (d *Daemon) removePlugin(ctx context.Context, name string) (api.RemovePluginResponse, error) {
	p, ok := d.plugins.Plugin(name)
	if !ok {
		return api.RemovePluginResponse{}, &api.Error{Code: api.CodeUnknownPlugin,
			Message: fmt.Sprintf("no Plugin named %s is installed; `lane plugin list` lists them", name)}
	}
	if users := d.pluginUsers(p); len(users) > 0 {
		return api.RemovePluginResponse{}, &api.Error{Code: api.CodePluginInUse,
			Message: fmt.Sprintf("Plugin %s is used by these Linked Projects, so it wasn't removed:\n  %s\nUnlink them, or change their config so they stop using it, then remove it again",
				p.Name, strings.Join(users, "\n  "))}
	}
	p, err := d.plugins.Remove(name)
	if err != nil {
		return api.RemovePluginResponse{}, err
	}
	d.log.Info("Plugin removed", "plugin", p.Name, "version", p.Version)
	return api.RemovePluginResponse{Plugin: apiPlugin(p), Kept: d.unserveEngines(ctx, p)}, nil
}

// unserveEngines stops serving the Services of the removed Plugin p's
// Engines, keeping their data (spec #48: removing a Plugin is never a way to
// lose data), and returns every Service of them the Registry records. The
// Registry keeps their ports, so adding the Plugin again gives each Service
// its port and its data back.
func (d *Daemon) unserveEngines(ctx context.Context, p plugin.Plugin) []api.KeptService {
	kept := []api.KeptService{}
	for _, rs := range d.registry.Services() {
		if !slices.ContainsFunc(p.Engines, func(e *engine.Manifest) bool { return e.Name == rs.Engine }) {
			continue
		}
		if _, served := d.services.Status(rs.Name()); served {
			if err := d.services.Unserve(ctx, rs.Name()); err != nil {
				d.log.Error("stopping the Service of a removed Plugin", "plugin", p.Name, "service", rs.Name(), "err", err)
			}
		}
		kept = append(kept, api.KeptService{Name: rs.Name(), Volume: d.services.Volume(rs.Ref)})
	}
	return kept
}

// pluginUsers are the Linked Projects any of whose Worktrees use p, each as
// "<project>: <how it uses p>", sorted by Project.
func (d *Daemon) pluginUsers(p plugin.Plugin) []string {
	var users []string
	for _, proj := range d.registry.Projects() {
		var uses []string
		for _, w := range d.apiProject(proj).Worktrees {
			for _, use := range d.plugins.Uses(p, w.Path) {
				if !slices.Contains(uses, use) {
					uses = append(uses, use)
				}
			}
		}
		if len(uses) > 0 {
			users = append(users, proj.Name+": "+strings.Join(uses, ", "))
		}
	}
	slices.Sort(users)
	return users
}
