package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

// backgroundTimeout is how long a test waits for what the daemon does in
// the background when it starts or fetches from the Catalog: loading the
// Catalog, downloading and compiling Plugins, and preparing or serving
// Worktrees again. On a loaded CI runner that takes well over
// discoveryTimeout.
const backgroundTimeout = 30 * time.Second

// eventuallyInBackground is eventually, for what the daemon does in the
// background (see backgroundTimeout).
func eventuallyInBackground(t *testing.T, what string, cond func() bool) {
	t.Helper()
	testkit.WaitUntil(t, backgroundTimeout, what, cond)
}

func TestOnStartTheDaemonFetchesTheCatalogPluginsALinkedProjectNeedsAndServesItsServices(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(testkit.Plugin(t, "kv"))
	d := catalogDaemon(t, cat)
	repo := serviceProject(t, "kv-upgrade", `kv = "1"`, "[env]\nKV_PORT = \"{kv_port}\"\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	upgrade(t, d)

	eventuallyInBackground(t, "kv to be fetched again", func() bool { return slices.Contains(installedPlugins(t, d), "kv") })
	eventuallyInBackground(t, "the kv Service to be served again", func() bool { return servesService(t, d, "kv") })
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
	eventuallyInBackground(t, "the new Worktree's setup to fail", func() bool { return setupState(t, d, wt.Dir) == api.SetupFailed })
	// The start-up fetch fails before the Catalog comes back, so it's the
	// next start that fetches kv.
	eventuallyInBackground(t, "the start-up fetch to fail", func() bool { return len(projectWarnings(t, d, "kv-retry")) > 0 })

	cat.Online()
	d.Restart()

	eventuallyInBackground(t, "the new Worktree to be Ready", func() bool { return setupState(t, d, wt.Dir) == api.SetupReady })
	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "KV_PORT=7379\n")
}

func TestAWorktreeWhoseSetupFailedForAPluginInstalledSinceIsPreparedAgainOnStart(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(testkit.Plugin(t, "kv"))
	d := catalogDaemon(t, cat)
	repo := kvEnvProject(t, "kv-installed-since")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	installed := filepath.Join(gitfixture.TempDir(t), "plugins")
	if err := os.CopyFS(installed, os.DirFS(d.Paths.Plugins())); err != nil {
		t.Fatal(err)
	}
	cat.Offline()
	d.Stop()
	wt := repo.AddWorktree("feature")
	upgrade(t, d)
	eventuallyInBackground(t, "the new Worktree's setup to fail", func() bool { return setupState(t, d, wt.Dir) == api.SetupFailed })
	eventuallyInBackground(t, "the start-up fetch to fail", func() bool { return len(projectWarnings(t, d, "kv-installed-since")) > 0 })
	d.Stop()
	// kv is installed while the daemon is stopped, as when it stops right
	// after fetching kv, before bringing the Worktrees up to date with it.
	if err := os.RemoveAll(d.Paths.Plugins()); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(d.Paths.Plugins(), os.DirFS(installed)); err != nil {
		t.Fatal(err)
	}

	d.Start()

	eventuallyInBackground(t, "the new Worktree to be Ready", func() bool { return setupState(t, d, wt.Dir) == api.SetupReady })
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
	var seen []string
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("the Project's warnings were last %q", seen)
		}
	})
	eventuallyInBackground(t, "the Project to warn that kv couldn't be fetched", func() bool {
		seen = projectWarnings(t, d, "kv-offline")
		return slices.ContainsFunc(seen, func(w string) bool { return strings.Contains(w, warning) })
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
	eventuallyInBackground(t, "the kv Service to be served", func() bool { return servesService(t, d, "kv") })
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

	eventuallyInBackground(t, "the Project to warn", func() bool { return len(projectWarnings(t, d, "never-checked")) > 0 })
	w := projectWarnings(t, d, "never-checked")[0]
	assertContains(t, w, "unknown template variable {domian}")
	assertNotContains(t, w, "couldn't fetch any Plugin from it this Project needs")
}

func TestFetchingAPluginLeavesTheAlwaysProcessesOfProjectsNotUsingItAsTheyAre(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(testkit.Plugin(t, "kv"))
	d := catalogDaemon(t, cat)
	pids := gitfixture.TempDir(t)
	otherPID, userPID := filepath.Join(pids, "other"), filepath.Join(pids, "user")
	other := alwaysProject(t, "no-kv", "", otherPID)
	user := alwaysProject(t, "uses-kv", "[services]\nkv = \"1\"\n", userPID)
	d.MustRunCLI(other.Dir, "link", "--yes")
	d.MustRunCLI(user.Dir, "link", "--yes")
	waitForPID(t, userPID)
	cat.Offline()
	d.Stop()
	removeFile(t, userPID)
	upgrade(t, d) // kv can't be fetched again, so uses-kv's always Process doesn't start
	waitForPID(t, otherPID)
	d.MustRunCLI(other.Dir, "down", "worker") // which the developer then stops
	removeFile(t, otherPID)

	cat.Online()
	d.RunCLI(user.Dir, "doctor")

	waitForPID(t, userPID)
	if _, err := os.Stat(otherPID); err == nil {
		t.Fatal("fetching kv started again the always Process of a Project that doesn't use kv, which the developer stopped")
	}
}

func TestAWorktreeSetUpWithoutAPluginItNeedsIsSetUpAgainOnceLaneDoctorFetchesIt(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(testkit.Plugin(t, "layers"))
	cat.Offline() // Laneway has never reached it
	d := catalogDaemon(t, cat)
	repo := gitfixture.New(t, "assets-later").
		WriteFile("assets.json", layerJSON(t, map[string]any{"worktree": map[string]any{"on_create": []string{"touch assets-set-up"}}})).
		Commit("an assets Integration with a setup step")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	marker := filepath.Join(repo.Dir, "assets-set-up")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the assets Integration's setup step ran without its Plugin")
	}

	cat.Online()
	d.RunCLI(repo.Dir, "doctor")

	eventuallyInBackground(t, "the assets Integration's setup step to run", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
}

// alwaysProject is a repo with config extra and an always Process,
// worker, that writes its pid to pidFile.
func alwaysProject(t *testing.T, name, extra, pidFile string) *gitfixture.Repo {
	t.Helper()
	return gitfixture.New(t, name).
		WriteFile("laneway.toml", fmt.Sprintf("%s[processes.worker]\ncommand = %q\nstart_mode = \"always\"\n",
			extra, "echo $$ > "+pidFile+"; exec sleep 600")).
		Commit("an always Process")
}

// removeFile removes the file at path.
func removeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
