package daemon

import (
	"strconv"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/plugin"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/service"
)

// The links, badges and actions Plugins show on the Dashboard (spec #48):
// a Service shows those of the Plugin that adds its Engine; a Project or
// Worktree those of the Plugins it uses.

// serviceItems are what Plugins show on the Service st.
func (d *Daemon) serviceItems(st service.Status) api.PluginItems {
	var items api.PluginItems
	vars := map[string]string{
		"dashboard_domain": d.cfg.DashboardDomain,
		"service":          st.Name(),
		"engine":           st.Engine.Name,
		"version":          st.Version,
		"host":             st.Host,
		"port":             strconv.Itoa(st.Port),
	}
	for _, p := range d.plugins.Showing() {
		if addsEngine(p, st.Engine.Name) {
			addItems(&items, p.Name, p.Dashboard.In(plugin.ScopeService, vars))
		}
	}
	return items
}

// worktreeItems are what the Plugins the Worktree w uses show on it (scope
// worktree) or, for a main Worktree, on its Project (scope project). cfg is
// w's resolved config.
func (d *Daemon) worktreeItems(scope string, project string, w api.Worktree, cfg recipe.Resolved) api.PluginItems {
	var items api.PluginItems
	vars := map[string]string{
		"dashboard_domain": d.cfg.DashboardDomain,
		"project":          project,
		"worktree":         w.Name,
		"domain":           w.PrimaryDomain,
	}
	for _, p := range d.plugins.Showing() {
		if p.ShowsOn(cfg) {
			addItems(&items, p.Name, p.Dashboard.In(scope, vars))
		}
	}
	return items
}

// addsEngine reports whether p adds the Engine called name.
func addsEngine(p plugin.Plugin, name string) bool {
	for _, e := range p.Engines {
		if e.Name == name {
			return true
		}
	}
	return false
}

// addItems adds shown, what the Plugin called name shows, to items.
func addItems(items *api.PluginItems, name string, shown plugin.Dashboard) {
	for _, l := range shown.Links {
		items.Links = append(items.Links, api.PluginLink{Plugin: name, Label: l.Label, URL: l.URL})
	}
	for _, b := range shown.Badges {
		items.Badges = append(items.Badges, api.PluginBadge{Plugin: name, Text: b.Text, Title: b.Title})
	}
	for _, a := range shown.Actions {
		items.Actions = append(items.Actions, api.PluginAction{Plugin: name, Label: a.Label, Command: a.Command, Args: a.Args})
	}
}
