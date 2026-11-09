package recipe_test

import (
	"os"
	"sync"

	"github.com/lanewayhq/laneway/internal/engine"
	"github.com/lanewayhq/laneway/internal/plugin"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// noPlugins is a Plugin Host with no Plugins installed, as on a fresh
// machine.
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

// resolveFromConfig resolves the Project in dir from its laneway.toml and
// laneway.local.toml, as the daemon does once the first-party Engine Plugins
// its config names are fetched.
func resolveFromConfig(dir string) (recipe.Resolved, error) {
	return noPlugins().ResolveDirAssuming(dir, firstPartyEngines())
}

// firstPartyEngines are the Engines of the first-party Plugins (Redis,
// Mailpit, Postgres, MySQL), which the Catalog lists.
var firstPartyEngines = sync.OnceValue(func() engine.Set {
	set, err := testkit.FirstPartyEngines()
	if err != nil {
		panic(err)
	}
	return set
})
