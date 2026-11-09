package sdk

// CallHook tells a Plugin about a lifecycle event it hooks; Request.Name is
// the event, such as EventReady, and Request.Event describes it.
const CallHook = "hook"

// Events a Plugin can hook, as its manifest's hooks list names them.
const (
	// EventPreparing: a Worktree is being prepared. The hook runs as one
	// of its setup steps, after on_create, and an error or a failed exec
	// effect fails setup there, keeping the Worktree from being Ready.
	EventPreparing = "preparing"
	// EventReady: a Worktree became Ready. The hook runs in the
	// background; its failures become warnings on the Worktree's setup.
	EventReady = "ready"
	// EventWorktreeRemoved: a Worktree was removed. The hook runs in the
	// background with the main Worktree mounted, since the removed one is
	// gone; exec effects run in the main Worktree.
	EventWorktreeRemoved = "worktree_removed"
)

// Event describes a lifecycle event to a hook.
type Event struct {
	// Name is the event, such as EventReady.
	Name string `json:"name"`
	// Project is the Project it happened in.
	Project EventProject `json:"project"`
	// Worktree is the Worktree it happened to.
	Worktree *EventWorktree `json:"worktree,omitempty"`
}

// EventProject is the Project an event happened in.
type EventProject struct {
	// Name is the Project's name, as in its Domain.
	Name string `json:"name"`
	// Path is its main Worktree's directory on the developer's machine.
	Path string `json:"path"`
}

// EventWorktree is the Worktree an event happened to.
type EventWorktree struct {
	// Name is git's name for it; "" for the main Worktree.
	Name string `json:"name"`
	// Path is its directory on the developer's machine. The hook sees it
	// only through the read-only mount (Project.Dir), if at all.
	Path string `json:"path"`
	// Main is true for the Project's main Worktree.
	Main bool `json:"main"`
	// Domain is its Primary Domain, such as "feature.app.localhost".
	Domain string `json:"domain"`
	// URL is its Primary Domain's URL, without a trailing slash.
	URL string `json:"url"`
}

// HookResult is what a hook returns: the effects Laneway applies, in order.
type HookResult struct {
	Effects []Effect `json:"effects"`
}

// Hook answers a CallHook: it returns the effects Laneway applies for the
// event, in order. An error fails a Preparing hook (and so setup), and is a
// warning for any other.
type Hook func(Project, Event) ([]Effect, error)
