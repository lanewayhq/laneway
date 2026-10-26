// Package api defines the daemon's JSON API: the request and response bodies
// shared by the daemon, the CLI and the Dashboard (package dashboard, which
// serves these routes under /api). Changing a field here changes the wire
// contract.
//
// Routes (all JSON, served over the daemon's Unix socket):
//
//	GET  /v1/projects   -> ProjectList
//	GET  /v1/events     -> server-sent events: "projects" (data = ProjectList) at
//	                    once and whenever it changes (the Dashboard's live view)
//	POST /v1/projects   LinkRequest -> LinkResponse   (Link a repository and apply
//	                    its Env Patch; with preview, only report the Env Patch)
//	GET  /v1/worktree?dir=<abs path> -> WorktreeInfo  (the Worktree containing dir)
//	GET  /v1/info?dir=<abs path> -> Info  (everything about the Worktree containing
//	                    dir: the stable contract `portby info --json` prints)
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
	// State is "stopped" (nothing has started it, or `portby down`
	// stopped it), "starting", "running" or "idle" (it had no traffic
	// for its idle timeout, so its lazy Processes were stopped). Only
	// GET /v1/projects and the up/down/restart routes fill it in.
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
}

// DeclaredProcess is a Process as the Worktree's config declares it.
type DeclaredProcess struct {
	Name string `json:"name"`
	// StartMode is "lazy", "always" or "manual".
	StartMode string `json:"startMode"`
	Primary   bool   `json:"primary,omitempty"`
	// HTTP Processes other than the Primary one are served at
	// <name>.<domain>.
	HTTP bool `json:"http,omitempty"`
}

// Setup states.
const (
	SetupDiscovered   = "discovered"
	SetupProvisioning = "provisioning"
	SetupReady        = "ready"
	SetupFailed       = "failed"
)

// Setup is a Worktree's setup state: discovered → provisioning → ready or
// failed.
type Setup struct {
	// State is one of the Setup* constants.
	State string `json:"state"`
	// Step is the step running (provisioning) or the step that failed
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
	// Worktree picks one of the Project's Worktrees by git's name, its
	// Domain or its path; empty means the Worktree containing Dir.
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
	// Name overrides the Project name from portby.toml or the directory name.
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
}

// EnvFilePatch is how the Env Patch changes one Worktree's env file. Only the
// keys Portby owns ever change.
type EnvFilePatch struct {
	Worktree Worktree `json:"worktree"`
	// Path is the env file, such as <worktree>/.env.
	Path string `json:"path"`
	// Create is true when the file doesn't exist yet. It is created as a
	// copy of CopyFrom (the main Worktree's .env, or .env.example), or with
	// only Portby's keys when CopyFrom is empty.
	Create   bool   `json:"create,omitempty"`
	CopyFrom string `json:"copyFrom,omitempty"`
	// Backup is where the original is copied before its first patch.
	Backup  string      `json:"backup,omitempty"`
	Changes []EnvChange `json:"changes"`
}

// EnvChange is one key Portby owns changing value.
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
	// URL is the Domain's HTTPS URL, empty with Domain.
	URL string `json:"url"`
	// Database is the Worktree's database and Test Database; nil if the
	// Project has no database, the Worktree has no Domain yet or its config
	// can't be read.
	Database *Database `json:"database,omitempty"`
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
}

// DaemonInfo is the body of GET /v1/daemon.
type DaemonInfo struct {
	PID int `json:"pid"`
	// ProxyAddr is the address the HTTPS proxy is listening on.
	ProxyAddr string `json:"proxyAddr"`
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
	// CodeStartFailed: `portby up` or `restart` started the Worktree, but its
	// Primary Process exited or never accepted connections.
	CodeStartFailed = "start_failed"

	// CodeInvalidConfig: portby.toml or portby.local.toml can't be used.
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
	// Error says why the Service can't be used (Portby can't listen on
	// its port, or its last start failed).
	Error string `json:"error,omitempty"`
}

// ServiceList is every Service Portby serves, sorted by name.
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
// Project. It is kept for a grace period (24 hours), then dropped; `portby
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
	// Both are unset for a parallel test copy `portby gc` found that was
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
