package daemon

import (
	"cmp"
	"os"
	"path/filepath"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/dotenv"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/rootdomain"
)

// staleEnvKeys are the keys in the env files of the Worktree in dir (.env,
// then the test env file, such as .env.testing) that its Env Patch doesn't
// own and whose values name a host under a Root Domain Laneway no longer
// serves: under localhost while the Root Domain is another, or under the
// previous Root Domain. Laneway can't change them, so the developer is
// told which they are. cfg is the Worktree's config; main is whether it's
// the main Worktree. Only key names and files are returned, never values:
// env files hold secrets.
func (d *Daemon) staleEnvKeys(dir string, main bool, cfg recipe.Resolved) []api.StaleEnvKey {
	current := d.rootDomain()
	_, previous := d.registry.RootDomain()
	if rootdomain.IsDefault(current) && (previous == "" || previous == current) {
		return nil
	}
	owned := cfg.EnvFor(main)
	files := []string{envFileName}
	// The test env file is read even when Test Databases are off and the
	// Env Patch doesn't reach it: the tests still read it.
	if name := cmp.Or(cfg.TestEnv, recipe.DefaultTestEnv).File; name != envFileName {
		files = append(files, name)
	}
	var stale []api.StaleEnvKey
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		f := dotenv.Parse(data)
		for _, key := range f.Keys() {
			if _, ok := owned[key]; ok {
				continue
			}
			if value, _ := f.Get(key); rootdomain.NamesStale(value, current, previous) {
				stale = append(stale, api.StaleEnvKey{Key: key, File: name})
			}
		}
	}
	return stale
}
