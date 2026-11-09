package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
)

// callCommand runs one of a Plugin's commands (see sdk.CallCommand).
const callCommand = "command"

// CommandDecl is a command a Plugin adds, run as `lane <plugin> <name>
// [args...]` in the current Worktree. Its module answers sdk.CallCommand for
// the name with effects.
type CommandDecl struct {
	Name string `json:"name"`
	// Summary says what it does, in `lane <plugin> --help`.
	Summary string `json:"summary"`
}

// validateCommands checks the commands m declares.
func (m Manifest) validateCommands() error {
	seen := map[string]bool{}
	for i, c := range m.Commands {
		if !name.MatchString(c.Name) {
			return fmt.Errorf("commands[%d]: name %q isn't a command name: use lowercase letters, digits and dashes, starting with a letter", i, c.Name)
		}
		if seen[c.Name] {
			return fmt.Errorf("commands[%d]: the Plugin declares command %s twice", i, c.Name)
		}
		seen[c.Name] = true
		if c.Summary == "" {
			return fmt.Errorf("commands[%d] (%s): summary isn't set; say what the command does, for `lane %s --help`", i, c.Name, m.Name)
		}
	}
	return nil
}

// commandResponse is a module's answer to a command call.
type commandResponse struct {
	Effects []Effect `json:"effects"`
}

// Command runs the installed Plugin's command with args in the Worktree in
// dir, mounted read-only, and returns the effects it asks for, in order. It
// doesn't check them against the manifest: whoever applies them refuses
// those it may not return (see Plugin.CheckEffects).
//
// A command's result isn't reused, unlike a Recipe's: the developer runs it
// to have it run.
func (h *Host) Command(p Plugin, command string, args []string, dir string) ([]Effect, error) {
	what := fmt.Sprintf("the command `lane %s %s`", p.Name, command)
	if !p.HasCommand(command) {
		return nil, fmt.Errorf("Plugin %s has no command %s", p.Name, command)
	}
	in, err := json.Marshal(request{APIVersion: APIVersion, Call: callCommand, Name: command, Dir: projectMount, Args: args})
	if err != nil {
		return nil, err
	}
	out, err := h.runtime.run(p, in, os.DirFS(dir))
	var res commandResponse
	if err := decode(what, out, err, &res); err != nil {
		return nil, err
	}
	return res.Effects, nil
}

// HasCommand reports whether the Plugin adds the command called name.
func (m Manifest) HasCommand(name string) bool {
	return slices.ContainsFunc(m.Commands, func(c CommandDecl) bool { return c.Name == name })
}

// Plugin returns the installed Plugin called name.
func (h *Host) Plugin(name string) (Plugin, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.plugins[name]
	return p, ok
}

// Declares reports whether p's manifest declares the effect kind, so it may
// return effects of that kind.
func (p Plugin) Declares(kind string) bool {
	return slices.Contains(p.Capabilities.Effects, kind)
}
