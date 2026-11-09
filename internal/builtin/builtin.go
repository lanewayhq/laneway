// Package builtin holds the first-party Plugins built into Laneway (ADR-0007,
// spec #48 phase 3): the laravel and rails Recipes, the vite Integration, and
// the Claude Code, T3 Code and Laravel Boost Generators behind `lane
// generate <plugin>` and `lane harness add`.
//
// Each is a Plugin like any other, written with the SDK against the Plugin
// API, with its manifest in <plugin>/plugin.json and its source a command in
// <plugin>/, built into a WASM module by `go generate` and embedded gzipped
// (see generate.go). Laneway runs each in the Plugin host's sandbox, with
// the Project mounted read-only, like any installed Plugin's. All of them
// move to lanewayhq/plugins, and out of the binary, with the Catalog.
//
// Generated files are committed, so everything generated must be harmless
// for teammates without Laneway (see agents.ReadyCommand), and Laneway
// merges it into the developer's files without overwriting their own (see
// fileblock).
package builtin

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"path"
	"slices"

	"github.com/lanewayhq/laneway/internal/plugin"
)

//go:embed */plugin.json
var manifests embed.FS

//go:generate go run generate.go

// modules are the built-in Plugins' WASM modules, gzipped, by Plugin:
// <plugin>/plugin.wasm.gz.
//
//go:embed */plugin.wasm.gz
var modules embed.FS

// Plugins are the built-in Plugins, for plugin.Options.Builtin.
func Plugins() []plugin.Builtin {
	return wasmPlugins()
}

// notHarnesses are the built-in Plugins whose Generators set up something
// other than a Harness: boost's, a Laravel package's guidelines.
var notHarnesses = []string{"boost"}

// Harnesses are the Harnesses `lane harness add` sets up, sorted: the
// built-in Plugins with Generators, other than notHarnesses, each named
// after its Plugin.
func Harnesses() []string {
	var names []string
	for _, b := range Plugins() {
		var m struct {
			Name       string            `json:"name"`
			Generators []json.RawMessage `json:"generators"`
		}
		if err := json.Unmarshal(b.Manifest, &m); err != nil {
			panic(err) // embedded, and checked by plugin.Open
		}
		if len(m.Generators) > 0 && !slices.Contains(notHarnesses, m.Name) {
			names = append(names, m.Name)
		}
	}
	slices.Sort(names)
	return names
}

// wasmPlugins are the built-in Plugins that run as WASM modules.
func wasmPlugins() []plugin.Builtin {
	files, err := fs.Glob(modules, "*/plugin.wasm.gz")
	if err != nil {
		panic(err) // embedded
	}
	var builtins []plugin.Builtin
	for _, file := range files {
		manifest, err := manifests.ReadFile(path.Join(path.Dir(file), "plugin.json"))
		if err != nil {
			panic(err) // embedded
		}
		gz, err := modules.ReadFile(file)
		if err != nil {
			panic(err) // embedded
		}
		sum := sha256.Sum256(gz)
		builtins = append(builtins, plugin.Builtin{Manifest: manifest, ModuleID: hex.EncodeToString(sum[:]), Module: func() ([]byte, error) {
			r, err := gzip.NewReader(bytes.NewReader(gz))
			if err != nil {
				return nil, err
			}
			return io.ReadAll(r)
		}})
	}
	return builtins
}
