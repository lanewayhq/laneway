package plugin

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
)

// BuiltinSource is the Source of a Plugin built into Laneway.
const BuiltinSource = "built in"

// Builtin is a Plugin built into Laneway: its manifest and its WASM module,
// run in the sandbox like an installed Plugin's, so a first-party Plugin can
// move out of the binary unchanged. Built-in Plugins aren't installed:
// `lane plugin list` doesn't show them, and no Plugin may take their names.
type Builtin struct {
	// Manifest is its plugin.json.
	Manifest []byte
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
		if m.needsModule() && b.Module == nil {
			return nil, fmt.Errorf("built-in Plugin %s needs a module", m.Name)
		}
		plugins[m.Name] = Plugin{Manifest: m, Source: BuiltinSource, readModule: b.Module, SHA256: b.ModuleID}
	}
	return plugins, nil
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

// Has reports whether a Plugin called name is installed or built into
// Laneway.
func (h *Host) Has(name string) bool {
	_, ok := h.lookup(name)
	return ok
}
