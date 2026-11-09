// Command t3 is the t3 Plugin, built into Laneway: the T3 Code Generator
// behind `lane harness add t3`.
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lanewayhq/laneway/internal/builtin/agents"
	"github.com/lanewayhq/laneway/sdk"
)

func main() {
	sdk.Run(sdk.Plugin{Generators: map[string]sdk.Generator{"t3": t3}})
}

// t3File is T3 Code's committed project file.
const t3File = "t3.json"

// t3Schema is the JSON Schema a new t3.json points at, as T3 Code writes it.
const t3Schema = "https://t3.codes/schema/t3.json"

// t3ScriptName names the generated script in T3 Code's scripts menu, and is
// how Laneway recognises its own script on later runs (t3.json allows no keys
// of our own).
const t3ScriptName = "Laneway: wait until Ready"

// t3Script is a T3 Code script, its keys in the order T3 Code writes them.
type t3Script struct {
	Name                string `json:"name"`
	Command             string `json:"command"`
	Icon                string `json:"icon"`
	RunOnWorktreeCreate bool   `json:"runOnWorktreeCreate"`
	Async               bool   `json:"async"`
}

// t3 is the T3 Code Generator. It adds a script to t3.json that T3 Code runs
// in every Worktree it creates for a thread (runOnWorktreeCreate), holding
// the agent until it exits (async: false). It also adds the agent
// instructions to AGENTS.md, which the agents T3 Code runs read (or, with
// Laravel Boost, to Boost's guidelines).
func t3(p sdk.Project) (sdk.GeneratorResult, error) {
	script := t3Script{Name: t3ScriptName, Command: agents.ReadyCommand, Icon: "configure", RunOnWorktreeCreate: true}
	effects := []sdk.Effect{sdk.File(sdk.FileBlock{Path: t3File, JSON: &sdk.JSONBlock{
		New: map[string]any{"$schema": t3Schema},
		Entries: []sdk.JSONEntry{{
			At:    []string{"scripts"},
			Match: map[string]any{"name": t3ScriptName},
			Value: script,
		}},
	}})}
	if others := otherWorktreeCreateScripts(p); len(others) > 0 {
		effects = append(effects, sdk.Warning(fmt.Sprintf(
			"%s: T3 Code only runs the first script with runOnWorktreeCreate, and %s %s too, so they won't all run; "+
				"run `lane worktree ready` at the end of yours instead, or turn runOnWorktreeCreate off on all but one",
			t3File, strings.Join(others, ", "), plural(len(others), "has it", "have it"))))
	}
	instructions, err := agents.InstructionEffects(p, false)
	if err != nil {
		return sdk.GeneratorResult{}, err
	}
	return sdk.GeneratorResult{Effects: append(effects, instructions...)}, nil
}

// otherWorktreeCreateScripts returns the quoted names of the scripts in the
// Worktree's t3.json, other than Laneway's, that run on worktree creation.
// A t3.json Laneway can't read has none: merging into it fails anyway.
func otherWorktreeCreateScripts(p sdk.Project) []string {
	data, err := p.ReadFile(t3File)
	if err != nil {
		return nil
	}
	var doc struct {
		Scripts []struct {
			Name                string `json:"name"`
			RunOnWorktreeCreate any    `json:"runOnWorktreeCreate"`
		} `json:"scripts"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	var names []string
	for _, s := range doc.Scripts {
		if s.Name != t3ScriptName && s.RunOnWorktreeCreate == true {
			names = append(names, fmt.Sprintf("%q", s.Name))
		}
	}
	return names
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
