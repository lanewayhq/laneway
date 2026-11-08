package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
)

// boostGuideline is where a Project using Laravel Boost gets the agent
// instructions: Boost writes the guideline files of the agents it's set up
// for (AGENTS.md, CLAUDE.md…) itself, inside its own block, and includes
// every file in .ai/guidelines when it does.
const boostGuideline = ".ai/guidelines/laneway.md"

// usesBoost reports whether the Project at root requires Laravel Boost. It
// reads composer.json, which is committed, rather than boost.json, which
// Boost suggests ignoring.
func usesBoost(root string) (bool, error) {
	data, err := readFile(root, "composer.json")
	if err != nil || data == nil {
		return false, err
	}
	var composer struct {
		Require    map[string]json.RawMessage `json:"require"`
		RequireDev map[string]json.RawMessage `json:"require-dev"`
	}
	if json.Unmarshal(data, &composer) != nil {
		return false, nil
	}
	_, req := composer.Require["laravel/boost"]
	_, dev := composer.RequireDev["laravel/boost"]
	return req || dev, nil
}

// boostEdits puts the agent instructions in Boost's custom guidelines, and
// takes out the blocks an earlier run put in AGENTS.md and CLAUDE.md: Boost
// adds the guideline to those files, so they would say it twice, and
// CLAUDE.md importing AGENTS.md would repeat all of Boost's guidelines.
func boostEdits(root string) ([]edit, error) {
	e, err := markdownEdit(root, boostGuideline, agentsBlock)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(e.old, e.new) {
		e.warnings = append(e.warnings, fmt.Sprintf(
			"this Project uses Laravel Boost, so the agent instructions are in %s; "+
				"run `php artisan boost:update` to add them to the guideline files of the agents Boost is set up for", boostGuideline))
	}
	edits := []edit{e}
	for _, rel := range []string{agentsFile, claudeFile} {
		if rel == claudeFile && sameFile(filepath.Join(root, claudeFile), filepath.Join(root, agentsFile)) {
			continue
		}
		e, ok, err := removeBlockEdit(root, rel)
		if err != nil {
			return nil, err
		}
		if ok {
			edits = append(edits, e)
		}
	}
	return edits, nil
}
