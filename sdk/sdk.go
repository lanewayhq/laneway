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
// `lane plugin test`, run in the Plugin's directory, runs the Plugin against
// the test cases under testdata/<case>/: a fixture Project in project/ and
// sample events for its hooks in events/<name>.json. It compares the layers
// and effects the Plugin returns with the case's golden.json and shows each
// difference as a diff; `lane plugin test --update` rewrites the golden
// files. It needs no daemon.
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
	// CallIntegration asks an Integration for the config layer it adds to
	// the Project.
	CallIntegration = "integration"
)

// Request is what Laneway sends a Plugin's module on stdin.
type Request struct {
	// APIVersion is the Plugin API version Laneway speaks.
	APIVersion int `json:"api_version"`
	// Call is what Laneway asks for, such as CallRecipe.
	Call string `json:"call"`
	// Name names what answers the call, such as the Recipe's or
	// Integration's name as the manifest declares it.
	Name string `json:"name"`
	// Dir is the Project's directory as the module sees it: mounted
	// read-only, and the module's working directory. For a CallCommand it
	// is the Worktree the command was run in.
	Dir string `json:"dir"`
	// HostDir is the Project's directory on the developer's machine, which
	// the module can't open: it is how absolute paths Laneway wrote into
	// the Project's files, such as a SQLite file in .env, are read.
	HostDir string `json:"host_dir,omitempty"`
	// MainDir is the main Worktree's directory as the module sees it,
	// mounted read-only, when the Project's directory is another Worktree,
	// whose uncommitted files (such as .env.testing) setup copies from it;
	// "" otherwise.
	MainDir string `json:"main_dir,omitempty"`
	// Args are a CallCommand's arguments, after the command's name.
	Args []string `json:"args,omitempty"`
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
	// IntegrationResult answers a CallIntegration.
	*IntegrationResult
	// CommandResult answers a CallCommand.
	*CommandResult
	// GeneratorResult answers a CallGenerator.
	*GeneratorResult
	// HookResult answers a CallHook.
	*HookResult
}

// MarshalJSON writes the Error alone, or the one result that is set. (Two
// embedded results with the same fields, such as layer, would otherwise
// cancel each other out.)
func (r Response) MarshalJSON() ([]byte, error) {
	switch {
	case r.Error != "":
		return json.Marshal(struct {
			Error string `json:"error"`
		}{r.Error})
	case r.RecipeResult != nil:
		return json.Marshal(r.RecipeResult)
	case r.IntegrationResult != nil:
		return json.Marshal(r.IntegrationResult)
	case r.CommandResult != nil:
		return json.Marshal(r.CommandResult)
	case r.GeneratorResult != nil:
		return json.Marshal(r.GeneratorResult)
	case r.HookResult != nil:
		return json.Marshal(r.HookResult)
	}
	return []byte("{}"), nil
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
	// TestEnv is the env file the Project's tests read, which Laneway
	// points at the Test Database; nil for .env.testing, replacing .env.
	TestEnv *TestEnv `json:"test_env,omitempty"`
	// ExportEnv says the stack reads its settings from the environment
	// rather than from .env (such as Rails without the dotenv gem), so
	// Laneway exports the Worktree's .env into the commands it runs there:
	// setup steps, hooks' and commands' exec effects, and `lane exec`. Its
	// Processes load .env themselves.
	ExportEnv bool `json:"export_env,omitempty"`
	// Warnings are problems worth telling the developer about that don't
	// stop the Project running, each naming the file at fault.
	Warnings []string `json:"warnings,omitempty"`
}

// TestEnv is the env file a Project's tests read their settings from.
type TestEnv struct {
	// File is its name in the Worktree, such as ".env.test".
	File string `json:"file"`
	// Layered means the tests read File over .env, so a File holding only
	// what Laneway sets is complete and Laneway may create it. Otherwise
	// File replaces .env for the tests, and Laneway only patches one the
	// Worktree has, or a copy of the main Worktree's.
	Layered bool `json:"layered,omitempty"`
}

// IntegrationResult is what an Integration returns for a Project it
// detected.
type IntegrationResult struct {
	// Layer is the Integration's config layer, applied over the Recipe's
	// and under the Project's laneway.toml and laneway.local.toml. It may
	// not choose the Primary Process, and a key another Integration sets
	// to a different value is an error unless the Project's config sets
	// it.
	Layer Layer `json:"layer"`
	// Warnings are problems worth telling the developer about that don't
	// stop the Project running, each naming the file at fault.
	Warnings []string `json:"warnings,omitempty"`
}

// Layer is a config layer, keyed like laneway.toml. Empty fields aren't set,
// so the Project's laneway.toml (or Laneway's defaults) decide them. A
// Service's version may be "", for its Engine's default version.
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
	// HostDir is its directory on the developer's machine (see
	// Request.HostDir); see Rel.
	HostDir string
	// MainDir is the main Worktree's directory, read-only, when Dir is
	// another Worktree; "" otherwise.
	MainDir string
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

// Integration answers a CallIntegration for a Project the Integration's
// detection rule matched.
type Integration func(Project) (IntegrationResult, error)

// Plugin is what a Plugin's module handles, by the names its manifest
// declares.
type Plugin struct {
	Recipes      map[string]Recipe
	Integrations map[string]Integration
	Commands     map[string]Command
	Generators   map[string]Generator
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
	project := Project{Dir: req.Dir, HostDir: req.HostDir, MainDir: req.MainDir}
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
		return Response{HookResult: &HookResult{Effects: effects}}, nil
	case CallIntegration:
		integration, ok := p.Integrations[req.Name]
		if !ok {
			return Response{}, fmt.Errorf("this Plugin has no Integration %q", req.Name)
		}
		res, err := integration(project)
		if err != nil {
			return Response{}, err
		}
		return Response{IntegrationResult: &res}, nil
	case CallCommand:
		command, ok := p.Commands[req.Name]
		if !ok {
			return Response{}, fmt.Errorf("this Plugin has no command %q", req.Name)
		}
		effects, err := command(project, req.Args)
		if err != nil {
			return Response{}, err
		}
		return Response{CommandResult: &CommandResult{Effects: effects}}, nil
	case CallGenerator:
		generator, ok := p.Generators[req.Name]
		if !ok {
			return Response{}, fmt.Errorf("this Plugin has no Generator %q", req.Name)
		}
		res, err := generator(project)
		if err != nil {
			return Response{}, err
		}
		return Response{GeneratorResult: &res}, nil
	}
	return Response{}, errors.New("this Plugin doesn't answer " + req.Call + " calls")
}
