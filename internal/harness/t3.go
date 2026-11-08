package harness

import (
	"fmt"
	"strings"
)

// t3File is T3 Code's committed project file.
const t3File = "t3.json"

// t3Schema is the JSON Schema a new t3.json points at, as T3 Code writes it.
const t3Schema = "https://t3.codes/schema/t3.json"

// t3ScriptName names the generated script in T3 Code's scripts menu, and is
// how Laneway recognises its own script on later runs (t3.json allows no keys
// of our own).
const t3ScriptName = "Laneway: wait until Ready"

// t3 adds a script to t3.json that T3 Code runs in every Worktree it creates
// for a thread (runOnWorktreeCreate), holding the agent until it exits
// (async: false). It also adds the agent instructions to AGENTS.md, which
// the agents T3 Code runs read (or, with Laravel Boost, to Boost's
// guidelines).
func t3(root string) ([]edit, error) {
	old, err := readFile(root, t3File)
	if err != nil {
		return nil, err
	}
	var others []string
	data, err := mergeJSON(t3File, old, func(doc *object) (bool, error) {
		if old == nil {
			doc.set("$schema", t3Schema)
		}
		changed, err := mergeT3Script(doc)
		others = otherWorktreeCreateScripts(doc)
		return changed, err
	})
	if err != nil {
		return nil, err
	}
	e := edit{path: t3File, old: old, new: data}
	if len(others) > 0 {
		e.warnings = append(e.warnings, fmt.Sprintf(
			"%s: T3 Code only runs the first script with runOnWorktreeCreate, and %s %s too, so they won't all run; "+
				"run `lane worktree ready` at the end of yours instead, or turn runOnWorktreeCreate off on all but one",
			t3File, strings.Join(others, ", "), plural(len(others), "has it", "have it")))
	}
	instructions, err := instructionEdits(root, false)
	if err != nil {
		return nil, err
	}
	return append([]edit{e}, instructions...), nil
}

// mergeT3Script makes doc.scripts hold Laneway's script once: updated in place
// if it's there, else added at the end.
func mergeT3Script(doc *object) (bool, error) {
	script := newObject()
	script.set("name", t3ScriptName)
	script.set("command", ReadyCommand)
	script.set("icon", "configure")
	script.set("runOnWorktreeCreate", true)
	script.set("async", false)

	scripts, err := arrayAt(doc, "scripts", "scripts")
	if err != nil {
		return false, err
	}
	for i, s := range scripts {
		if s, ok := s.(*object); ok && s.vals["name"] == t3ScriptName {
			if equalJSON(s, script) {
				return false, nil
			}
			scripts[i] = script
			return true, nil
		}
	}
	doc.set("scripts", append(scripts, script))
	return true, nil
}

// otherWorktreeCreateScripts returns the quoted names of scripts other than
// Laneway's that run on worktree creation.
func otherWorktreeCreateScripts(doc *object) []string {
	scripts, _ := arrayAt(doc, "scripts", "scripts")
	var names []string
	for _, s := range scripts {
		s, ok := s.(*object)
		if !ok || s.vals["name"] == t3ScriptName || s.vals["runOnWorktreeCreate"] != true {
			continue
		}
		name, _ := s.vals["name"].(string)
		names = append(names, fmt.Sprintf("%q", name))
	}
	return names
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
