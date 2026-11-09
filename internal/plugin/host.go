// Package plugin is Laneway's Plugin host (ADR-0007): it reads Plugin
// manifests, keeps the installed Plugins, and runs their WASM modules with
// wazero as WASI commands, one JSON request on stdin and one JSON response
// on stdout, with the Project mounted read-only, a time limit, and stderr
// sent to Laneway's logs.
//
// The daemon and the CLI each open a Host on the same directory of installed
// Plugins; the Recipe Resolver sees Plugins' Recipes only as recipe.Recipe
// values a Host hands it.
package plugin

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/engine"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/runtimes"
)

// DefaultTimeout is how long one call to a Plugin's module may take.
const DefaultTimeout = 10 * time.Second

// CatalogSource is the Source of a Plugin fetched from the Catalog.
const CatalogSource = "Catalog"

// installedFile records, in an installed Plugin's directory, where it was
// added from and its module's hash.
const installedFile = "installed.json"

// Options configure a Host.
type Options struct {
	// Dir is where installed Plugins are kept, one directory each, named
	// after the Plugin.
	Dir string
	// CacheDir is where compiled modules are kept between runs of
	// Laneway; "" keeps them in memory only.
	CacheDir string
	// Log receives what modules write to stderr, and Plugins skipped
	// because they can't be loaded. Nil discards it.
	Log *slog.Logger
	// Timeout is how long one call may take; DefaultTimeout if 0.
	Timeout time.Duration
	// Builtin are the Plugins built into Laneway.
	Builtin []Builtin
	// GlobalConfig is the global config file, whose plugins turn on
	// machine-wide Plugins in every Project; "" for none.
	GlobalConfig string
}

// Plugin is a Plugin: its manifest, and where it came from.
type Plugin struct {
	Manifest
	// Dir is the Plugin's directory: where it is installed, or for one not
	// added yet, its source.
	Dir string
	// Source is where it was added from, such as the directory given to
	// `lane plugin add`.
	Source string
	// SHA256 is its module's hash, hex-encoded, as it was when added: the
	// installed Plugin is pinned to it. For a Plugin built into Laneway,
	// it is its Builtin.ModuleID. "" without a module.
	SHA256 string
	// Engines are the Engines it adds, read from its EngineFiles.
	Engines []*engine.Manifest
	// Global is true for a machine-wide Plugin, added with `lane plugin
	// add --global`: only personal config may turn it on (see global.go).
	Global bool
	// Digest, for a Plugin read from its source (Inspect), hashes
	// everything that decides what it is and may do: its manifest, Engine
	// files and module. Passing it to AddPreviewed installs the Plugin only
	// if its source still has what was previewed. "" once installed.
	Digest string

	// serve answers a built-in Plugin's calls in Laneway's process, and
	// readModule reads a built-in Plugin's module (see Builtin).
	serve      func([]byte) ([]byte, error)
	readModule func() ([]byte, error)
}

// installed is what installedFile records.
type installed struct {
	Source string `json:"source"`
	SHA256 string `json:"sha256,omitempty"`
	Global bool   `json:"global,omitempty"`
}

// Host keeps the installed Plugins and runs their modules. It is safe for
// concurrent use.
type Host struct {
	opts    Options
	runtime *runtime

	mu       sync.Mutex
	plugins  map[string]Plugin // by name
	builtins map[string]Plugin // by name
}

// Open loads the Plugins installed in opts.Dir. A Plugin that can't be
// loaded, such as one targeting another Plugin API version after Laneway was
// updated, is skipped and logged.
func Open(opts Options) (*Host, error) {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	opts.Timeout = cmp.Or(opts.Timeout, DefaultTimeout)
	builtins, err := loadBuiltins(opts.Builtin)
	if err != nil {
		return nil, err
	}
	rt, err := newRuntime(opts)
	if err != nil {
		return nil, err
	}
	h := &Host{opts: opts, runtime: rt, plugins: map[string]Plugin{}, builtins: builtins}
	entries, err := os.ReadDir(opts.Dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		rt.close()
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p, err := loadInstalled(filepath.Join(opts.Dir, e.Name()))
		if err != nil {
			opts.Log.Warn("skipping an installed Plugin that can't be loaded", "dir", e.Name(), "err", err)
			continue
		}
		h.plugins[p.Name] = p
	}
	return h, nil
}

// Close releases the modules' runtime.
func (h *Host) Close() error {
	return h.runtime.close()
}

