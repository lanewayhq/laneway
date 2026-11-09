package recipe_test

import (
	"os"
	"sync"

	"github.com/lanewayhq/laneway/internal/engine"
	"github.com/lanewayhq/laneway/internal/plugin"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/runtimes"
)

// noPlugins is a Plugin Host with no Plugins installed, as on a fresh
// machine: Laneway's own Recipes and the built-in Plugins' Engines (Redis,
// Mailpit, Postgres, MySQL).
var noPlugins = sync.OnceValue(func() *plugin.Host {
	dir, err := os.MkdirTemp("", "laneway-recipe-test-")
	if err != nil {
		panic(err)
	}
	h, err := plugin.Open(plugin.Options{Dir: dir})
	if err != nil {
		panic(err)
	}
	return h
})

// resolveBuiltin resolves the Project in dir from its laneway.toml and
// laneway.local.toml, as the daemon does with no Plugins installed.
func resolveBuiltin(dir string, found ...runtimes.Runtime) (recipe.Resolved, error) {
	return noPlugins().ResolveDir(dir, found...)
}

// builtinEngines are the Engines of the Plugins built into Laneway.
func builtinEngines() engine.Set {
	return noPlugins().Engines()
}
