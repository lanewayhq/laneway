// Package api defines the daemon's JSON API: the request and response bodies
// shared by the daemon, the CLI and the Dashboard (package dashboard, which
// serves these routes under /api). Changing a field here changes the wire
// contract.
//
// Routes (all JSON, served over the daemon's Unix socket):
//
//	GET  /v1/projects   -> ProjectList
//	GET  /v1/events     -> server-sent events: "projects" (data = ProjectList),
//	                    "services" (ServiceList) and "orphans" (OrphanList) at
//	                    once and whenever one changes (the Dashboard's live view)
//	POST /v1/projects   LinkRequest -> LinkResponse   (Link a repository and apply
//	                    its Env Patch; with preview, only report the Env Patch)
//	GET  /v1/worktree?dir=<abs path> -> WorktreeInfo  (the Worktree containing dir)
//	GET  /v1/info?dir=<abs path> -> Info  (everything about the Worktree containing
//	                    dir: the stable contract `lane info --json` prints)
//	GET  /v1/daemon     -> DaemonInfo
//	GET  /v1/logs?dir=&process=&follow=  -> stream of LogLine, one JSON object per line
//	POST /v1/worktree/up       ControlRequest -> Worktree  (start it, or one Process)
//	POST /v1/worktree/down     ControlRequest -> Worktree  (stop it, or one Process)
//	POST /v1/worktree/restart  ControlRequest -> Worktree
//	POST /v1/worktree/ready    ReadyRequest -> WorktreeInfo  (find the Worktree containing
//	                    dir now, begin or retry its setup and, with wait, wait for it)
//	POST   /v1/aliases  AliasRequest -> AliasResponse  (add an Alias to the Worktree containing dir)
//	DELETE /v1/aliases/{name}?dir=<abs path> -> AliasResponse  (remove one)
//	GET  /v1/services   -> ServiceList
//	POST /v1/services/up    ServiceRequest -> ServiceList  (start one Service, or every one)
//	POST /v1/services/down  ServiceRequest -> ServiceList  (stop one Service, or every one)
//	GET  /v1/orphans    -> OrphanList  (databases of removed Worktrees and unlinked
//	                    Projects, waiting out their grace period)
//	DELETE /v1/projects?dir=<abs path> -> UnlinkResponse  (Unlink the Project containing dir:
//	                    stop it, turn its databases into Orphans, forget its Domains)
//	POST /v1/gc         GCRequest -> GCResponse  (drop Orphans and leftover parallel test
//	                    copies now; with preview, only list them)
//
// GET /v1/logs acts on the Worktree containing dir. Without process it
// returns every Process's output, interleaved in the order it was written.
// It sends the kept recent output, then with follow=true keeps the response
// open and sends new lines as they are written.
//
// Errors are returned with a non-2xx status and an ErrorResponse body.
package api

import "time"

// Project is a Linked git repository.
type Project struct {
	Name string `json:"name"`
	// Path is the main Worktree's directory.
	Path string `json:"path"`
	// Worktrees are the Project's Worktrees, the main one first.
	Worktrees []Worktree `json:"worktrees"`
	// Warnings are problems with the whole Project, such as Catalog
	// Plugins it needs that Laneway couldn't fetch when it started. Each of
	// its Worktrees' setup warnings has them too. Only GET /v1/projects and
	// GET /v1/events fill them in.
	Warnings []string `json:"warnings,omitempty"`
	// PluginItems are what Plugins the Project uses show on it; only
	// GET /v1/projects and GET /v1/events fill them in.
	PluginItems
}

// Process is one of a Worktree's Processes and what it is doing.
type Process struct {
	Name string `json:"name"`
	// State is "running", "restarting" (it crashed and is waiting out its
	// backoff), "failed" (it kept crashing and won't be restarted) or
	// "stopped".
	State string `json:"state"`
}

