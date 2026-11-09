package plugin

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lanewayhq/laneway/internal/recipe"
)

// Lifecycle events a Plugin can hook (see sdk.Event…).
const (
	EventPreparing       = "preparing"
	EventReady           = "ready"
	EventWorktreeRemoved = "worktree_removed"
)

// Events are the events a Plugin can hook, in lifecycle order, each with
// its name as the developer is shown it. To add one, add it here and fire
// it from the daemon with Host.Hooking and Host.Hook.
var Events = []struct{ Name, Title string }{
	{EventPreparing, "Preparing"},
	{EventReady, "Ready"},
	{EventWorktreeRemoved, "Worktree removed"},
}

// EventTitle is the event's name as the developer is shown it, such as
// "Worktree removed".
func EventTitle(event string) string {
	for _, e := range Events {
		if e.Name == event {
			return e.Title
		}
	}
	return event
}

func validateHooks(hooks []string) error {
	seen := map[string]bool{}
	for i, h := range hooks {
		if !slices.ContainsFunc(Events, func(e struct{ Name, Title string }) bool { return e.Name == h }) {
			var names []string
			for _, e := range Events {
				names = append(names, e.Name)
			}
			return fmt.Errorf("hooks[%d]: %q isn't an event a Plugin can hook; use %s", i, h, strings.Join(names, ", "))
		}
		if seen[h] {
			return fmt.Errorf("hooks[%d]: the Plugin hooks %s twice", i, h)
		}
		seen[h] = true
	}
	return nil
}

// Event is a lifecycle event as a hook is told it (see sdk.Event).
type Event struct {
	Name     string         `json:"name"`
	Project  EventProject   `json:"project"`
	Worktree *EventWorktree `json:"worktree,omitempty"`
}

// EventProject is the Project an event happened in.
type EventProject struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// EventWorktree is the Worktree an event happened to.
type EventWorktree struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Main   bool   `json:"main"`
	Domain string `json:"domain"`
	URL    string `json:"url"`
}

// hookResponse is a module's answer to a hook call.
type hookResponse struct {
	Effects []Effect `json:"effects"`
}

// Hooking are the Plugins whose hooks fire for event in the Project whose
// config is res: those the Project uses that hook it, by Plugin name.
func (h *Host) Hooking(event string, res recipe.Resolved) []Plugin {
	var out []Plugin
	for _, p := range h.List() {
		if slices.Contains(p.Hooks, event) && p.usedBy(res) {
			out = append(out, p)
		}
	}
	return out
}

// usedBy reports whether a Project whose config is res uses the Plugin, so
// its hooks fire there: its Recipe or one of its Integrations detected the
// Project. (Recipe and Integration names are unique across Plugins and
// Laneway.) Engines and Plugins named in config should count too, once
// Plugins can add or be named by them.
func (p Plugin) usedBy(res recipe.Resolved) bool {
	return slices.ContainsFunc(p.Recipes, func(r RecipeDecl) bool { return r.Name == res.Recipe }) ||
		slices.ContainsFunc(p.Integrations, func(i IntegrationDecl) bool { return slices.Contains(res.Integrations, i.Name) })
}

// Hook runs p's hook for ev, with the directory dir mounted as the Project,
// and returns the effects to apply, in order. Effects p may not return are
// left out, each with a warning saying why. Unlike a Recipe's, a hook's
// call is never reused: every event runs it.
func (h *Host) Hook(p Plugin, ev Event, dir string) (effects []Effect, warnings []string, err error) {
	what := fmt.Sprintf("Plugin %s's %s hook", p.Name, EventTitle(ev.Name))
	var res hookResponse
	if err := h.callFresh(p, what, request{Call: callHook, Name: ev.Name, Event: &ev}, dir, &res); err != nil {
		return nil, nil, err
	}
	effects, warnings = p.checkEffects(what, res.Effects)
	return effects, warnings, nil
}
