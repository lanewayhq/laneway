// Package api defines the daemon's JSON API: the request and response bodies
// shared by the daemon, the CLI and (later) the dashboard. Changing a field
// here changes the wire contract.
//
// Routes (all JSON, served over the daemon's Unix socket):
//
//	GET  /v1/projects   -> ProjectList
//	POST /v1/projects   LinkRequest -> LinkResponse   (Link a repository)
//	GET  /v1/worktree?dir=<abs path> -> WorktreeInfo  (the Worktree containing dir)
//	GET  /v1/daemon     -> DaemonInfo
//	GET  /v1/logs?dir=&process=&follow=  -> stream of LogLine, one JSON object per line
//	POST   /v1/aliases  AliasRequest -> AliasResponse  (add an Alias to the Worktree containing dir)
//	DELETE /v1/aliases/{name}?dir=<abs path> -> AliasResponse  (remove one)
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
	// Aliases are the Worktree's extra Domains, sorted.
	Aliases []string `json:"aliases,omitempty"`
	// Processes are the Worktree's Processes, sorted by name. Only
	// GET /v1/projects fills them in.
	Processes []Process `json:"processes,omitempty"`
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
}

// LinkResponse is the body returned after Linking.
type LinkResponse struct {
	Project Project `json:"project"`
	// Worktree is the Worktree that contains LinkRequest.Dir.
	Worktree Worktree `json:"worktree"`
	// AlreadyLinked is true when the repository was already a Project.
	AlreadyLinked bool `json:"alreadyLinked"`
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

	// CodeInvalidConfig: portby.toml or portby.local.toml can't be used.
	// The message names the file and the key.
	CodeInvalidConfig = "invalid_config"
)

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
