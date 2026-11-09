// Command layers is a test Plugin whose Recipe and Integrations return the
// layer the Project spells out for them: the layered Recipe the layer in
// layered.json, and each Integration the layer in <name>.json, so that tests
// choose what each one sets. Its manifest declares the assets and queue
// Integrations; tests may rewrite it to declare any of the names below.
package main

import (
	"encoding/json"

	"github.com/lanewayhq/laneway/sdk"
)

func main() {
	integrations := map[string]sdk.Integration{}
	for _, name := range []string{"assets", "queue", "search", "mail"} {
		integrations[name] = func(p sdk.Project) (sdk.IntegrationResult, error) {
			var res sdk.IntegrationResult
			return res, read(p, name+".json", &res.Layer)
		}
	}
	sdk.Run(sdk.Plugin{
		Recipes: map[string]sdk.Recipe{"layered": func(p sdk.Project) (sdk.RecipeResult, error) {
			var res sdk.RecipeResult
			return res, read(p, "layered.json", &res.Layer)
		}},
		Integrations: integrations,
	})
}

func read(p sdk.Project, file string, layer *sdk.Layer) error {
	data, err := p.ReadFile(file)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, layer)
}
