// Command boost is the boost Plugin, built into Laneway: the Laravel Boost
// Generator, which gives Boost the agent instructions (see
// agents.BoostEffects).
package main

import (
	"github.com/lanewayhq/laneway/internal/builtin/agents"
	"github.com/lanewayhq/laneway/sdk"
)

func main() {
	sdk.Run(sdk.Plugin{Generators: map[string]sdk.Generator{"boost": boost}})
}

func boost(p sdk.Project) (sdk.GeneratorResult, error) {
	return sdk.GeneratorResult{Effects: agents.BoostEffects(p)}, nil
}
