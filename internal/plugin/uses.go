package plugin

import (
	"slices"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/engine"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/service"
)

// Uses says how the Worktree in dir uses p, for people, such as "its Recipe
// procfile": one entry per thing of p's it uses, or none if it doesn't use
// p. See UsedBy.
//
// If the Worktree's config can't be resolved (a Plugin's Recipe fails, say),
// it still answers from what can be read without resolving: p's detection
// rules, and the Plugins and Services the global config and the Worktree's
// config files name. If those
// can't be read either and p adds Engines, it says the Worktree may use
// them: telling would need the config.
func (h *Host) Uses(p Plugin, dir string) []string {
	cfg, err := h.ResolveDir(dir)
	if err == nil {
		return p.UsedBy(cfg)
	}
	var uses []string
	if g, err := config.LoadGlobal(h.opts.GlobalConfig); err == nil && slices.Contains(g.Plugins, p.Name) {
		uses = append(uses, "its config turns it on (plugins)")
	}
	for _, r := range p.Recipes {
		if _, ok := r.Detect.Match(dir); ok {
			uses = append(uses, "its Recipe "+r.Name)
		}
	}
	for _, in := range p.Integrations {
		if _, ok := in.Detect.Match(dir); ok {
			uses = append(uses, "its Integration "+in.Name)
		}
	}
	layers, err := config.LoadLayers(dir)
	if err != nil {
		if len(p.Engines) == 0 {
			return uses
		}
		return append(uses, "maybe its Services, but its config can't be read: "+err.Error())
	}
	if !slices.Contains(uses, "its config turns it on (plugins)") &&
		slices.ContainsFunc(layers, func(l config.Layer) bool { return slices.Contains(l.Plugins, p.Name) }) {
		uses = append(uses, "its config turns it on (plugins)")
	}
	if len(p.Engines) == 0 {
		return uses
	}
	versions := map[string]string{} // engine → version, as the Resolver merges them
	database := ""
	for _, l := range layers {
		for e, v := range l.Services {
			versions[e] = v
		}
		if l.Database != nil && l.Database.Engine != nil {
			database = *l.Database.Engine
		}
	}
	for _, e := range p.Engines {
		v, ok := versions[e.Name]
		if !ok && database == e.Name {
			v, ok = e.DefaultVersion, true
		}
		if ok {
			uses = append(uses, "its Service "+service.Ref{Engine: e.Name, Version: v}.Name())
		}
	}
	return uses
}

// UsedBy says how a Worktree whose resolved config is cfg uses p, as Uses
// does.
//
// A Worktree uses a Plugin when its config turns the Plugin on by name
// (Resolved.Plugins: laneway.toml, laneway.local.toml or the global
// config's plugins; only personal config can turn on a machine-wide
// Plugin), the Plugin's Recipe or one of its Integrations detected it, or
// it uses a Service of the Plugin's Engine (which includes the Service its
// database is in). (Recipe, Integration and Engine names are unique across
// Plugins and Laneway.)
func (p Plugin) UsedBy(cfg recipe.Resolved) []string {
	var uses []string
	if slices.Contains(cfg.Plugins, p.Name) {
		uses = append(uses, "its config turns it on (plugins)")
	}
	if slices.ContainsFunc(p.Recipes, func(r RecipeDecl) bool { return r.Name == cfg.Recipe }) {
		uses = append(uses, "its Recipe "+cfg.Recipe)
	}
	for _, in := range p.Integrations {
		if slices.Contains(cfg.Integrations, in.Name) {
			uses = append(uses, "its Integration "+in.Name)
		}
	}
	for _, s := range cfg.Services {
		if slices.ContainsFunc(p.Engines, func(e *engine.Manifest) bool { return e.Name == s.Engine }) {
			uses = append(uses, "its Service "+s.Name())
		}
	}
	return uses
}
