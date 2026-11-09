package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// Fetching Linked Projects' Catalog Plugins when the daemon starts (issue
// #78): an upgrade that leaves the installed Plugins behind, such as one to
// a Laneway whose binary no longer carries them, is simulated by removing
// the installed Plugins while the daemon is stopped.

// upgrade stops the daemon, removes every installed Plugin and starts it
// again.
func upgrade(t *testing.T, d *testkit.Daemon) {
	t.Helper()
	d.Stop()
	if err := os.RemoveAll(d.Paths.Plugins()); err != nil {
		t.Fatal(err)
	}
	d.Start()
}

func TestOnStartTheDaemonFetchesTheCatalogPluginsALinkedProjectNeedsAndServesItsServices(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(testkit.Plugin(t, "kv"))
	d := catalogDaemon(t, cat)
	repo := serviceProject(t, "kv-upgrade", `kv = "1"`, "[env]\nKV_PORT = \"{kv_port}\"\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	upgrade(t, d)

	eventually(t, "kv to be fetched again", func() bool { return slices.Contains(installedPlugins(t, d), "kv") })
	eventually(t, "the kv Service to be served again", func() bool { return servesService(t, d, "kv") })
	c := dialRedis(t, d, 7379)
	if got := c.do("AUTH", "laneway"); got != "+OK" {
		t.Fatalf("AUTH after the upgrade = %q, want +OK", got)
	}
}

// servesService reports whether the daemon serves a Service of engine.
func servesService(t *testing.T, d *testkit.Daemon, engine string) bool {
	t.Helper()
	services, err := d.Client().Services(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(services, func(s api.Service) bool { return s.Engine == engine })
}

func TestAWorktreeWhoseSetupFailedForAMissingPluginIsPreparedAgainOnceTheDaemonFetchesIt(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(testkit.Plugin(t, "kv"))
	d := catalogDaemon(t, cat)
	repo := kvEnvProject(t, "kv-retry")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	cat.Offline()
	d.Stop()
	wt := repo.AddWorktree("feature")
	upgrade(t, d)
	eventually(t, "the new Worktree's setup to fail", func() bool { return setupState(t, d, wt.Dir) == api.SetupFailed })

	cat.Online()
	d.Restart()

	eventually(t, "the new Worktree to be Ready", func() bool { return setupState(t, d, wt.Dir) == api.SetupReady })
	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "KV_PORT=7379\n")
}

func TestAlwaysProcessesStartOnceTheDaemonFetchesThePluginsTheirConfigNeeds(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(testkit.Plugin(t, "kv"))
	d := catalogDaemon(t, cat)
	pid := filepath.Join(gitfixture.TempDir(t), "worker.pid")
	repo := gitfixture.New(t, "kv-always").
		WriteFile("laneway.toml", fmt.Sprintf("[services]\nkv = \"1\"\n"+
			"[processes.worker]\ncommand = %q\nstart_mode = \"always\"\n", "echo $$ > "+pid+"; exec sleep 600")).
		Commit("an always Process using kv")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	waitForPID(t, pid)
	d.Stop()
	if err := os.Remove(pid); err != nil {
		t.Fatal(err)
	}

	upgrade(t, d)

	waitForPID(t, pid)
}

// kvEnvProject is a repo whose config declares a kv Service and puts its
// port in .env, which git ignores.
func kvEnvProject(t *testing.T, name string) *gitfixture.Repo {
	t.Helper()
	return gitfixture.New(t, name).
		WriteFile(".gitignore", ".env\n").
		WriteFile("laneway.toml", "[env]\nKV_PORT = \"{kv_port}\"\n\n[services]\nkv = \"1\"\n").
		Commit("declare a kv Service")
}

// setupState is the setup state of the Worktree at dir.
func setupState(t *testing.T, d *testkit.Daemon, dir string) string {
	t.Helper()
	info, err := d.Client().Worktree(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	return info.Worktree.Setup.State
}
