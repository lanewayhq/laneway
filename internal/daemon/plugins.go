package daemon

import (
	"context"
	"slices"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/plugin"
)

// listPlugins is the body of GET /v1/plugins: the installed Plugins, each
// with its available update, if any.
func (d *Daemon) listPlugins(ctx context.Context) api.PluginList {
	updates := d.availableUpdates(ctx)
	list := api.PluginList{Plugins: []api.Plugin{}}
	for _, p := range d.plugins.List() {
		ap := apiPlugin(p)
		if u, ok := updates[p.Name]; ok {
			ap.Update = &u
		}
		list.Plugins = append(list.Plugins, ap)
	}
	return list
}

// addPlugin adds the Plugin in the directory or GitHub repository
// req.Source (see pluginSource), or with req.Preview only reads and checks
// it, so the developer can see what it may do first. Projects' config is
// read anew every time it's needed, so the Plugin's Recipes detect Projects
// from the next read on.
func (d *Daemon) addPlugin(ctx context.Context, req api.AddPluginRequest) (api.AddPluginResponse, error) {
	src, source, done, err := d.pluginSource(ctx, req.Source)
	if err != nil {
		return api.AddPluginResponse{}, err
	}
	defer done()
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
		p, err := d.plugins.Inspect(src)
		if err != nil {
			return api.AddPluginResponse{}, &api.Error{Code: api.CodeInvalidPlugin, Message: err.Error()}
		}
		p.Source, p.Global = source, req.Global
		res.Plugin = apiPlugin(p)
		replaced(p.Name)
		return res, nil
	}
	add := d.plugins.AddFrom
	if req.Global {
		add = d.plugins.AddGlobal
	}
	p, old, err := add(src, source)
	if err != nil {
		return api.AddPluginResponse{}, &api.Error{Code: api.CodeInvalidPlugin, Message: err.Error()}
	}
	d.log.Info("Plugin added", "plugin", p.Name, "version", p.Version, "source", p.Source, "sha256", p.SHA256, "global", p.Global)
	res.Plugin = apiPlugin(p)
	if old != nil {
		o := apiPlugin(*old)
		res.Replaces = &o
	}
	return res, nil
}

func apiPlugin(p plugin.Plugin) api.Plugin {
	ap := api.Plugin{Name: p.Name, Version: p.Version, Source: p.Source, SHA256: p.SHA256, Global: p.Global,
		Recipes: []api.PluginRecipe{}, Integrations: []api.PluginIntegration{}, Commands: []api.PluginCommand{}, Engines: []api.PluginEngine{}, Generators: []string{}, Hooks: []string{}, Effects: []api.PluginEffect{}, HTTPHosts: []string{}}
	for _, r := range p.Recipes {
		ap.Recipes = append(ap.Recipes, api.PluginRecipe{Name: r.Name, Detects: r.Detect.String()})
	}
	for _, in := range p.Integrations {
		ap.Integrations = append(ap.Integrations, api.PluginIntegration{Name: in.Name, Detects: in.Detect.String()})
	}
	for _, c := range p.Commands {
		ap.Commands = append(ap.Commands, api.PluginCommand{Name: c.Name, Summary: c.Summary})
	}
	for _, e := range p.Engines {
		ap.Engines = append(ap.Engines, api.PluginEngine{Name: e.Name, Image: e.Image, Port: e.Port})
	}
	for _, g := range p.Generators {
		ap.Generators = append(ap.Generators, g.Name)
	}
	for _, e := range plugin.Events {
		if slices.Contains(p.Hooks, e.Name) {
			ap.Hooks = append(ap.Hooks, e.Name)
		}
	}
	for _, kind := range p.Capabilities.Effects {
		ap.Effects = append(ap.Effects, api.PluginEffect{Kind: kind, Description: plugin.EffectKinds[kind]})
	}
	ap.HTTPHosts = append(ap.HTTPHosts, p.Capabilities.HTTP...)
	return ap
}