// Worktree is one checkout of a Project.
type Worktree struct {
	// Name is git's name for a linked worktree; empty for the main Worktree.
	Name string `json:"name"`
	Path string `json:"path"`
	Main bool   `json:"main"`
	// Domain is the hostname the Worktree is served at.
	Domain string `json:"domain,omitempty"`
	// PrimaryDomain is the Domain Laneway gives out for the Worktree: its
	// newest Alias, or Domain if it has none. Empty with Domain.
	PrimaryDomain string `json:"primaryDomain,omitempty"`
	// State is one of the Worktree* run states. Only GET /v1/projects
	// and the up/down/restart routes fill it in.
	State string `json:"state,omitempty"`
	// Aliases are the Worktree's extra Domains, sorted.
	Aliases []string `json:"aliases,omitempty"`
	// Processes are the Worktree's running Processes, sorted by name.
	// Only GET /v1/projects and the up/down/restart routes fill them in.
	Processes []Process `json:"processes,omitempty"`
	// Declared are the Processes the Worktree's config declares, sorted by
	// name, whether they run or not (Processes has the ones that run).
	// Only GET /v1/projects and GET /v1/events fill them in; empty if the
	// config can't be read.
	Declared []DeclaredProcess `json:"declared,omitempty"`
	// Setup is where the Worktree is in setup. GET /v1/projects,
	// GET /v1/worktree, POST /v1/worktree/ready and the up/down/restart
	// routes fill it in.
	Setup *Setup `json:"setup,omitempty"`
	// PluginItems are what Plugins the Worktree uses show on it; only
	// GET /v1/projects and GET /v1/events fill them in.
	PluginItems
}

// DeclaredProcess is a Process as the Worktree's config declares it.
type DeclaredProcess struct {
	Name string `json:"name"`
	// StartMode is "lazy", "always" or "manual".
	StartMode string `json:"startMode"`
	Primary   bool   `json:"primary,omitempty"`
	// HTTP Processes other than the Primary one are served at
	// <name>.<PrimaryDomain> (and under the Worktree's other names).
	HTTP bool `json:"http,omitempty"`
}

// Worktree run states: whether a Worktree's Processes run.
const (
	// WorktreeStopped: nothing has started it, its last start failed, or
	// `lane down` stopped it.
	WorktreeStopped  = "stopped"
	WorktreeStarting = "starting"
	WorktreeRunning  = "running"
	// WorktreeIdle: it had no traffic for its idle timeout, so its lazy
	// Processes were stopped; the next request starts them.
	WorktreeIdle = "idle"
)

// Setup states.
const (
	SetupDiscovered = "discovered"
	SetupPreparing  = "preparing"
	SetupReady      = "ready"
	SetupFailed     = "failed"
)

// Setup is how far a Worktree is from Ready: discovered → preparing → ready or
// failed.
type Setup struct {
	// State is one of the Setup* constants.
	State string `json:"state"`
	// Step is the step running (preparing) or the step that failed
	// (failed), such as "env" or "on_create: composer install".
	Step string `json:"step,omitempty"`
	// Error is why Step failed.
	Error string `json:"error,omitempty"`
	// Output is the last lines the failed step printed.
	Output []string `json:"output,omitempty"`
	// Warnings are problems setup worked around, such as a database
	// created empty because the main Worktree had none to clone.
	Warnings []string `json:"warnings,omitempty"`
}

// ReadyRequest is the body of POST /v1/worktree/ready.
type ReadyRequest struct {
	// Dir is an absolute path inside the Worktree. A Worktree git has but
	// the Worktree Watcher hasn't reported yet is discovered at once.
	Dir string `json:"dir"`
	// Worktree names the Worktree instead of Dir, as in ControlRequest.
	Worktree string `json:"worktree,omitempty"`
	// Retry runs a failed setup again.
	Retry bool `json:"retry,omitempty"`
	// Wait holds the response until the Worktree is Ready or its setup has
	// failed. Without it, the current state is returned at once.
	Wait bool `json:"wait,omitempty"`
}

// ControlRequest is the body of POST /v1/worktree/up, /down and /restart.
type ControlRequest struct {
	// Dir is an absolute path inside the Linked Project to act on.
	Dir string `json:"dir"`
	// Worktree picks the Worktree to act on: git's name for one of the
	// Project's Worktrees (the Project's name for its main Worktree), a
	// Domain or Alias of any Project, or a path; empty means the Worktree
	// containing Dir. GET and DELETE routes that act on a Worktree take the
	// same as the worktree query parameter.
	Worktree string `json:"worktree,omitempty"`
	// Process names one Process; empty means the whole Worktree.
	Process string `json:"process,omitempty"`
}

