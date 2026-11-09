package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/detect"
	"github.com/lanewayhq/laneway/internal/engine"
	"github.com/lanewayhq/laneway/internal/recipe"
)

// pluginRecipe is a Recipe a Plugin adds, as the Recipe Resolver sees it:
// detected by the rule its manifest declares, its answer (layer, Test
// Database decision and warnings, test env file) from its module.
type pluginRecipe struct {
	host   *Host
	plugin Plugin
	decl   RecipeDecl
}

// recipeResponse is a module's answer to a Recipe call (see
// sdk.RecipeResult).
type recipeResponse struct {
	// Layer is keyed like laneway.toml.
	Layer                json.RawMessage `json:"layer"`
	TestDatabase         bool            `json:"test_database"`
	TestDatabaseWarnings []string        `json:"test_database_warnings"`
	TestEnv              *testEnv        `json:"test_env"`
	ExportEnv            bool            `json:"export_env"`
	Warnings             []string        `json:"warnings"`
}

// testEnv is the env file a Recipe says its Project's tests read (see
// sdk.TestEnv).
type testEnv struct {
	File    string `json:"file"`
	Layered bool   `json:"layered"`
}

func (r pluginRecipe) Name() string           { return r.decl.Name }
func (r pluginRecipe) Detection() detect.Rule { return r.decl.Detect }

// source names the Recipe in errors and warnings.
func (r pluginRecipe) source() string {
	return fmt.Sprintf("the %s Recipe (Plugin %s)", r.decl.Name, r.plugin.Name)
}

func (r pluginRecipe) Defaults(dir string) (recipe.Result, error) {
	var res recipeResponse
	if err := r.host.call(r.plugin, r.source(), request{Call: callRecipe, Name: r.decl.Name}, dir, &res); err != nil {
		return recipe.Result{}, err
	}
	l, err := parseLayer(r.source(), res.Layer, r.host.Engines())
	if err != nil {
		return recipe.Result{}, err
	}
	for _, w := range res.Warnings {
		l.Warnings = append(l.Warnings, r.source()+": "+w)
	}
	answer := recipe.Result{Layer: l, TestDatabase: res.TestDatabase, TestDatabaseWarnings: res.TestDatabaseWarnings, ExportEnv: res.ExportEnv}
	if t := res.TestEnv; t != nil {
		// Laneway writes the file: it must be the Worktree's own.
		if t.File == "" || t.File == "." || t.File == ".." || strings.ContainsAny(t.File, `/\`) {
			return recipe.Result{}, fmt.Errorf("%s: test_env.file %q must be a file name in the Worktree, such as %q",
				r.source(), t.File, recipe.DefaultTestEnv.File)
		}
		answer.TestEnv = recipe.TestEnv{File: t.File, Layered: t.Layered}
	}
	return answer, nil
}

// parseLayer reads a layer a module returned as JSON keyed like
// laneway.toml, checking it exactly as laneway.toml is checked: it goes
// through TOML so that config.ParseLayer's validation and errors apply.
func parseLayer(source string, raw json.RawMessage, engines engine.Set) (config.Layer, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return config.Layer{Source: source}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return config.Layer{}, fmt.Errorf("%s: its layer isn't valid JSON: %w", source, err)
	}
	table, ok := tomlValue(v).(map[string]any)
	if !ok {
		return config.Layer{}, fmt.Errorf("%s: its layer must be a JSON object keyed like laneway.toml", source)
	}
	// A Service without a version is at its Engine's default one, among
	// engines.
	if services, ok := table["services"].(map[string]any); ok {
		for name, version := range services {
			if e, known := engines.Lookup(name); known && version == "" {
				services[name] = e.DefaultVersion
			}
		}
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(table); err != nil {
		return config.Layer{}, fmt.Errorf("%s: its layer can't be read as config: %w", source, err)
	}
	return config.ParseLayer(source, buf.Bytes())
}

// tomlValue is a decoded JSON value as TOML can encode it: numbers as
// integers where they are whole, and without nulls, which TOML lacks and
// mean "not set".
func tomlValue(v any) any {
	switch v := v.(type) {
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i
		}
		f, _ := v.Float64()
		return f
	case map[string]any:
		for k, e := range v {
			if e == nil {
				delete(v, k)
				continue
			}
			v[k] = tomlValue(e)
		}
		return v
	case []any:
		out := v[:0]
		for _, e := range v {
			if e != nil {
				out = append(out, tomlValue(e))
			}
		}
		return out
	}
	return v
}
