package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/lanewayhq/laneway/internal/detect"
)

// APIVersion is the Plugin API version this Laneway speaks. Until Laneway
// 1.0 only this version is accepted, and it may change freely (ADR-0007).
const APIVersion = 1

// ManifestFile is a Plugin's manifest, at the top of its directory.
const ManifestFile = "plugin.json"

// DefaultModule is a Plugin's WASM module when its manifest doesn't name one.
const DefaultModule = "plugin.wasm"

// Manifest is what a Plugin declares in plugin.json: who it is, the Plugin
// API it targets, what it adds and what it may do. Later Plugin API versions
// add what Plugins can add (Integrations, Engines, Generators, commands,
// hooked events) as more fields here.
type Manifest struct {
	// Name identifies the Plugin, such as "procfile": lowercase letters,
	// digits and dashes, starting with a letter.
	Name string `json:"name"`
	// Version is the Plugin's own version, such as "1.2.0".
	Version string `json:"version"`
	// APIVersion is the Plugin API version it targets; only APIVersion is
	// accepted.
	APIVersion int `json:"api_version"`
	// Module is its WASM module, relative to its directory; DefaultModule
	// if empty. Only a Plugin with calls to answer, such as a Recipe's,
	// needs one.
	Module string `json:"module,omitempty"`
	// Recipes are the Recipes it adds, in the order they detect Projects.
	Recipes []RecipeDecl `json:"recipes,omitempty"`
	// Integrations are the Integrations it adds.
	Integrations []IntegrationDecl `json:"integrations,omitempty"`
	// Commands are the commands it adds, run as `lane <plugin> <command>`.
	Commands []CommandDecl `json:"commands,omitempty"`
	// EngineFiles are the Engines it adds: Engine manifests (TOML, read by
	// engine.Parse), relative to its directory. An Engine needs no module.
	EngineFiles []string `json:"engines,omitempty"`
	// Generators are the Generators it adds, run in this order by
	// `lane generate <plugin>`.
	Generators []GeneratorDecl `json:"generators,omitempty"`
	// Hooks are the lifecycle events it hooks, from Events. Its module
	// answers a hook call for each with effects.
	Hooks []string `json:"hooks,omitempty"`
	// Capabilities are what it may do beyond returning config.
	Capabilities Capabilities `json:"capabilities,omitzero"`
}

// RecipeDecl is a Recipe a Plugin adds: its name and the detection rule a
// Project of its kind matches. Its module answers sdk.CallRecipe for the
// name with the Project's config layer.
type RecipeDecl struct {
	Name   string      `json:"name"`
	Detect detect.Rule `json:"detect"`
}

// Capabilities are what a Plugin may do, shown to the developer before it's
// added: the kinds of effect it may return, and the hosts its HTTP effects
// may reach. Laneway refuses any effect it doesn't declare.
type Capabilities struct {
	// Effects are the effect kinds it may return, from EffectKinds.
	Effects []string `json:"effects,omitempty"`
	// HTTP are the hosts its http effects may send requests to.
	HTTP []string `json:"http,omitempty"`
}

// Effect kinds a Plugin's hooks and commands may return, which Laneway
// applies under its usual rules.
const (
	EffectEnv     = "env"
	EffectProcess = "process"
	EffectExec    = "exec"
	EffectFile    = "file"
	EffectHTTP    = "http"
	EffectWarning = "warning"
)

// EffectKinds describes every effect kind, as the developer is shown it.
var EffectKinds = map[string]string{
	EffectEnv:     "set keys in Worktrees' env files",
	EffectProcess: "add Processes",
	EffectExec:    "run commands in Worktrees",
	EffectFile:    "merge blocks into files",
	EffectHTTP:    "send HTTP requests",
	EffectWarning: "show warnings",
}

// name is what Plugin and Recipe names look like: they become directory
// names and, later, `lane <plugin>` commands.
var name = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// LoadManifest reads and checks the manifest of the Plugin in dir. Errors
// name the file and say what to change.
func LoadManifest(dir string) (Manifest, error) {
	file := filepath.Join(dir, ManifestFile)
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return Manifest{}, fmt.Errorf("%s has no %s, so it isn't a Plugin", dir, ManifestFile)
	}
	if err != nil {
		return Manifest{}, err
	}
	m, err := parseManifest(data)
	if err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", file, err)
	}
	if m.needsModule() {
		module := filepath.Join(dir, filepath.FromSlash(m.module()))
		if fi, err := os.Stat(module); err != nil || !fi.Mode().IsRegular() {
			return Manifest{}, fmt.Errorf("%s: the Plugin's module %s isn't there; build it, such as with "+
				"`GOOS=wasip1 GOARCH=wasm go build -o %s .`, or set module to where it is", file, module, m.module())
		}
	}
	return m, nil
}

