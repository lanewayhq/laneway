package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/BurntSushi/toml"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/detect"
	"github.com/lanewayhq/laneway/internal/recipe"
)

// pluginRecipe is a Recipe a Plugin adds, as the Recipe Resolver sees it:
// detected by the rule its manifest declares, its layer and Test Database
// decision from its module.
type pluginRecipe struct {
	host   *Host
	plugin Plugin
	decl   RecipeDecl
}

var (
	_ recipe.TestDatabaseRecipe = pluginRecipe{}
	_ recipe.TestDatabaseWarner = pluginRecipe{}
)

// recipeResponse is a module's answer to a Recipe call (see
// sdk.RecipeResult).
type recipeResponse struct {
	// Layer is keyed like laneway.toml.
	Layer                json.RawMessage `json:"layer"`
	TestDatabase         bool            `json:"test_database"`
	TestDatabaseWarnings []string        `json:"test_database_warnings"`
	Warnings             []string        `json:"warnings"`
}

func (r pluginRecipe) Name() string           { return r.decl.Name }
func (r pluginRecipe) Detection() detect.Rule { return r.decl.Detect }

// source names the Recipe in errors and warnings.
func (r pluginRecipe) source() string {
	return fmt.Sprintf("the %s Recipe (Plugin %s)", r.decl.Name, r.plugin.Name)
}

func (r pluginRecipe) call(dir string) (recipeResponse, error) {
	var res recipeResponse
	err := r.host.call(r.plugin, r.source(), request{Call: callRecipe, Name: r.decl.Name}, dir, &res)
	return res, err
}

func (r pluginRecipe) Defaults(dir string) (config.Layer, error) {
	res, err := r.call(dir)
	if err != nil {
		return config.Layer{}, err
	}
	l, err := parseLayer(r.source(), res.Layer)
	if err != nil {
		return config.Layer{}, err
	}
	for _, w := range res.Warnings {
		l.Warnings = append(l.Warnings, r.source()+": "+w)
	}
	return l, nil
}

func (r pluginRecipe) AutoTestDatabase(dir string) bool {
	res, err := r.call(dir)
	return err == nil && res.TestDatabase
}

func (r pluginRecipe) TestDatabaseWarnings(dir string) []string {
	res, err := r.call(dir)
	if err != nil {
		return nil
	}
	return res.TestDatabaseWarnings
}

// parseLayer reads a layer a module returned as JSON keyed like
// laneway.toml, checking it exactly as laneway.toml is checked: it goes
// through TOML so that config.ParseLayer's validation and errors apply.
func parseLayer(source string, raw json.RawMessage) (config.Layer, error) {
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
