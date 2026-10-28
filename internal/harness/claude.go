package harness

import (
	"encoding/json"
	"strconv"
)

// claudeSettings is Claude Code's shared (committed) project settings file.
const claudeSettings = ".claude/settings.json"

// claudeStatusMessage is the spinner text of the generated SessionStart hook,
// and how Portby recognises its own hook on later runs.
const claudeStatusMessage = "Portby: waiting for this Worktree to be Ready (from `portby harness add claude`)"

// claude adds a SessionStart hook to Claude Code's project settings that
// runs ReadyCommand. SessionStart hooks run on startup, resume, /clear and
// compaction; Claude's first response waits for them, and a hook's stdout
// (the Worktree's URL) becomes part of the agent's context. It also adds the
// agent instructions to AGENTS.md, and imports them into CLAUDE.md.
func claude(root string) ([]edit, error) {
	old, err := readFile(root, claudeSettings)
	if err != nil {
		return nil, err
	}
	data, err := mergeJSON(claudeSettings, old, mergeSessionStartHook)
	if err != nil {
		return nil, err
	}
	instructions, err := instructionEdits(root, true)
	if err != nil {
		return nil, err
	}
	return append([]edit{{path: claudeSettings, old: old, new: data}}, instructions...), nil
}

// mergeSessionStartHook makes settings.hooks.SessionStart hold Portby's hook
// once: updated in place if it's there, else added as a new group at the end.
func mergeSessionStartHook(settings *object) (bool, error) {
	handler := newObject()
	handler.set("type", "command")
	handler.set("command", ReadyCommand)
	handler.set("timeout", json.Number(strconv.Itoa(readyTimeout)))
	handler.set("statusMessage", claudeStatusMessage)

	hooks, err := objectAt(settings, "hooks", "hooks")
	if err != nil {
		return false, err
	}
	groups, err := arrayAt(hooks, "SessionStart", "hooks.SessionStart")
	if err != nil {
		return false, err
	}
	for _, g := range groups {
		group, ok := g.(*object)
		if !ok {
			continue
		}
		handlers, _ := group.vals["hooks"].([]any)
		for i, h := range handlers {
			if h, ok := h.(*object); ok && h.vals["statusMessage"] == claudeStatusMessage {
				if equalJSON(h, handler) {
					return false, nil
				}
				handlers[i] = handler
				return true, nil
			}
		}
	}
	group := newObject()
	group.set("hooks", []any{handler})
	hooks.set("SessionStart", append(groups, group))
	return true, nil
}