// ProjectList is the body of GET /v1/projects.
type ProjectList struct {
	Projects []Project `json:"projects"`
}

// LinkRequest is the body of POST /v1/projects.
type LinkRequest struct {
	// Dir is an absolute path anywhere inside the repository to Link,
	// including inside one of its linked worktrees.
	Dir string `json:"dir"`
	// Name overrides the Project name from laneway.toml or the directory name.
	Name string `json:"name,omitempty"`
	// Preview asks what Linking would do to the Worktrees' env files
	// (LinkResponse.EnvPatch) without Linking or changing anything, so it
	// can be confirmed.
	Preview bool `json:"preview,omitempty"`
}

// LinkResponse is the body returned after Linking.
type LinkResponse struct {
	Project Project `json:"project"`
	// Worktree is the Worktree that contains LinkRequest.Dir.
	Worktree Worktree `json:"worktree"`
	// AlreadyLinked is true when the repository was already a Project.
	AlreadyLinked bool `json:"alreadyLinked"`
	// EnvPatch lists the env files that were (or, with Preview, would be)
	// changed, one per Worktree whose Env Patch wasn't already in place.
	EnvPatch []EnvFilePatch `json:"envPatch,omitempty"`
	// Fetched are the Plugins fetched from the Catalog and installed
	// because the Project needs them (even when previewing, since the Env
	// Patch can't be worked out without them).
	Fetched []Plugin `json:"fetched,omitempty"`
	// Warnings are problems that didn't stop Linking, such as a Catalog
	// Laneway refused.
	Warnings []string `json:"warnings,omitempty"`
}

// EnvFilePatch is how the Env Patch changes one Worktree's env file. Only the
// keys Laneway owns ever change.
type EnvFilePatch struct {
	Worktree Worktree `json:"worktree"`
	// Path is the env file, such as <worktree>/.env.
	Path string `json:"path"`
	// Create is true when the file doesn't exist yet. It is created as a
	// copy of CopyFrom (the main Worktree's .env, or .env.example), or with
	// only Laneway's keys when CopyFrom is empty.
	Create   bool   `json:"create,omitempty"`
	CopyFrom string `json:"copyFrom,omitempty"`
	// Backup is where the original is copied before its first patch.
	Backup  string      `json:"backup,omitempty"`
	Changes []EnvChange `json:"changes"`
}

// EnvChange is one key Laneway owns changing value.
type EnvChange struct {
	Key string `json:"key"`
	// Old is "" when Added.
	Old string `json:"old,omitempty"`
	New string `json:"new"`
	// Added is true when the key wasn't in the file.
	Added bool `json:"added,omitempty"`
}

// WorktreeInfo is the body of GET /v1/worktree: the Worktree containing a
// directory, and where it is served.
type WorktreeInfo struct {
	Project  Project  `json:"project"`
	Worktree Worktree `json:"worktree"`
	// Domain is where the Worktree is served, such as "shop.localhost". It
	// is empty if the Worktree has no Domain.
	Domain string `json:"domain"`
	// URL is the HTTPS URL of the Worktree's Primary Domain (its newest
	// Alias, else Domain), empty with Domain.
	URL string `json:"url"`
	// Database is the Worktree's database and Test Database; nil if the
	// Project has no database, the Worktree has no Domain yet or its config
	// can't be read.
	Database *Database `json:"database,omitempty"`
	// ExportEnv is true when the Project's stack reads its settings from
	// the environment rather than its .env (such as Rails without the
	// dotenv gem), so `lane exec` exports the Worktree's .env into the
	// command's.
	ExportEnv bool `json:"exportEnv,omitempty"`
}

// Database names the databases a Worktree uses.
type Database struct {
	// Engine is the database engine, such as "sqlite" or "postgres".
	Engine string `json:"engine"`
	// Name is the database the Worktree uses (the Env Patch's {db}): for
	// SQLite the file's absolute path, else the database's name in its
	// Service. In shared mode it is the main Worktree's.
	Name string `json:"name"`
	// Test is the Worktree's own Test Database (the Env Patch's
	// {test_db}), in the same Service; empty when Test Databases are off.
	Test string `json:"test,omitempty"`
}

