package api

// InfoSchemaVersion is Info.SchemaVersion. It only changes when a key is
// removed or renamed, or a value changes type or meaning; adding keys (at any
// level) and adding values to a documented set of states doesn't change it.
const InfoSchemaVersion = 1

// Info is the body of GET /v1/info and exactly what `portby info --json`
// prints: everything about the Worktree containing a directory that agents,
// scripts and the Dashboard need to work with it.
//
// It is a stable contract, separate from the other routes' types so that
// they can change without breaking it (see InfoSchemaVersion). Rules every
// field follows, so consumers never need to check whether a key exists:
// every key is always present; strings are never null ("" means none or not
// known); lists are never null ([] means none); an object that may not
// apply is null.
type Info struct {
	// SchemaVersion is InfoSchemaVersion.
	SchemaVersion int          `json:"schemaVersion"`
	Project       InfoProject  `json:"project"`
	Worktree      InfoWorktree `json:"worktree"`
	// Domain is where the Worktree is served, such as
	// "feature-x.shop.localhost"; "" if Portby hasn't discovered the
	// Worktree yet.
	Domain string `json:"domain"`
	// URL is the Domain's HTTPS URL, such as
	// "https://feature-x.shop.localhost/"; "" with Domain.
	URL string `json:"url"`
	// Aliases are the Worktree's extra Domains, sorted by Domain.
	Aliases []InfoAlias `json:"aliases"`
	// Setup is where the Worktree is in setup; null with Domain.
	Setup *InfoSetup `json:"setup"`
	// State is whether the Worktree's Processes run: "stopped" (nothing
	// has started it, or `portby down` stopped it), "starting", "running"
	// or "idle" (it had no traffic for its idle timeout, so its lazy
	// Processes were stopped; the next request starts them). "" with
	// Domain.
	State string `json:"state"`
	// Processes are the Processes the Worktree's config declares, sorted
	// by name, whether they run or not, then any that still run but are no
	// longer declared.
	Processes []InfoProcess `json:"processes"`
	// Database is the Worktree's database and Test Database; null if the
	// Worktree has no database (or no Domain yet).
	Database *InfoDatabase `json:"database"`
	// Services are the Services the Worktree's config uses (including its
	// database's), sorted by name, with how to connect to them.
	Services []InfoService `json:"services"`
	// ConfigError is why the Worktree's portby.toml or portby.local.toml
	// can't be used, naming the file and key; "" if they can. While it is
	// set, Processes and Services are empty.
	ConfigError string `json:"configError"`
}

// InfoProject is the Linked Project the Worktree belongs to.
type InfoProject struct {
	Name string `json:"name"`
	// Path is the main Worktree's directory.
	Path string `json:"path"`
}

// InfoWorktree is the Worktree itself.
type InfoWorktree struct {
	// Name is git's name for a linked worktree; "" for the main Worktree.
	Name string `json:"name"`
	// Path is the Worktree's root directory.
	Path string `json:"path"`
	Main bool   `json:"main"`
}

// InfoAlias is one of the Worktree's Aliases.
type InfoAlias struct {
	// Domain is the Alias's Domain, such as "api-v2.shop.localhost".
	Domain string `json:"domain"`
	// URL is its HTTPS URL, such as "https://api-v2.shop.localhost/".
	URL string `json:"url"`
}

// InfoSetup is where the Worktree is in setup.
type InfoSetup struct {
	// State is "discovered" (setup hasn't begun), "provisioning", "ready"
	// or "failed".
	State string `json:"state"`
	// Step is the step running (provisioning) or the step that failed
	// (failed), such as "env" or "on_create: composer install"; else "".
	Step string `json:"step"`
	// Error is why Step failed; else "".
	Error string `json:"error"`
	// Output is the last lines (up to 50) the failed step printed.
	Output []string `json:"output"`
	// Warnings are problems setup worked around, such as a database
	// created empty because the main Worktree had none to clone.
	Warnings []string `json:"warnings"`
}

// InfoProcess is one of the Worktree's Processes.
type InfoProcess struct {
	Name string `json:"name"`
	// StartMode is "lazy" (started by the first request), "always"
	// (started once the Worktree is Ready) or "manual" (only by `portby
	// up <name>`); "" for a Process that runs but is no longer declared.
	StartMode string `json:"startMode"`
	// Primary is the Process the Worktree's Domain is served by.
	Primary bool `json:"primary"`
	// HTTP is true for the Processes Portby serves.
	HTTP bool `json:"http"`
	// State is "running", "restarting" (it crashed and is waiting out its
	// backoff), "failed" (it kept crashing and won't be restarted) or
	// "stopped".
	State string `json:"state"`
	// URL is where the Process is served: the Worktree's URL for the
	// Primary Process, https://<name>.<domain>/ for other HTTP Processes,
	// "" for the rest.
	URL string `json:"url"`
}

// InfoDatabase names the databases the Worktree uses.
type InfoDatabase struct {
	// Engine is the database engine, such as "sqlite" or "postgres".
	Engine string `json:"engine"`
	// Name is the database the Worktree uses (the Env Patch's {db}): for
	// SQLite the file's absolute path, else the database's name in
	// Service. In shared mode it is the main Worktree's.
	Name string `json:"name"`
	// Test is the Worktree's own Test Database (the Env Patch's
	// {test_db}), in the same Service; "" when Test Databases are off.
	Test string `json:"test"`
	// Service is the Name of the Service in Services holding the
	// databases, such as "postgres:18"; "" for SQLite.
	Service string `json:"service"`
}

// InfoService is a Service the Worktree uses and how to connect to it.
type InfoService struct {
	// Name is "<engine>:<version>", such as "redis:7".
	Name    string `json:"name"`
	Engine  string `json:"engine"`
	Version string `json:"version"`
	// Host and Port are where clients connect. Portby listens there and
	// starts the Service on the first connection, so connect even when
	// State isn't "running".
	Host string `json:"host"`
	Port int    `json:"port"`
	// User is "" for engines without users (Redis).
	User     string `json:"user"`
	Password string `json:"password"`
	// State is "stopped", "starting", "running" or "idle" (stopped after
	// its idle timeout; the next connection starts it).
	State string `json:"state"`
	// Error says why the Service can't be used (Portby can't listen on
	// its port, or its last start failed); else "".
	Error string `json:"error"`
}
