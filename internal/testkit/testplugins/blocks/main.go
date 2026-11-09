// Command blocks is a test Plugin: a Generator that returns the effects
// listed in the Project's blocks.json, so a test says what a Generator
// returns by writing that file. Its Generator next, which its manifest
// doesn't declare (tests that need it do, such as for a newer release),
// returns those listed in next.json.
package main

import (
	"encoding/json"

	"github.com/lanewayhq/laneway/sdk"
)

func main() {
	sdk.Run(sdk.Plugin{Generators: map[string]sdk.Generator{
		"blocks": listedIn("blocks.json"),
		"next":   listedIn("next.json"),
	}})
}

// listedIn is a Generator returning the effects listed in the Project's
// file.
func listedIn(file string) sdk.Generator {
	return func(p sdk.Project) (sdk.GeneratorResult, error) {
		data, err := p.ReadFile(file)
		if err != nil {
			return sdk.GeneratorResult{}, err
		}
		var res sdk.GeneratorResult
		err = json.Unmarshal(data, &res.Effects)
		return res, err
	}
}
