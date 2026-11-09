package plugin

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lanewayhq/laneway/internal/recipe"
)

// Lifecycle events a Plugin can hook (see sdk.Event…).
const (
	EventLink                 = "link"
	EventUnlink               = "unlink"
	EventPreparing            = "preparing"
	EventReady                = "ready"
	EventWorktreeRemoved      = "worktree_removed"
	EventPrimaryDomainChanged = "primary_domain_changed"
	EventProcessStarted       = "process_started"
	EventProcessStopped       = "process_stopped"
	EventProcessCrashed       = "process_crashed"
	EventIdle                 = "idle"
	EventWoken                = "woken"
	EventServiceStarted       = "service_started"
	EventServiceStopped       = "service_stopped"
)

// Events are the events a Plugin can hook, in lifecycle order, each with
// its name as the developer is shown it. To add one, add it here and fire
// it from the daemon with Host.Hooking and Host.Hook.
var Events = []struct{ Name, Title string }{
	{EventLink, "Link"},
	{EventUnlink, "Unlink"},
	{EventPreparing, "Preparing"},
	{EventReady, "Ready"},
	{EventWorktreeRemoved, "Worktree removed"},
	{EventPrimaryDomainChanged, "Primary Domain changed"},
	{EventProcessStarted, "Process started"},
	{EventProcessStopped, "Process stopped"},
	{EventProcessCrashed, "Process crashed"},
	{EventIdle, "Idle"},
	{EventWoken, "Woken"},
	{EventServiceStarted, "Service started"},
	{EventServiceStopped, "Service stopped"},
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

// Event is a lifecycle event as a hook is told it (see sdk.Event). Which of
// Worktree, Process and Service are set depends on the event.
type Event struct {
	Name     string         `json:"name"`
	Project  EventProject   `json:"project"`
	Worktree *EventWorktree `json:"worktree,omitempty"`
	Process  *EventProcess  `json:"process,omitempty"`
	Service  *EventService  `json:"service,omitempty"`
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
	// PreviousDomain is the Primary Domain it had before, for Primary
	// Domain changed.
	PreviousDomain string `json:"previous_domain,omitempty"`
}

// EventProcess is the Process a Process event happened to.
type EventProcess struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Port    int    `json:"port,omitempty"`
	// Exit is how it exited, for Process crashed, such as "exit status 1".
	Exit string `json:"exit,omitempty"`
	// Failed is true, for Process crashed, when it has crashed too often
	// and won't be restarted.
	Failed bool `json:"failed,omitempty"`
}

// EventService is the Service a Service event happened to.
type EventService struct {
	Name    string `json:"name"`
	Engine  string `json:"engine"`
	Version string `json:"version"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
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
// its hooks fire there (see UsedBy, which also decides whether the Plugin
// may be removed).
func (p Plugin) usedBy(res recipe.Resolved) bool {
	return len(p.UsedBy(res)) > 0
}

// HookSource names p's hook for event in errors and warnings, such as
// "Plugin notify's Ready hook".
func HookSource(p Plugin, event string) string {
	return fmt.Sprintf("Plugin %s's %s hook", p.Name, EventTitle(event))
}

// Hook runs p's hook for ev, with the directory dir mounted as the Project,
// and returns the effects it asks for, in order. Like Command, it doesn't
// check them against the manifest (see Plugin.CheckEffects). Unlike a
// Recipe's, a hook's call is never reused: every event runs it.
func (h *Host) Hook(p Plugin, ev Event, dir string) ([]Effect, error) {
	var res effectsResponse
	if err := h.callFresh(p, HookSource(p, ev.Name), request{Call: callHook, Name: ev.Name, Event: &ev}, dir, &res); err != nil {
		return nil, err
	}
	return res.Effects, nil
}

// Hooked reports whether any installed Plugin hooks event, wherever it is
// used.
func (h *Host) Hooked(event string) bool {
	return slices.ContainsFunc(h.List(), func(p Plugin) bool { return slices.Contains(p.Hooks, event) })
}
