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
//
// Errors are returned with a non-2xx status and an ErrorResponse body.
package api

// Project is a Linked git repository.
type Project struct {
	Name string `json:"name"`
	// Path is the main Worktree's directory.
	Path string `json:"path"`
}

// Worktree is one checkout of a Project.
type Worktree struct {
	// Name is git's name for a linked worktree; empty for the main Worktree.
	Name string `json:"name"`
	Path string `json:"path"`
	Main bool   `json:"main"`
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

// DaemonInfo is the body of GET /v1/daemon.
type DaemonInfo struct {
	PID int `json:"pid"`
	// ProxyAddr is the address the HTTPS proxy is listening on.
	ProxyAddr string `json:"proxyAddr"`
}

// Error codes returned in ErrorResponse.
const (
	CodeBadRequest = "bad_request"
	CodeNameTaken  = "name_taken"
	CodeNotGitRepo = "not_git_repo"
	CodeNotLinked  = "not_linked"
	CodeInternal   = "internal"
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
