// Command blocks is a test Plugin: a Generator that returns the effects
// listed in the Project's blocks.json, so a test says what a Generator
// returns by writing that file.
package main

import (
	"encoding/json"

	"github.com/lanewayhq/laneway/sdk"
)

func main() {
	sdk.Run(sdk.Plugin{Generators: map[string]sdk.Generator{"blocks": blocks}})
}

func blocks(p sdk.Project) (sdk.GeneratorResult, error) {
	data, err := p.ReadFile("blocks.json")
	if err != nil {
		return sdk.GeneratorResult{}, err
	}
	var res sdk.GeneratorResult
	err = json.Unmarshal(data, &res.Effects)
	return res, err
}
