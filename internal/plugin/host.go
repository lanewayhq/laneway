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
	// installed Plugin is pinned to it. "" without a module.
	SHA256 string
	// Engines are the Engines it adds, read from its EngineFiles.
	Engines []*engine.Manifest
}

// installed is what installedFile records.
type installed struct {
	Source string `json:"source"`
	SHA256 string `json:"sha256,omitempty"`
}

// Host keeps the installed Plugins and runs their modules. It is safe for
// concurrent use.
type Host struct {
	opts    Options
	runtime *runtime

	mu      sync.Mutex
	plugins map[string]Plugin // by name
}

// Open loads the Plugins installed in opts.Dir. A Plugin that can't be
// loaded, such as one targeting another Plugin API version after Laneway was
// updated, is skipped and logged.
func Open(opts Options) (*Host, error) {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	opts.Timeout = cmp.Or(opts.Timeout, DefaultTimeout)
	rt, err := newRuntime(opts)
	if err != nil {
		return nil, err
	}
	h := &Host{opts: opts, runtime: rt, plugins: map[string]Plugin{}}
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
	return Plugin{Manifest: m, Dir: dir, Source: in.Source, SHA256: in.SHA256, Engines: engines}, nil
}

// Inspect reads the Plugin in the directory src without adding it, and
// checks that it can be added: that its manifest is valid and its Recipes,
// Integrations and Engines don't take names Laneway's or another installed
// Plugin's have.
func (h *Host) Inspect(src string) (Plugin, error) {
	m, err := LoadManifest(src)
	if err != nil {
		return Plugin{}, err
	}
	engines, err := readEngines(os.DirFS(src), src, m)
	if err != nil {
		return Plugin{}, err
	}
	p := Plugin{Manifest: m, Dir: src, Source: src, Engines: engines}
	if err := h.checkEngineNames(p); err != nil {
		return Plugin{}, err
	}
	if m.needsModule() {
		data, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(m.module())))
		if err != nil {
			return Plugin{}, err
		}
		sum := sha256.Sum256(data)
		p.SHA256 = hex.EncodeToString(sum[:])
	}
	taken := map[string]string{} // Recipe name → who has it
	for _, r := range recipe.Builtin {
		taken[r.Name()] = "Laneway"
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

// Add installs the Plugin in the directory src, copying its manifest and
// module so that later changes to src don't change what runs. A Plugin of the
// same name is replaced. It returns the Plugin added and the one it
// replaced, if any.
func (h *Host) Add(src string) (added Plugin, replaced *Plugin, err error) {
	p, err := h.Inspect(src)
	if err != nil {
		return Plugin{}, nil, err
	}
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
	record, err := json.Marshal(installed{Source: p.Source, SHA256: p.SHA256})
	if err != nil {
		return Plugin{}, nil, err
	}
	if err := os.WriteFile(filepath.Join(tmp, installedFile), record, 0o644); err != nil {
		return Plugin{}, nil, err
	}
	// The module was hashed when inspected: check the copy is what was
	// hashed, so the pin is to what's installed.
	if p.SHA256 != "" {
		if sum, err := fileSHA256(filepath.Join(tmp, filepath.FromSlash(p.module()))); err != nil || sum != p.SHA256 {
			return Plugin{}, nil, fmt.Errorf("the module of Plugin %s changed while it was being added; add it again", p.Name)
		}
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
// ships, then recipe.Builtin.
func (h *Host) Recipes() []recipe.Recipe {
	var recipes []recipe.Recipe
	for _, p := range h.List() {
		for _, r := range p.Recipes {
			recipes = append(recipes, pluginRecipe{host: h, plugin: p, decl: r})
		}
	}
	return append(recipes, recipe.Builtin...)
}

// ResolveDir is recipe.ResolveDir with the installed Plugins' Recipes and
// Integrations as well as Laneway's own (see Recipes and Integrations).
func (h *Host) ResolveDir(dir string, found ...runtimes.Runtime) (recipe.Resolved, error) {
	layers, err := config.LoadLayers(dir)
	if err != nil {
		return recipe.Resolved{}, err
	}
	return recipe.Resolve(recipe.Input{
		Dir:          dir,
		Layers:       layers,
		Recipes:      h.Recipes(),
		Engines:      h.Engines(),
		Integrations: h.Integrations(),
		Runtimes:     found,
	})
}

// call runs p's module for req about the Project in dir, decoding its
// response into res. what names the call in errors, such as "the procfile
// Recipe (Plugin procfile)".
func (h *Host) call(p Plugin, what string, req request, dir string, res any) error {
	out, err := h.runtime.call(p, req, dir)
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
