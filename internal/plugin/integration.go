package plugin

import (
	"encoding/json"
	"fmt"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/detect"
	"github.com/lanewayhq/laneway/internal/engine"
	"github.com/lanewayhq/laneway/internal/recipe"
)

// IntegrationDecl is an Integration a Plugin adds: its name and the
// detection rule a Project using it matches. Its module answers
// sdk.CallIntegration for the name with the layer the Integration adds.
type IntegrationDecl struct {
	Name   string      `json:"name"`
	Detect detect.Rule `json:"detect"`
}

// validateIntegrations checks the Integrations a manifest declares.
func validateIntegrations(integrations []IntegrationDecl) error {
	if err := checkNames("integrations", "Integration", integrations, func(in IntegrationDecl) string { return in.Name }); err != nil {
		return err
	}
	for i, in := range integrations {
		if err := in.Detect.Validate(); err != nil {
			return fmt.Errorf("integrations[%d] (%s): %w", i, in.Name, err)
		}
	}
	return nil
}

// integrationsTaken fails when p adds an Integration of a name another
// installed Plugin's Integrations have, since the Recipe Resolver and `lane
// info` know Integrations by name.
func (h *Host) integrationsTaken(p Plugin) error {
	taken := map[string]string{} // Integration name → who has it
	h.mu.Lock()
	for _, other := range h.plugins {
		if other.Name == p.Name {
			continue // adding p replaces it
		}
		for _, in := range other.Integrations {
			taken[in.Name] = "Plugin " + other.Name
		}
	}
	h.mu.Unlock()
	for _, in := range p.Integrations {
		if who, ok := taken[in.Name]; ok {
			return fmt.Errorf("Plugin %s adds Integration %s, but %s already has an Integration of that name", p.Name, in.Name, who)
		}
	}
	return nil
}

// Integrations are every Integration that may detect a Project: the
// installed Plugins'. The Recipe Resolver applies the detected ones by name,
// whichever Plugin adds them.
func (h *Host) Integrations() []recipe.Integration {
	return h.integrations(h.Engines(), h.catalogEngines())
}

// integrations are Integrations, whose layers' Services may name engines.
func (h *Host) integrations(engines engine.Set, catalog []string) []recipe.Integration {
	var integrations []recipe.Integration
	for _, p := range h.List() {
		for _, in := range p.Integrations {
			integrations = append(integrations, pluginIntegration{host: h, plugin: p, decl: in, engines: engines, catalog: catalog})
		}
	}
	return integrations
}

// pluginIntegration is an Integration a Plugin adds, as the Recipe Resolver
// sees it: detected by the rule its manifest declares, its layer from its
// module.
type pluginIntegration struct {
	host   *Host
	plugin Plugin
	decl   IntegrationDecl
	// engines are the Engines its layer's Services may name.
	engines engine.Set
	// catalog are the Engines Laneway can fetch from the Catalog (see
	// Options.CatalogEngines).
	catalog []string
}

// integrationResponse is a module's answer to an Integration call (see
// sdk.IntegrationResult).
type integrationResponse struct {
	// Layer is keyed like laneway.toml.
	Layer    json.RawMessage `json:"layer"`
	Warnings []string        `json:"warnings"`
}

func (in pluginIntegration) Name() string           { return in.decl.Name }
func (in pluginIntegration) Detection() detect.Rule { return in.decl.Detect }

// source names the Integration, and so its Plugin, in errors and warnings,
// such as the Resolver's error when two Integrations set the same key.
func (in pluginIntegration) source() string {
	return fmt.Sprintf("the %s Integration (Plugin %s)", in.decl.Name, in.plugin.Name)
}

func (in pluginIntegration) Defaults(dir string) (config.Layer, error) {
	var res integrationResponse
	if err := in.host.call(in.plugin, in.source(), request{Call: callIntegration, Name: in.decl.Name}, dir, &res); err != nil {
		return config.Layer{}, err
	}
	l, err := parseLayer(in.source(), res.Layer, in.engines, in.catalog)
	if err != nil {
		return config.Layer{}, err
	}
	for _, w := range res.Warnings {
		l.Warnings = append(l.Warnings, in.source()+": "+w)
	}
	return l, nil
}
