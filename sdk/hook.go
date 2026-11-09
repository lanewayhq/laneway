package sdk

// CallHook tells a Plugin about a lifecycle event it hooks; Request.Name is
// the event, such as EventReady, and Request.Event describes it.
const CallHook = "hook"

// Events a Plugin can hook, as its manifest's hooks list names them. Every
// hook but Preparing's runs in the background: its error, or a failed
// effect, becomes a warning and can't block or fail anything.
const (
	// EventLink: a Project was Linked. Event.Worktree is nil; the main
	// Worktree is mounted, and exec effects run there.
	EventLink = "link"
	// EventUnlink: a Project was Unlinked. Event.Worktree is nil; the main
	// Worktree is mounted, and exec effects run there. The Project has no
	// .env Laneway manages any more, so env effects are skipped.
	EventUnlink = "unlink"
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
	// EventPrimaryDomainChanged: a Worktree's Primary Domain moved, after
	// an Alias was added or removed. Event.Worktree has the new Domain and
	// URL, and PreviousDomain.
	EventPrimaryDomainChanged = "primary_domain_changed"
	// EventProcessStarted: a Worktree's Process started, including after a
	// crash. Event.Process says which.
	EventProcessStarted = "process_started"
	// EventProcessStopped: Laneway stopped a Worktree's Process.
	EventProcessStopped = "process_stopped"
	// EventProcessCrashed: a Worktree's Process exited without being
	// stopped. Event.Process says how (Exit) and whether it won't be
	// restarted (Failed).
	EventProcessCrashed = "process_crashed"
	// EventIdle: a Worktree went Idle, and its lazy Processes stopped.
	EventIdle = "idle"
	// EventWoken: a request started an Idle Worktree again, and its
	// Primary Process is up.
	EventWoken = "woken"
	// EventServiceStarted: a Service the Project uses started. It fires
	// once per Project using it, with Event.Worktree nil and the main
	// Worktree mounted.
	EventServiceStarted = "service_started"
	// EventServiceStopped: a Service the Project uses stopped, by hand or
	// after going Idle. Like EventServiceStarted, once per Project.
	EventServiceStopped = "service_stopped"
)

// Event describes a lifecycle event to a hook.
type Event struct {
	// Name is the event, such as EventReady.
	Name string `json:"name"`
	// Project is the Project it happened in.
	Project EventProject `json:"project"`
	// Worktree is the Worktree it happened to; nil for Link, Unlink and
	// Service events, which happen to the whole Project.
	Worktree *EventWorktree `json:"worktree,omitempty"`
	// Process is the Process a Process event happened to.
	Process *EventProcess `json:"process,omitempty"`
	// Service is the Service a Service event happened to.
	Service *EventService `json:"service,omitempty"`
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
	// PreviousDomain is the Primary Domain it had before, for
	// EventPrimaryDomainChanged.
	PreviousDomain string `json:"previous_domain,omitempty"`
}

// EventProcess is the Process a Process event happened to.
type EventProcess struct {
	// Name is its name in laneway.toml, such as "web".
	Name string `json:"name"`
	// Command is the command it runs, as run, with {port} replaced.
	Command string `json:"command"`
	// Port is the loopback port an HTTP Process was told to listen on; 0
	// for other Processes.
	Port int `json:"port,omitempty"`
	// Exit is how it exited, for EventProcessCrashed, such as
	// "exit status 1".
	Exit string `json:"exit,omitempty"`
	// Failed is true, for EventProcessCrashed, when it has crashed too
	// often to be restarted until the developer starts it again.
	Failed bool `json:"failed,omitempty"`
}

// EventService is the Service a Service event happened to.
type EventService struct {
	// Name is how Laneway names it, such as "redis:7".
	Name string `json:"name"`
	// Engine and Version make up the name.
	Engine  string `json:"engine"`
	Version string `json:"version"`
	// Host and Port are where clients connect to it.
	Host string `json:"host"`
	Port int    `json:"port"`
}

// HookResult is what a hook returns: the effects Laneway applies, in order.
type HookResult struct {
	Effects []Effect `json:"effects"`
}

// Hook answers a CallHook: it returns the effects Laneway applies for the
// event, in order. An error fails a Preparing hook (and so setup), and is a
// warning for any other.
type Hook func(Project, Event) ([]Effect, error)