func loadInstalled(dir string) (Plugin, error) {
	m, err := LoadManifest(dir)
	if err != nil {
		return Plugin{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, installedFile))
	if err != nil {
		return Plugin{}, err
	}
	var in installed
	if err := json.Unmarshal(data, &in); err != nil {
		return Plugin{}, fmt.Errorf("%s: %w", filepath.Join(dir, installedFile), err)
	}
	engines, err := readEngines(os.DirFS(dir), dir, m)
	if err != nil {
		return Plugin{}, err
	}
	return Plugin{Manifest: m, Dir: dir, Source: in.Source, SHA256: in.SHA256, Engines: engines, Global: in.Global}, nil
}

// Inspect reads the Plugin in the directory src without adding it, and
// checks that it can be added: that its manifest is valid and its Recipes,
// Integrations and Engines don't take names Laneway's or another installed
// Plugin's have.
func (h *Host) Inspect(src string) (Plugin, error) {
	p, err := readSource(src)
	if err != nil {
		return Plugin{}, err
	}
	m := p.Manifest
	if err := h.checkEngineNames(p); err != nil {
		return Plugin{}, err
	}
	if _, ok := h.builtins[p.Name]; ok {
		return Plugin{}, fmt.Errorf("Laneway has a built-in Plugin named %s; give the Plugin another name", p.Name)
	}
	taken := map[string]string{} // Recipe name → who has it
	for _, b := range h.builtins {
		for _, r := range b.Recipes {
			taken[r.Name] = "Laneway"
		}
	}
	h.mu.Lock()
	for _, other := range h.plugins {
		if other.Name == p.Name {
			continue // adding p replaces it
		}
		for _, r := range other.Recipes {
			taken[r.Name] = "Plugin " + other.Name
		}
	}
	h.mu.Unlock()
	for _, r := range m.Recipes {
		if who, ok := taken[r.Name]; ok {
			return Plugin{}, fmt.Errorf("Plugin %s adds Recipe %s, but %s already has a Recipe of that name", p.Name, r.Name, who)
		}
	}
	if err := h.integrationsTaken(p); err != nil {
		return Plugin{}, err
	}
	return p, nil
}

// readSource reads the Plugin in the directory src, as its author has it:
// its manifest, Engines, and its module's hash.
func readSource(src string) (Plugin, error) {
	m, err := LoadManifest(src)
	if err != nil {
		return Plugin{}, err
	}
	engines, err := readEngines(os.DirFS(src), src, m)
	if err != nil {
		return Plugin{}, err
	}
	p := Plugin{Manifest: m, Dir: src, Source: src, Engines: engines}
	if m.needsModule() {
		if p.SHA256, err = fileSHA256(filepath.Join(src, filepath.FromSlash(m.module()))); err != nil {
			return Plugin{}, err
		}
	}
	digest := sha256.New()
	for _, f := range append([]string{ManifestFile}, m.EngineFiles...) {
		data, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(f)))
		if err != nil {
			return Plugin{}, err
		}
		fmt.Fprintf(digest, "%s %d\n", f, len(data))
		digest.Write(data)
	}
	fmt.Fprintf(digest, "module %s\n", p.SHA256)
	p.Digest = hex.EncodeToString(digest.Sum(nil))
	return p, nil
}

// ErrChanged is returned by AddPreviewed when the source no longer has the
// Plugin that was previewed.
var ErrChanged = errors.New("the Plugin's source changed since it was previewed")

// Add installs the Plugin in the directory src, copying its manifest and
// module so that later changes to src don't change what runs. A Plugin of the
// same name is replaced. It returns the Plugin added and the one it
// replaced, if any. See AddGlobal for a machine-wide Plugin.
func (h *Host) Add(src string) (added Plugin, replaced *Plugin, err error) {
	return h.add(src, src, false, "")
}

// AddFrom is Add for a Plugin unpacked into src from somewhere else, such as
// the Catalog (CatalogSource), which it records as where it came from.
func (h *Host) AddFrom(src, source string) (added Plugin, replaced *Plugin, err error) {
	return h.add(src, source, false, "")
}

// AddPreviewed is AddFrom (or AddGlobal, if global) for a Plugin the
// developer confirmed after Inspect showed it: it adds nothing, returning
// ErrChanged, unless src still has the Plugin whose Digest was digest.
func (h *Host) AddPreviewed(src, source string, global bool, digest string) (added Plugin, replaced *Plugin, err error) {
	if digest == "" {
		return Plugin{}, nil, errors.New("no previewed Plugin to add")
	}
	return h.add(src, source, global, digest)
}