// parseManifest decodes and checks a manifest. The Plugin API version is
// checked first, since a manifest for another version may have keys this
// Laneway doesn't know.
func parseManifest(data []byte) (Manifest, error) {
	var head struct {
		Name       string `json:"name"`
		APIVersion int    `json:"api_version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return Manifest{}, fmt.Errorf("not a valid manifest: %w", err)
	}
	switch {
	case head.APIVersion == 0:
		return Manifest{}, fmt.Errorf("api_version isn't set; set it to the Plugin API version the Plugin targets (this Laneway speaks %d)", APIVersion)
	case head.APIVersion > APIVersion:
		return Manifest{}, fmt.Errorf("Plugin %s targets Plugin API version %d, but this Laneway speaks only version %d; "+
			"update Laneway, or use a release of %s built for Plugin API %d", head.Name, head.APIVersion, APIVersion, head.Name, APIVersion)
	case head.APIVersion < APIVersion:
		return Manifest{}, fmt.Errorf("Plugin %s targets Plugin API version %d, but this Laneway speaks only version %d; "+
			"use a release of %s built for Plugin API %d", head.Name, head.APIVersion, APIVersion, head.Name, APIVersion)
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("not a valid manifest: %w", err)
	}
	return m, m.validate()
}

func (m Manifest) validate() error {
	if !name.MatchString(m.Name) {
		return fmt.Errorf("name %q isn't a Plugin name: use lowercase letters, digits and dashes, starting with a letter", m.Name)
	}
	if strings.TrimSpace(m.Version) == "" {
		return errors.New("version isn't set; set it to the Plugin's version, such as \"1.0.0\"")
	}
	if m.Module != "" && (path.IsAbs(m.Module) || !fs.ValidPath(m.Module)) {
		return fmt.Errorf("module %q must be a path inside the Plugin's directory, such as %q", m.Module, DefaultModule)
	}
	if len(m.Recipes) == 0 && len(m.Integrations) == 0 && len(m.Commands) == 0 && len(m.EngineFiles) == 0 && len(m.Generators) == 0 && len(m.Hooks) == 0 {
		return errors.New("the Plugin adds nothing; declare what it adds, such as a Recipe under recipes, an Integration under integrations, an Engine under engines, a command under commands, a Generator under generators or the events it hooks under hooks")
	}
	if err := m.validateEngineFiles(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i, r := range m.Recipes {
		if !name.MatchString(r.Name) {
			return fmt.Errorf("recipes[%d]: name %q isn't a Recipe name: use lowercase letters, digits and dashes, starting with a letter", i, r.Name)
		}
		if seen[r.Name] {
			return fmt.Errorf("recipes[%d]: the Plugin declares Recipe %s twice", i, r.Name)
		}
		seen[r.Name] = true
		if err := r.Detect.Validate(); err != nil {
			return fmt.Errorf("recipes[%d] (%s): %w", i, r.Name, err)
		}
	}
	if err := m.validateGenerators(); err != nil {
		return err
	}
	if err := validateIntegrations(m.Integrations); err != nil {
		return err
	}
	if err := m.validateCommands(); err != nil {
		return err
	}
	if err := validateHooks(m.Hooks); err != nil {
		return err
	}
	for _, kind := range m.Capabilities.Effects {
		if _, ok := EffectKinds[kind]; !ok {
			return fmt.Errorf("capabilities.effects: %q isn't an effect kind; use %s", kind, strings.Join(slices.Sorted(maps.Keys(EffectKinds)), ", "))
		}
	}
	if len(m.Capabilities.HTTP) > 0 && !slices.Contains(m.Capabilities.Effects, EffectHTTP) {
		return fmt.Errorf("capabilities.http lists hosts, but capabilities.effects doesn't include %q; add it, or remove the hosts", EffectHTTP)
	}
	if err := validateHTTPHosts(m.Capabilities.HTTP); err != nil {
		return err
	}
	return nil
}

// module is the Plugin's module, relative to its directory.
func (m Manifest) module() string {
	if m.Module == "" {
		return DefaultModule
	}
	return m.Module
}

// needsModule reports whether the Plugin has calls to answer.
func (m Manifest) needsModule() bool {
	return len(m.Recipes) > 0 || len(m.Integrations) > 0 || len(m.Commands) > 0 || len(m.Generators) > 0 || len(m.Hooks) > 0
}
