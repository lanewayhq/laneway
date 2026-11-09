package daemon

import (
	"path/filepath"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/plugin"
)

// listPlugins is the body of GET /v1/plugins.
func (d *Daemon) listPlugins() api.PluginList {
	list := api.PluginList{Plugins: []api.Plugin{}}
	for _, p := range d.plugins.List() {
		list.Plugins = append(list.Plugins, apiPlugin(p))
	}
	return list
}

// addPlugin adds the Plugin in the directory req.Source, or with req.Preview
// only reads and checks it, so the developer can see what it may do first.
// Projects' config is read anew every time it's needed, so the Plugin's
// Recipes detect Projects from the next read on.
func (d *Daemon) addPlugin(req api.AddPluginRequest) (api.AddPluginResponse, error) {
	if !filepath.IsAbs(req.Source) {
		return api.AddPluginResponse{}, &api.Error{Code: api.CodeBadRequest, Message: "source must be an absolute path"}
	}
	var res api.AddPluginResponse
	replaced := func(name string) {
		for _, p := range d.plugins.List() {
			if p.Name == name {
				old := apiPlugin(p)
				res.Replaces = &old
			}
		}
	}
	if req.Preview {
		p, err := d.plugins.Inspect(req.Source)
		if err != nil {
			return api.AddPluginResponse{}, &api.Error{Code: api.CodeInvalidPlugin, Message: err.Error()}
		}
		res.Plugin = apiPlugin(p)
		replaced(p.Name)
		return res, nil
	}
	p, old, err := d.plugins.Add(req.Source)
	if err != nil {
		return api.AddPluginResponse{}, &api.Error{Code: api.CodeInvalidPlugin, Message: err.Error()}
	}
	d.log.Info("Plugin added", "plugin", p.Name, "version", p.Version, "source", p.Source, "sha256", p.SHA256)
	res.Plugin = apiPlugin(p)
	if old != nil {
		o := apiPlugin(*old)
		res.Replaces = &o
	}
	return res, nil
}

func apiPlugin(p plugin.Plugin) api.Plugin {
	ap := api.Plugin{Name: p.Name, Version: p.Version, Source: p.Source, SHA256: p.SHA256,
		Recipes: []api.PluginRecipe{}, Commands: []api.PluginCommand{}, Effects: []api.PluginEffect{}, HTTPHosts: []string{}}
	for _, r := range p.Recipes {
		ap.Recipes = append(ap.Recipes, api.PluginRecipe{Name: r.Name, Detects: r.Detect.String()})
	}
	for _, c := range p.Commands {
		ap.Commands = append(ap.Commands, api.PluginCommand{Name: c.Name, Summary: c.Summary})
	}
	for _, kind := range p.Capabilities.Effects {
		ap.Effects = append(ap.Effects, api.PluginEffect{Kind: kind, Description: plugin.EffectKinds[kind]})
	}
	ap.HTTPHosts = append(ap.HTTPHosts, p.Capabilities.HTTP...)
	return ap
}
