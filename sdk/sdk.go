// Package sdk is the Go SDK for Laneway Plugins: it hides the JSON plumbing
// between Laneway and a Plugin's WASM module (ADR-0007), so a Plugin is its
// handlers and a main that calls Run.
//
// A Plugin's module is a WASI command, built with
//
//	GOOS=wasip1 GOARCH=wasm go build -o plugin.wasm .
//
// Laneway runs it once per call: it writes one JSON request to the module's
// stdin and reads one JSON response from its stdout. The Project's directory
// is mounted read-only and is the working directory, so a handler reads the
// Project's files with relative paths. What the module writes to stderr goes
// to Laneway's logs.
//
// The SDK starts in Laneway's repository and moves to lanewayhq/plugins with
// the first-party Plugins.
package sdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// APIVersion is the Plugin API version this SDK speaks. A Plugin's manifest
// declares the same number as its api_version.
const APIVersion = 1

// Calls a Plugin answers, the "call" of a Request.
const (
	// CallRecipe asks a Recipe for the Project's config layer.
	CallRecipe = "recipe"
)

// Request is what Laneway sends a Plugin's module on stdin.
type Request struct {
	// APIVersion is the Plugin API version Laneway speaks.
	APIVersion int `json:"api_version"`
	// Call is what Laneway asks for, such as CallRecipe.
	Call string `json:"call"`
	// Name names what answers the call, such as the Recipe's name as the
	// manifest declares it.
	Name string `json:"name"`
	// Dir is the Project's directory as the module sees it: mounted
	// read-only, and the module's working directory.
	Dir string `json:"dir"`
	// Event describes the event a CallHook is about.
	Event *Event `json:"event,omitempty"`
}

// Response is what a Plugin's module writes on stdout.
type Response struct {
	// Error, if set, is why the call failed, said for the developer. The
	// rest of the Response is ignored.
	Error string `json:"error,omitempty"`
	// RecipeResult answers a CallRecipe.
	*RecipeResult
	// Effects answer a CallHook: what Laneway does for the Plugin, in
	// order.
	Effects []Effect `json:"effects,omitempty"`
}

// RecipeResult is what a Recipe returns for a Project it detected.
type RecipeResult struct {
	// Layer is the Recipe's config layer, under the Project's laneway.toml
	// and laneway.local.toml.
	Layer Layer `json:"layer"`
	// TestDatabase is whether the Project's tests use a Test Database,
	// deciding database.test = "auto".
	TestDatabase bool `json:"test_database,omitempty"`
	// TestDatabaseWarnings are reasons the Project's tests may bypass their
	// Test Database, each naming the file at fault.
	TestDatabaseWarnings []string `json:"test_database_warnings,omitempty"`
	// Warnings are problems worth telling the developer about that don't
	// stop the Project running, each naming the file at fault.
	Warnings []string `json:"warnings,omitempty"`
}

// Layer is a config layer, keyed like laneway.toml. Empty fields aren't set,
// so the Project's laneway.toml (or Laneway's defaults) decide them.
type Layer struct {
	Name         string             `json:"name,omitempty"`
	Processes    map[string]Process `json:"processes,omitempty"`
	StartTimeout string             `json:"start_timeout,omitempty"`
	IdleTimeout  string             `json:"idle_timeout,omitempty"`
	Env          map[string]string  `json:"env,omitempty"`
	LinkedEnv    map[string]string  `json:"linked_env,omitempty"`
	Services     map[string]string  `json:"services,omitempty"`
	Worktree     *Worktree          `json:"worktree,omitempty"`
	Database     *Database          `json:"database,omitempty"`
}

// Process is one [processes.<name>] table.
type Process struct {
	Command   string `json:"command,omitempty"`
	Primary   bool   `json:"primary,omitempty"`
	StartMode string `json:"start_mode,omitempty"`
	HTTP      bool   `json:"http,omitempty"`
	StaticDir string `json:"static_dir,omitempty"`
}

// Worktree is the [worktree] table.
type Worktree struct {
	Copy     []string `json:"copy,omitempty"`
	OnCreate []string `json:"on_create,omitempty"`
	OnRemove []string `json:"on_remove,omitempty"`
}

// Database is the [database] table.
type Database struct {
	Engine   string `json:"engine,omitempty"`
	Path     string `json:"path,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	Test     string `json:"test,omitempty"`
}

// Project is the Project a call is about.
type Project struct {
	// Dir is its directory, read-only.
	Dir string
}

// ReadFile reads the Project's file name, relative to its directory.
func (p Project) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(p.Dir, name))
}

// Exists reports whether the Project has a file or directory at name.
func (p Project) Exists(name string) bool {
	_, err := os.Stat(filepath.Join(p.Dir, name))
	return err == nil
}

// Recipe answers a CallRecipe for a Project the Recipe's detection rule
// matched.
type Recipe func(Project) (RecipeResult, error)

// Plugin is what a Plugin's module handles, by the names its manifest
// declares.
type Plugin struct {
	Recipes map[string]Recipe
	// Hooks are by event, such as EventReady.
	Hooks map[string]Hook
}

// Run answers the request on stdin and exits: it is a Plugin module's whole
// main.
func Run(p Plugin) {
	if err := Serve(p, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Serve answers the request read from r on w. A handler's error is the
// response's Error; Serve fails only when it can't read the request or write
// the response.
func Serve(p Plugin, r io.Reader, w io.Writer) error {
	var req Request
	if err := json.NewDecoder(r).Decode(&req); err != nil {
		return fmt.Errorf("reading the request: %w", err)
	}
	res, err := handle(p, req)
	if err != nil {
		res = Response{Error: err.Error()}
	}
	return json.NewEncoder(w).Encode(res)
}

func handle(p Plugin, req Request) (Response, error) {
	if req.APIVersion != APIVersion {
		return Response{}, fmt.Errorf("this Plugin speaks Plugin API version %d, but Laneway sent version %d", APIVersion, req.APIVersion)
	}
	project := Project{Dir: req.Dir}
	switch req.Call {
	case CallRecipe:
		recipe, ok := p.Recipes[req.Name]
		if !ok {
			return Response{}, fmt.Errorf("this Plugin has no Recipe %q", req.Name)
		}
		res, err := recipe(project)
		if err != nil {
			return Response{}, err
		}
		return Response{RecipeResult: &res}, nil
	case CallHook:
		hook, ok := p.Hooks[req.Name]
		if !ok || req.Event == nil {
			return Response{}, fmt.Errorf("this Plugin doesn't hook %q", req.Name)
		}
		effects, err := hook(project, *req.Event)
		if err != nil {
			return Response{}, err
		}
		return Response{Effects: effects}, nil
	}
	return Response{}, errors.New("this Plugin doesn't answer " + req.Call + " calls")
}
