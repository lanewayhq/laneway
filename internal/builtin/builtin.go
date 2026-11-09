// Package builtin holds the first-party Plugins built into Laneway (ADR-0007,
// spec #48 phase 3): the Claude Code, T3 Code and Laravel Boost Generators
// behind `lane generate <plugin>` and `lane harness add`.
//
// Each is a Plugin like any other, written with the SDK against the Plugin
// API, with its manifest in <plugin>/plugin.json; it just runs in Laneway's
// process instead of as a WASM module, until first-party Plugins move to
// lanewayhq/plugins.
//
// Generated files are committed, so everything generated must be harmless
// for teammates without Laneway (see ReadyCommand), and Laneway merges it
// into the developer's files without overwriting their own (see fileblock).
package builtin

import (
	"bytes"
	"embed"

	"github.com/lanewayhq/laneway/internal/plugin"
	"github.com/lanewayhq/laneway/sdk"
)

//go:embed */plugin.json
var manifests embed.FS

// handlers are the built-in Plugins' handlers, by Plugin name.
var handlers = map[string]sdk.Plugin{
	"claude": {Generators: map[string]sdk.Generator{"claude": claude}},
	"t3":     {Generators: map[string]sdk.Generator{"t3": t3}},
	"boost":  {Generators: map[string]sdk.Generator{"boost": boost}},
}

// Plugins are the built-in Plugins, for plugin.Options.Builtin.
func Plugins() []plugin.Builtin {
	var builtins []plugin.Builtin
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