// AliasRequest is the body of POST /v1/aliases.
type AliasRequest struct {
	// Dir is an absolute path inside the Worktree to add the Alias to.
	Dir string `json:"dir"`
	// Worktree names the Worktree instead of Dir, as in ControlRequest.
	Worktree string `json:"worktree,omitempty"`
	// Name is the Alias's part of the Domain: "api-v2" adds
	// api-v2.<project>.localhost.
	Name string `json:"name"`
}

// AliasResponse is the body returned after adding or removing an Alias.
type AliasResponse struct {
	// Domain is the Alias's Domain, such as "api-v2.shop.localhost".
	Domain string `json:"domain"`
	// Worktree is the Worktree afterwards.
	Worktree WorktreeInfo `json:"worktree"`
	// Moved is true if the change moved the Worktree's Primary Domain, so
	// its URL is now Worktree.URL.
	Moved bool `json:"moved,omitempty"`
	// Existed is true when adding an Alias the Worktree already had, which
	// changes nothing: an older Alias stays older.
	Existed bool `json:"existed,omitempty"`
}

// DaemonInfo is the body of GET /v1/daemon.
type DaemonInfo struct {
	PID int `json:"pid"`
	// ProxyAddr is the address the HTTPS proxy is listening on.
	ProxyAddr string `json:"proxyAddr"`
	// HTTPAddr is the address plain HTTP is redirected to HTTPS from; ""
	// if the daemon couldn't bind it.
	HTTPAddr string `json:"httpAddr,omitempty"`
}

// LogLine is one line of a Process's output, from GET /v1/logs.
type LogLine struct {
	Process string    `json:"process"`
	Time    time.Time `json:"time"`
	Text    string    `json:"text"`
}

// Error codes returned in ErrorResponse.
const (
	CodeBadRequest     = "bad_request"
	CodeNameTaken      = "name_taken"
	CodeNotGitRepo     = "not_git_repo"
	CodeNotLinked      = "not_linked"
	CodeUnknownProcess = "unknown_process"
	CodeInvalidAlias   = "invalid_alias"
	CodeAliasTaken     = "alias_taken"
	CodeUnknownAlias   = "unknown_alias"
	CodeInternal       = "internal"
	// CodeStartFailed: `lane up` or `restart` started the Worktree, but its
	// Primary Process exited or never accepted connections.
	CodeStartFailed = "start_failed"

	// CodeInvalidConfig: laneway.toml or laneway.local.toml can't be used.
	// The message names the file and the key.
	CodeInvalidConfig = "invalid_config"

	// CodeUnknownWorktree: git doesn't list the directory's worktree (yet),
	// or --worktree names none of the Project's Worktrees.
	CodeUnknownWorktree = "unknown_worktree"
	// CodeShuttingDown: the daemon stopped while the request was waiting.
	CodeShuttingDown = "shutting_down"
	// CodeForbidden: a request that changes state reached the Dashboard's
	// API from somewhere other than the Dashboard's own pages.
	CodeForbidden = "forbidden"

	// CodeUnknownService: no Service has that name; the message lists them.
	CodeUnknownService = "unknown_service"
	// CodeServiceFailed: a Service could not be started or stopped, such
	// as when no container engine is available.
	CodeServiceFailed = "service_failed"

	// CodeInvalidPlugin: a Plugin can't be added; the message says why.
	CodeInvalidPlugin = "invalid_plugin"

	// CodeUnknownPlugin: no installed Plugin has the name.
	CodeUnknownPlugin = "unknown_plugin"
	// CodeUnknownCommand: the Plugin has no command of the name.
	CodeUnknownCommand = "unknown_command"
	// CodePluginInUse: a Plugin can't be removed while Linked Projects use
	// it; the message lists them.
	CodePluginInUse = "plugin_in_use"

	// CodePluginsUnavailable: Linking needs Catalog Plugins that couldn't
	// be fetched, such as when offline; the message names them.
	CodePluginsUnavailable = "plugins_unavailable"

	// CodeSourceChanged: a Plugin's source (such as its GitHub release)
	// has something other than the preview the developer confirmed, so
	// nothing was installed.
	CodeSourceChanged = "source_changed"
)

