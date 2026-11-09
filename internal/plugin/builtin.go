package plugin

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/lanewayhq/laneway/internal/gitrepo"
)

// BuiltinSource is the Source of a Plugin built into Laneway.
const BuiltinSource = "built in"

// Builtin is a Plugin built into Laneway: its manifest, and either its WASM
// module, run like an installed Plugin's, or its handler run in Laneway's
// own process. A handler speaks the same JSON requests and responses as a
// module (see sdk.Serve), except that the request's dir is the Project's
// real directory, so a first-party Plugin can move out of the binary
// unchanged. Built-in Plugins aren't installed: `lane plugin list` doesn't
// show them, and no Plugin may take their names.
type Builtin struct {
	// Manifest is its plugin.json.
	Manifest []byte
	// Serve answers one JSON request with one JSON response; nil for a
	// Plugin with a Module.
	Serve func(request []byte) ([]byte, error)
	// Module reads its WASM module. It is only called when the module is
	// first compiled, so a module embedded compressed is only decompressed
	// then.
	Module func() ([]byte, error)
	// ModuleID identifies what Module returns, such as a hash of it:
	// compiled modules and calls' results are kept by it.
	ModuleID string
}

// loadBuiltins reads the built-in Plugins' manifests.
func loadBuiltins(builtins []Builtin) (map[string]Plugin, error) {
	plugins := map[string]Plugin{}
	for _, b := range builtins {
		m, err := parseManifest(b.Manifest)
		if err != nil {
			return nil, fmt.Errorf("built-in Plugin: %w", err)
		}
		if m.needsModule() && (b.Serve == nil) == (b.Module == nil) {
			return nil, fmt.Errorf("built-in Plugin %s needs either a module or a handler", m.Name)
		}
		plugins[m.Name] = Plugin{Manifest: m, Source: BuiltinSource, serve: b.Serve, readModule: b.Module, SHA256: b.ModuleID}
	}
	return plugins, nil
}

// run answers req with p's module, or in-process for a built-in Plugin with
// a handler, which sees the real directories.
func (h *Host) run(p Plugin, req request, dir string) ([]byte, error) {
	if p.serve == nil {
		return h.runtime.call(p, req, dir)
	}
	req.APIVersion, req.Dir, req.HostDir = APIVersion, dir, dir
	if main, ok := gitrepo.MainDir(dir); ok {
		req.MainDir = main
	}
	in, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	return p.serve(in)
}

// builtinList returns the built-in Plugins, sorted by name.
func (h *Host) builtinList() []Plugin {
	return slices.SortedFunc(maps.Values(h.builtins), func(a, b Plugin) int { return cmp.Compare(a.Name, b.Name) })
}

// lookup returns the installed or built-in Plugin name.
func (h *Host) lookup(name string) (Plugin, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p, ok := h.plugins[name]; ok {
		return p, true
	}
	p, ok := h.builtins[name]
	return p, ok
}