// add installs the Plugin in src, recording source as where it came from
// and whether it is machine-wide (see AddGlobal), if its Digest is digest
// (any, if "").
func (h *Host) add(src, source string, global bool, digest string) (added Plugin, replaced *Plugin, err error) {
	p, err := h.Inspect(src)
	if err != nil {
		return Plugin{}, nil, err
	}
	if digest != "" && p.Digest != digest {
		return Plugin{}, nil, ErrChanged
	}
	inspected := p.Digest
	p.Source, p.Global, p.Digest = source, global, ""
	if err := os.MkdirAll(h.opts.Dir, 0o755); err != nil {
		return Plugin{}, nil, err
	}
	tmp, err := os.MkdirTemp(h.opts.Dir, ".add-"+p.Name+"-")
	if err != nil {
		return Plugin{}, nil, err
	}
	defer os.RemoveAll(tmp)
	files := append([]string{ManifestFile}, p.EngineFiles...)
	if p.needsModule() {
		files = append(files, p.module())
	}
	for _, f := range files {
		if err := copyFile(filepath.Join(src, filepath.FromSlash(f)), filepath.Join(tmp, filepath.FromSlash(f))); err != nil {
			return Plugin{}, nil, err
		}
	}
	record, err := json.Marshal(installed{Source: p.Source, SHA256: p.SHA256, Global: p.Global})
	if err != nil {
		return Plugin{}, nil, err
	}
	if err := os.WriteFile(filepath.Join(tmp, installedFile), record, 0o644); err != nil {
		return Plugin{}, nil, err
	}
	// The Plugin was hashed when inspected: check the copy is what was
	// hashed, so the pin is to what's installed.
	if copied, err := readSource(tmp); err != nil || copied.Digest != inspected {
		return Plugin{}, nil, fmt.Errorf("Plugin %s changed while it was being added; add it again", p.Name)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	dir := filepath.Join(h.opts.Dir, p.Name)
	if err := os.RemoveAll(dir); err != nil {
		return Plugin{}, nil, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return Plugin{}, nil, err
	}
	p.Dir = dir
	if old, ok := h.plugins[p.Name]; ok {
		replaced = &old
	}
	h.plugins[p.Name] = p
	h.runtime.forget(p.Name)
	return p, replaced, nil
}

// List returns the installed Plugins, sorted by name.
func (h *Host) List() []Plugin {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.SortedFunc(maps.Values(h.plugins), func(a, b Plugin) int { return cmp.Compare(a.Name, b.Name) })
}

// Recipes are every Recipe that may detect a Project, in detection order:
// the installed Plugins' first (by Plugin name, then in the order each
// declares them), so a Plugin the developer added wins over what Laneway
// ships, then those of the Plugins built into Laneway, likewise.
func (h *Host) Recipes() []recipe.Recipe {
	var recipes []recipe.Recipe
	for _, p := range append(h.List(), h.builtinList()...) {
		for _, r := range p.Recipes {
			recipes = append(recipes, pluginRecipe{host: h, plugin: p, decl: r})
		}
	}
	return recipes
}

// ResolveDir resolves the Project in dir from its laneway.toml and
// laneway.local.toml with the installed Plugins' Recipes and Integrations
// and those built into Laneway (see Recipes and Integrations), checking the
// Runtimes found for dir if any are given. The daemon and the CLI read
// config only through it. The Plugins the config turns on (see turnedOn)
// are in Resolved.Plugins.
func (h *Host) ResolveDir(dir string, found ...runtimes.Runtime) (recipe.Resolved, error) {
	layers, err := config.LoadLayers(dir)
	if err != nil {
		return recipe.Resolved{}, err
	}
	on, err := h.turnedOn(layers)
	if err != nil {
		return recipe.Resolved{}, err
	}
	res, err := recipe.Resolve(recipe.Input{
		Dir:          dir,
		Layers:       layers,
		Recipes:      h.Recipes(),
		Engines:      h.Engines(),
		Integrations: h.Integrations(),
		Runtimes:     found,
	})
	res.Plugins = on
	return res, err
}

// call runs p's module for req about the Project in dir, decoding its
// response into res. what names the call in errors, such as "the procfile
// Recipe (Plugin procfile)".
func (h *Host) call(p Plugin, what string, req request, dir string, res any) error {
	out, err := h.run(p, req, dir)
	return decode(what, out, err, res)
}

// callFresh is call without reusing an earlier call's result (see
// runtime.callFresh).
func (h *Host) callFresh(p Plugin, what string, req request, dir string, res any) error {
	out, err := h.runtime.callFresh(p, req, dir)
	return decode(what, out, err, res)
}

// decode reads a module's response out (or the error err running it) into
// res. what names the call, as for Host.call.
func decode(what string, out []byte, err error, res any) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	var envelope struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		return fmt.Errorf("%s answered with something other than a JSON response: %w", what, err)
	}
	if envelope.Error != "" {
		return fmt.Errorf("%s: %s", what, envelope.Error)
	}
	if err := json.Unmarshal(out, res); err != nil {
		return fmt.Errorf("%s answered with a response Laneway can't read: %w", what, err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func fileSHA256(file string) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