// Service is a shared Service, such as redis:7, and how to connect to it.
type Service struct {
	// Name is "<engine>:<version>".
	Name    string `json:"name"`
	Engine  string `json:"engine"`
	Version string `json:"version"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	// User is empty for engines without users (Redis).
	User     string `json:"user,omitempty"`
	Password string `json:"password"`
	// State is "stopped", "starting", "running" or "idle" (stopped after
	// its idle timeout; the next connection starts it).
	State string `json:"state"`
	// Error says why the Service can't be used (Laneway can't listen on
	// its port, or its last start failed).
	Error string `json:"error,omitempty"`
	// PluginItems are what the Plugin that adds the Service's Engine shows
	// on it, such as Mailpit's Inbox link.
	PluginItems
}

// ServiceList is every Service Laneway serves, sorted by name.
type ServiceList struct {
	Services []Service `json:"services"`
}

// ServiceRequest names a Service: "redis:7", or just "redis" if only one
// version of it is served (or the default version is). Empty means every
// Service.
type ServiceRequest struct {
	Name string `json:"name,omitempty"`
}

// Orphan is a database left behind by a removed Worktree or an unlinked
// Project. It is kept for a grace period (24 hours), then dropped; `lane
// gc` drops it at once.
type Orphan struct {
	// Database is the database's name in its Service.
	Database string `json:"database"`
	// Service is the database Service it lives in, "<engine>:<version>",
	// such as postgres:18.
	Service string `json:"service"`
	// Project is the name of the Project it belonged to; Worktree is
	// git's name for its Worktree, "" for the main Worktree.
	Project  string `json:"project"`
	Worktree string `json:"worktree,omitempty"`
	// Kind is "database" (the Worktree's database), "test database" (its
	// Test Database) or "parallel test copy" (a copy of its Test Database
	// made by a parallel test run, <test database>_test_<n>).
	Kind string `json:"kind"`
	// Since is when it became an Orphan and Expires when it is dropped.
	// Both are unset for a parallel test copy `lane gc` found that was
	// never recorded as an Orphan.
	Since   time.Time `json:"since,omitzero"`
	Expires time.Time `json:"expires,omitzero"`
}

// OrphanList is every Orphan, oldest first.
type OrphanList struct {
	Orphans []Orphan `json:"orphans"`
}

// UnlinkResponse is the Project that was Unlinked, as it was, and the
// databases of its that became Orphans.
type UnlinkResponse struct {
	Project Project  `json:"project"`
	Orphans []Orphan `json:"orphans"`
}

// GCRequest asks to drop Orphans now, along with the parallel test copies
// of Test Databases whose Worktree no longer exists.
type GCRequest struct {
	// Preview lists what would be dropped and drops nothing.
	Preview bool `json:"preview,omitempty"`
	// Databases limits the drop to these (as a preview listed them); empty
	// means everything a preview would list.
	Databases []OrphanRef `json:"databases,omitempty"`
}

// OrphanRef names an Orphan: a database in a Service.
type OrphanRef struct {
	Service  string `json:"service"`
	Database string `json:"database"`
}

// GCResponse is what was dropped, or with a preview what would be.
type GCResponse struct {
	Orphans []Orphan `json:"orphans"`
	// Errors say what couldn't be dropped (or, for a preview, looked for)
	// and why. Everything else was.
	Errors []string `json:"errors,omitempty"`
}

// ErrorResponse is the body of every non-2xx response.
type ErrorResponse struct {
	Error Error `json:"error"`
}

// Error is a machine-readable code plus a message meant for the user.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Plugin is an installed Plugin, or one about to be added: the body of
// GET /v1/plugins (in a PluginList) and of POST /v1/plugins.
type Plugin struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Source is where it was added from, such as a directory.
	Source string `json:"source"`
	// SHA256 is the hash of its module it is pinned to; "" without one.
	SHA256 string `json:"sha256,omitempty"`
	// Recipes are the Recipes it adds.
	Recipes []PluginRecipe `json:"recipes"`
	// Integrations are the Integrations it adds.
	Integrations []PluginIntegration `json:"integrations"`
	// Commands are the commands it adds, run as `lane <plugin> <command>`.
	Commands []PluginCommand `json:"commands"`
	// Engines are the Engines it adds.
	Engines []PluginEngine `json:"engines"`
	// Generators are the names of the Generators it adds, run with
	// `lane generate <plugin>`.
	Generators []string `json:"generators"`
	// Hooks are the lifecycle events it hooks, such as "ready", in
	// lifecycle order.
	Hooks []string `json:"hooks"`
	// Effects are the kinds of effect it may return, each with what it
	// lets the Plugin do; empty if it only returns config.
	Effects []PluginEffect `json:"effects"`
	// HTTPHosts are the hosts its http effects may reach.
	HTTPHosts []string `json:"httpHosts"`
	// Global is true for a machine-wide Plugin (`lane plugin add
	// --global`), which only personal config turns on.
	Global bool `json:"global,omitempty"`
	// Update is the release `lane plugin update` would update it to, if
	// there is a newer one that version pins allow: in the Catalog, or for
	// a Plugin added by hand, at its source.
	Update *PluginUpdate `json:"update,omitempty"`
}

