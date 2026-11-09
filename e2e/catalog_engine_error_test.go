package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/testkit"
)

// Config naming an Engine that isn't installed but is in the Catalog (issue
// #78): the error says so, and how to fetch it, instead of calling the
// Engine unknown. Each test takes its Catalog offline before the daemon
// restarts, or fetching on start would bring the Engine back.

// notInstalled is what a config error says about the Engine name, which
// the Catalog has but Laneway hasn't installed.
func notInstalled(name string) string {
	return "the " + name + " Engine isn't installed; run `lane doctor` to fetch it from the Catalog"
}

// configError is the Config line `lane info` shows for the Project in dir,
// "" if it has none.
func configError(t *testing.T, d *testkit.Daemon, dir string) string {
	t.Helper()
	for _, line := range strings.Split(d.MustRunCLI(dir, "info").Stdout, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Config") {
			return line
		}
	}
	return ""
}

func TestAServiceWhoseEngineIsInTheCatalogButNotInstalledSaysToRunLaneDoctor(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(testkit.Plugin(t, "kv"))
	d := catalogDaemon(t, cat)
	repo := serviceProject(t, "kv-not-installed", `kv = "1"`, "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	cat.Offline()

	upgrade(t, d)

	want := "laneway.toml: services.kv: " + notInstalled("kv")
	eventually(t, "lane info to say kv isn't installed", func() bool { return strings.Contains(configError(t, d, repo.Dir), want) })

	cat.Online()
	d.RunCLI(repo.Dir, "doctor")
	if line := configError(t, d, repo.Dir); line != "" {
		t.Fatalf("lane info still has a config error after lane doctor fetched kv:\n%s", line)
	}
}

func TestADatabaseEngineInTheCatalogButNotInstalledSaysToRunLaneDoctorRatherThanMustBeSqlite(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.PublishFirstParty()
	d := testkit.StartDaemon(t, cat.Option())
	repo := postgresProject(t, "pg-not-installed", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	cat.Offline()

	d.Stop()
	wt := repo.AddWorktree("feature")

	upgrade(t, d)

	want := "laneway.toml: database.engine: " + notInstalled("postgres")
	eventually(t, "lane info to say postgres isn't installed", func() bool { return strings.Contains(configError(t, d, repo.Dir), want) })
	// The new Worktree is prepared as the daemon starts, before it has
	// loaded the Catalog, and its setup fails saying so too.
	assertContains(t, d.RunCLI(wt.Dir, "worktree", "ready").Stderr, want)
}

func TestARecipeServiceWhoseEngineIsInTheCatalogButNotInstalledSaysToRunLaneDoctor(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.PublishFirstParty()
	d := testkit.StartDaemon(t, cat.Option())
	repo := laravelRepo(t, "lara-no-mailpit")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	cat.Offline()
	d.Stop()
	if err := os.RemoveAll(filepath.Join(d.Paths.Plugins(), "mailpit")); err != nil {
		t.Fatal(err)
	}

	d.Start()

	want := "the laravel Recipe (Plugin laravel): services.mailpit: " + notInstalled("mailpit")
	eventually(t, "lane info to say mailpit isn't installed", func() bool { return strings.Contains(configError(t, d, repo.Dir), want) })
}
