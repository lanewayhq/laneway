package plugin

import (
	"encoding/json"
	"fmt"
)

// BuiltinSource is the Source of a Plugin built into Laneway.
const BuiltinSource = "built in"

// Builtin is a Plugin built into Laneway: its manifest, and its handler run
// in Laneway's own process instead of as a WASM module. It speaks the same
// JSON requests and responses as a module (see sdk.Serve), except that the
// request's dir is the Project's real directory, so a first-party Plugin
// can move out of the binary unchanged. Built-in Plugins aren't installed:
// `lane plugin list` doesn't show them, and no Plugin may take their names.
type Builtin struct {
	// Manifest is its plugin.json.
	Manifest []byte
	// Serve answers one JSON request with one JSON response.
	Serve func(request []byte) ([]byte, error)
}

// loadBuiltins reads the built-in Plugins' manifests.
func loadBuiltins(builtins []Builtin) (map[string]Plugin, error) {
	plugins := map[string]Plugin{}
	for _, b := range builtins {
		m, err := parseManifest(b.Manifest)
		if err != nil {
			return nil, fmt.Errorf("built-in Plugin: %w", err)
		}
		plugins[m.Name] = Plugin{Manifest: m, Source: BuiltinSource, serve: b.Serve}
	}
	return plugins, nil
}

// run answers req with p's module, or in-process for a built-in Plugin.
func (h *Host) run(p Plugin, req request, dir string) ([]byte, error) {
	if p.serve == nil {
		return h.runtime.call(p, req, dir)
	}
	req.APIVersion, req.Dir = APIVersion, dir
	in, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	return p.serve(in)
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