// PluginRecipe is a Recipe a Plugin adds.
type PluginRecipe struct {
	Name string `json:"name"`
	// Detects describes the Projects its detection rule matches, such as
	// "Procfile exists".
	Detects string `json:"detects"`
}

// PluginIntegration is an Integration a Plugin adds.
type PluginIntegration struct {
	Name string `json:"name"`
	// Detects describes the Projects its detection rule matches, such as
	// "package.json depends on vite".
	Detects string `json:"detects"`
}

// PluginCommand is a command a Plugin adds.
type PluginCommand struct {
	Name string `json:"name"`
	// Summary says what it does.
	Summary string `json:"summary"`
}

// PluginEngine is an Engine a Plugin adds: what Services of it run.
type PluginEngine struct {
	Name string `json:"name"`
	// Image is the image repository its Services' containers run.
	Image string `json:"image"`
	// Port is its standard port, which a Service at its default version
	// listens on.
	Port int `json:"port"`
}

// PluginEffect is a kind of effect a Plugin may return.
type PluginEffect struct {
	Kind string `json:"kind"`
	// Description says what it lets the Plugin do, such as "run commands
	// in Worktrees".
	Description string `json:"description"`
}

// PluginList is every installed Plugin, sorted by name.
type PluginList struct {
	Plugins []Plugin `json:"plugins"`
}

// FetchPluginsRequest is the body of POST /v1/plugins/fetch: install from
// the Catalog, without Linking anything, the Catalog Plugins the Project in
// Dir needs and doesn't have yet, as Linking it would, or with Name, the
// Catalog Plugin of that name (for `lane generate`), unless it is installed.
type FetchPluginsRequest struct {
	// Dir is a directory in the Project, which must be absolute: its
	// config's version pins apply.
	Dir  string `json:"dir"`
	Name string `json:"name,omitempty"`
	// Command is the command asking, such as `lane doctor`: what Laneway
	// couldn't fetch says to run it again once Laneway can reach the
	// Catalog. "" is `lane link`.
	Command string `json:"command,omitempty"`
}

// FetchPluginsResponse is what POST /v1/plugins/fetch fetched.
type FetchPluginsResponse struct {
	// Fetched are the Plugins installed from the Catalog.
	Fetched []Plugin `json:"fetched,omitempty"`
	// Warnings are problems that didn't stop it, such as a Catalog Laneway
	// refused.
	Warnings []string `json:"warnings,omitempty"`
}

// AddPluginRequest is the body of POST /v1/plugins: add the Plugin in a
// directory.
type AddPluginRequest struct {
	// Source is the Plugin's directory, an absolute path, or its GitHub
	// repository, as github.com/<owner>/<repo>[@<tag>], whose release is
	// downloaded.
	Source string `json:"source"`
	// Preview only reads and checks the Plugin, adding nothing, so the
	// developer can confirm what it may do first.
	Preview bool `json:"preview,omitempty"`
	// Global adds it as a machine-wide Plugin, which only the global
	// config or a Project's laneway.local.toml turns on.
	Global bool `json:"global,omitempty"`
	// Digest, required unless previewing, is the Digest of the preview the
	// developer confirmed: if the source has anything else now, such as a
	// GitHub release replaced since, nothing is added (CodeSourceChanged).
	Digest string `json:"digest,omitempty"`
}

