package builtin

import (
	"encoding/json"
	"fmt"

	"github.com/lanewayhq/laneway/sdk"
)

// boostGuideline is where a Project using Laravel Boost gets the agent
// instructions: Boost writes the guideline files of the agents it's set up
// for (AGENTS.md, CLAUDE.md…) itself, inside its own block, and includes
// every file in .ai/guidelines when it does.
const boostGuideline = ".ai/guidelines/laneway.md"

// boost is the Laravel Boost Generator: it gives Boost the agent
// instructions (see boostEffects).
func boost(p sdk.Project) (sdk.GeneratorResult, error) {
	return sdk.GeneratorResult{Effects: boostEffects(p)}, nil
}

// usesBoost reports whether the Project requires Laravel Boost. It reads
// composer.json, which is committed, rather than boost.json, which Boost
// suggests ignoring.
func usesBoost(p sdk.Project) bool {
	data, err := p.ReadFile("composer.json")
	if err != nil {
		return false
	}
	var composer struct {
		Require    map[string]json.RawMessage `json:"require"`
		RequireDev map[string]json.RawMessage `json:"require-dev"`
	}
	if json.Unmarshal(data, &composer) != nil {
		return false
	}
	_, req := composer.Require["laravel/boost"]
	_, dev := composer.RequireDev["laravel/boost"]
	return req || dev
}

// boostEffects put the agent instructions in Boost's custom guidelines, and
// take out the blocks an earlier run put in AGENTS.md and CLAUDE.md: Boost
// adds the guideline to those files, so they would say it twice, and
// CLAUDE.md importing AGENTS.md would repeat all of Boost's guidelines.
func boostEffects(p sdk.Project) []sdk.Effect {
	guideline := sdk.Markdown(boostGuideline, agentsBlock)
	guideline.Warning = fmt.Sprintf("this Project uses Laravel Boost, so the agent instructions are in %s; "+
		"run `php artisan boost:update` to add them to the guideline files of the agents Boost is set up for", boostGuideline)
	effects := []sdk.Effect{
		sdk.File(guideline),
		sdk.File(sdk.FileBlock{Path: agentsFile, RemoveMarkdown: true}),
	}
	if !sameFile(p, claudeFile, agentsFile) {
		effects = append(effects, sdk.File(sdk.FileBlock{Path: claudeFile, RemoveMarkdown: true}))
	}
	return effects
}
