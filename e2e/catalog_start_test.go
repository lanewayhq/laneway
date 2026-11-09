package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestPluginsTheDaemonCouldntFetchOnStartAreAWarningOnTheProjectUntilLaneDoctorFetchesThem(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(testkit.Plugin(t, "kv"))
	d := catalogDaemon(t, cat)
	repo := kvEnvProject(t, "kv-offline")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	cat.Offline()

	upgrade(t, d)

	const warning = "this Project needs Plugins from the Catalog that Laneway couldn't fetch: kv"
	eventually(t, "the Project to warn that kv couldn't be fetched", func() bool {
		return slices.ContainsFunc(projectWarnings(t, d, "kv-offline"), func(w string) bool { return strings.Contains(w, warning) })
	})
	status := d.MustRunCLI(d.Home, "status").Stdout
	assertContains(t, status, `warning: Project "kv-offline": `+warning)
	assertContains(t, status, "run `lane doctor` in the Project to fetch them")
	assertContains(t, d.MustRunCLI(repo.Dir, "info").Stdout, "warning: "+warning)
	assertContains(t, d.MustRunCLI(repo.Dir, "worktree", "ready").Stderr, "lane worktree: warning: "+warning)

	cat.Online()
	d.RunCLI(repo.Dir, "doctor")

	if w := projectWarnings(t, d, "kv-offline"); len(w) > 0 {
		t.Fatalf("the Project still warns after lane doctor fetched its Plugins: %q", w)
	}
	assertNotContains(t, d.MustRunCLI(d.Home, "status").Stdout, "warning:")
	eventually(t, "the kv Service to be served", func() bool { return servesService(t, d, "kv") })
}

// projectWarnings are the warnings GET /v1/projects, which the Dashboard
// shows, has on the Project name.
func projectWarnings(t *testing.T, d *testkit.Daemon, name string) []string {
	t.Helper()
	projects, err := d.Client().Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		if p.Name == name {
			return p.Warnings
		}
	}
	t.Fatalf("no Project %q", name)
	return nil
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

func TestWithoutEverReachingTheCatalogAConfigErrorOnStartIsntPutDownToPluginsLanewayCouldntFetch(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Offline()
	d := catalogDaemon(t, cat)
	repo := gitfixture.New(t, "never-checked").
		WriteFile(".gitignore", ".env\n").
		WriteFile("laneway.toml", "[env]\nAPP_URL = \"{url}\"\n").
		Commit("an env var")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.Stop()
	if err := os.WriteFile(filepath.Join(repo.Dir, "laneway.toml"), []byte("[env]\nAPP_URL = \"{domian}\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d.Start()

	eventually(t, "the Project to warn", func() bool { return len(projectWarnings(t, d, "never-checked")) > 0 })
	w := projectWarnings(t, d, "never-checked")[0]
	assertContains(t, w, "unknown template variable {domian}")
	assertNotContains(t, w, "couldn't fetch any Plugin from it this Project needs")
}