// AddPluginResponse is the Plugin added (or, previewing, that would be).
type AddPluginResponse struct {
	Plugin Plugin `json:"plugin"`
	// Replaces is the installed Plugin of the same name it replaced (or
	// would replace), if any.
	Replaces *Plugin `json:"replaces,omitempty"`
	// Digest, previewing, hashes what was read from the source (its
	// manifest, Engine files and module), for confirming it.
	Digest string `json:"digest,omitempty"`
}

// UpdateHandAddedRequest is the body of POST /v1/plugins/{plugin}/update:
// install what the source of a Plugin added by hand (a directory or GitHub
// repository) has now, such as the repository's latest release. Catalog
// Plugins are updated with POST /v1/plugins/update instead.
type UpdateHandAddedRequest struct {
	// Preview only reads and checks what's there, changing nothing, so the
	// developer can confirm it first.
	Preview bool `json:"preview,omitempty"`
	// Digest, required unless previewing, is the Digest of the preview the
	// developer confirmed, as for AddPluginRequest.
	Digest string `json:"digest,omitempty"`
}

// UpdateHandAddedResponse is the Plugin as updated (or, previewing, as it
// would be), and as it was.
type UpdateHandAddedResponse struct {
	Plugin Plugin `json:"plugin"`
	Was    Plugin `json:"was"`
	// UpToDate is true when the source has the version and hash already
	// installed, so nothing changes.
	UpToDate bool `json:"upToDate,omitempty"`
	// Digest, previewing, is as for AddPluginResponse.
	Digest string `json:"digest,omitempty"`
	// Warning, if set, is why nothing changes although the source has
	// something new, such as a version pin that doesn't allow it.
	Warning string `json:"warning,omitempty"`
	// OutOfDate are the generated files the update left out of date, as
	// for UpdatePluginsResponse.
	OutOfDate []OutOfDateFile `json:"outOfDate,omitempty"`
}

// RemovePluginResponse is the body of DELETE /v1/plugins/{plugin}: the
// Plugin removed.
type RemovePluginResponse struct {
	Plugin Plugin `json:"plugin"`
	// Kept are the Services of the Plugin's Engines, stopped but with their
	// data kept: adding the Plugin again brings them back as they were.
	Kept []KeptService `json:"kept"`
}

// KeptService is a Service whose Engine's Plugin was removed, and the
// volume its data is kept in.
type KeptService struct {
	Name   string `json:"name"`
	Volume string `json:"volume"`
}

// PluginCommandRequest is the body of POST
// /v1/plugins/{plugin}/commands/{command}: run a Plugin's command in a
// Worktree and apply the effects it returns.
type PluginCommandRequest struct {
	// Dir is a directory inside the Worktree, an absolute path.
	Dir string `json:"dir"`
	// Worktree, if set, names the Worktree instead, as for ControlRequest.
	Worktree string `json:"worktree,omitempty"`
	// Args are the command's arguments.
	Args []string `json:"args,omitempty"`
}

// PluginCommandResult is what a Plugin's command did.
type PluginCommandResult struct {
	// Effects are the effects applied, in order, with what each did.
	Effects []AppliedEffect `json:"effects"`
	// Warnings are the command's warnings, and the effects Laneway refused
	// and why.
	Warnings []string `json:"warnings"`
	// Error, if set, is why the command failed: its module failed, or an
	// effect did, and the effects after it weren't applied.
	Error string `json:"error,omitempty"`
}

// AppliedEffect is an effect a Plugin's command returned that Laneway
// applied.
type AppliedEffect struct {
	Kind string `json:"kind"`
	// Command is an exec effect's command and arguments.
	Command []string `json:"command,omitempty"`
	// Output is an exec effect's output, stdout and stderr together.
	Output string `json:"output,omitempty"`
}
