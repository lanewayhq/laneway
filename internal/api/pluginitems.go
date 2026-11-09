package api

// PluginItems are the links, badges and actions Plugins show on the
// Dashboard for one Project, Worktree or Service, as their manifests declare
// them (spec #48). Plugins supply no UI of their own: the Dashboard draws
// these. GET /v1/projects, GET /v1/services and GET /v1/events fill them in.
type PluginItems struct {
	Links   []PluginLink   `json:"links,omitempty"`
	Badges  []PluginBadge  `json:"badges,omitempty"`
	Actions []PluginAction `json:"actions,omitempty"`
}

// PluginLink is a link a Plugin shows, such as Mailpit's Inbox.
type PluginLink struct {
	// Plugin is the Plugin that declares it.
	Plugin string `json:"plugin"`
	Label  string `json:"label"`
	// URL is where it leads, an http or https URL.
	URL string `json:"url"`
}

// PluginBadge is a short label a Plugin shows.
type PluginBadge struct {
	// Plugin is the Plugin that declares it.
	Plugin string `json:"plugin"`
	Text   string `json:"text"`
	// Title says more, shown when the badge is pointed at; may be empty.
	Title string `json:"title,omitempty"`
}

// PluginAction is a button that runs one of a Plugin's commands, as
// POST /v1/plugins/{plugin}/commands/{command} with Args, in the Worktree it
// is shown on (the main Worktree for a Project's).
type PluginAction struct {
	// Plugin is the Plugin that declares it, and whose command it runs.
	Plugin  string   `json:"plugin"`
	Label   string   `json:"label"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}
