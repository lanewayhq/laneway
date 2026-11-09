// Package builtin holds the first-party Plugins built into Laneway (ADR-0007,
// spec #48 phase 3): the laravel and rails Recipes and the vite Integration,
// and the Claude Code, T3 Code and Laravel Boost Generators behind `lane
// generate <plugin>` and `lane harness add`.
//
// Each is a Plugin like any other, written with the SDK against the Plugin
// API, with its manifest in <plugin>/plugin.json. The Recipes and the
// Integration are WASM modules, each a command in <plugin>/ built by `go
// generate` and embedded gzipped (see generate.go), run in the Plugin host's
// sandbox like any installed Plugin's. The Generators run in Laneway's
// process instead. All of them move to lanewayhq/plugins, and out of the
// binary, with the Catalog.
//
// Generated files are committed, so everything generated must be harmless
// for teammates without Laneway (see ReadyCommand), and Laneway merges it
// into the developer's files without overwriting their own (see fileblock).
package builtin

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io"
	"io/fs"
	"path"

	"github.com/lanewayhq/laneway/internal/plugin"
	"github.com/lanewayhq/laneway/sdk"
)

//go:embed */plugin.json
var manifests embed.FS

//go:generate go run generate.go

// modules are the WASM modules of the built-in Plugins that have one,
// gzipped, by Plugin: <plugin>/plugin.wasm.gz.
//
//go:embed */plugin.wasm.gz
var modules embed.FS

// handlers are the built-in Plugins' handlers, by Plugin name.
var handlers = map[string]sdk.Plugin{
	"claude": {Generators: map[string]sdk.Generator{"claude": claude}},
	"t3":     {Generators: map[string]sdk.Generator{"t3": t3}},
	"boost":  {Generators: map[string]sdk.Generator{"boost": boost}},
}

// Plugins are the built-in Plugins, for plugin.Options.Builtin.
func Plugins() []plugin.Builtin {
	builtins := wasmPlugins()
	for name, p := range handlers {
		manifest, err := manifests.ReadFile(name + "/plugin.json")
		if err != nil {
			panic(err) // embedded
		}
		builtins = append(builtins, plugin.Builtin{Manifest: manifest, Serve: func(req []byte) ([]byte, error) {
			var out bytes.Buffer
			err := sdk.Serve(p, bytes.NewReader(req), &out)
			return out.Bytes(), err
		}})
	}
	return builtins
}

// ReadyCommand is the shell command Harness hooks run: `lane worktree
// ready`, guarded so that it succeeds quietly when `lane` isn't installed
// or there is nothing to wait for (exit 3: not a Linked repository, or no
// daemon). It still fails when setup Failed (1) or waiting gave up (4).
// It runs under `sh` because T3 Code types its scripts into the developer's
// interactive shell, which may not be POSIX and where `exit` would close the
// terminal.
const ReadyCommand = `sh -c 'command -v lane >/dev/null 2>&1 || exit 0; lane worktree ready || [ $? -eq 3 ]'`

// readyTimeout is how long a Harness may let ReadyCommand run, in seconds.
// It is longer than `lane worktree ready`'s own 10 minute timeout, so that
// Laneway gives up first and says why.
const readyTimeout = 660

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
