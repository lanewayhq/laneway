package e2e

import (
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/testkit"
)

// `lane plugin remove` (spec #48): it refuses while a Linked Project uses
// the Plugin, and never removes a Service's data.

func TestAnUnusedPluginIsRemoved(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "procfile"))

	res := d.MustRunCLI(d.Home, "plugin", "remove", "procfile")

	assertContains(t, res.Stdout, "Removed Plugin procfile 0.1.0")
	assertContains(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "No Plugins are installed")
}

func TestRemovingAPluginWhoseRecipeALinkedProjectUsesIsRefusedListingTheProjects(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "procfile"))
	for _, name := range []string{"rm-shop", "rm-blog"} {
		d.MustRunCLI(procfileRepo(t, name, "web: "+server+"\n").Dir, "link", "--yes")
	}
	d.MustRunCLI(serviceProject(t, "rm-other", `redis = "7"`, "").Dir, "link", "--yes")

	res := d.RunCLI(d.Home, "plugin", "remove", "procfile")

	if res.ExitCode == 0 {
		t.Fatalf("removing a Plugin Linked Projects use exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "Plugin procfile is used by these Linked Projects, so it wasn't removed:")
	assertContains(t, res.Stderr, "  rm-blog: its Recipe procfile\n")
	assertContains(t, res.Stderr, "  rm-shop: its Recipe procfile\n")
	if strings.Contains(res.Stderr, "rm-other") {
		t.Errorf("the refusal lists a Project that doesn't use the Plugin:\n%s", res.Stderr)
	}
	assertContains(t, lineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "procfile"), "Recipe procfile")
}

func TestRemovingAPluginWhoseIntegrationOnlyAnotherWorktreeUsesIsRefused(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "procfile"))
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", assetsPlugin(t))
	repo := procfileRepo(t, "rm-assets", "web: "+server+"\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	wt := repo.AddWorktree("feature-assets")
	wt.WriteFile("assets.json", "{}")
	waitForWorktree(t, d, "rm-assets", "feature-assets")

	res := d.RunCLI(d.Home, "plugin", "remove", "styles")

	if res.ExitCode == 0 {
		t.Fatalf("removing a Plugin a Worktree uses exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "  rm-assets: its Integration assets\n")
}

func TestRemovingAPluginWhoseEngineALinkedProjectUsesIsRefused(t *testing.T) {
	t.Parallel()
	d := kvDaemon(t)
	d.MustRunCLI(serviceProject(t, "rm-kv", `kv = "1"`, "").Dir, "link", "--yes")

	res := d.RunCLI(d.Home, "plugin", "remove", "kv")

	if res.ExitCode == 0 {
		t.Fatalf("removing a Plugin whose Engine a Project uses exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "  rm-kv: its Service kv:1\n")
}

func TestRemovingAPluginOnlyALinkedProjectsLanewayTomlNamesIsRefused(t *testing.T) {
	t.Parallel()
	server := newRequestLog(t)
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", notifyPlugin(t, server))
	repo := notifiedRepo(t, "rm-named", server, "laneway.toml", "plugins = [\"notify\"]\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	res := d.RunCLI(d.Home, "plugin", "remove", "notify")

	if res.ExitCode == 0 {
		t.Fatalf("removing a Plugin a Project's laneway.toml names exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "Plugin notify is used by these Linked Projects, so it wasn't removed:")
	assertContains(t, res.Stderr, "  rm-named: its config turns it on (plugins)\n")
}

func TestRemovingAnEnginesPluginStopsItsServiceAndKeepsItsData(t *testing.T) {
	t.Parallel()
	d := kvDaemon(t)
	repo := serviceProject(t, "rm-kv-data", `kv = "1"`, "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	c := dialRedis(t, d, 7379)
	c.do("AUTH", "laneway")
	if got := c.do("SET", "greeting", "hello"); got != "+OK" {
		t.Fatalf("SET = %q, want +OK", got)
	}
	c.conn.Close()
	d.MustRunCLI(repo.Dir, "unlink")

	res := d.MustRunCLI(d.Home, "plugin", "remove", "kv")

	assertContains(t, res.Stdout, "Kept the data of the Service kv:1")
	if d.Engine.IsRunning("laneway-kv-1") {
		t.Error("the kv container is still running after its Engine's Plugin was removed")
	}
	if services := d.MustRunCLI(d.Home, "services").Stdout; strings.Contains(services, "kv:1") {
		t.Errorf("lane services still lists kv:1 after its Engine's Plugin was removed:\n%s", services)
	}

	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "kv"))
	d.MustRunCLI(repo.Dir, "link", "--yes")
	c = dialRedis(t, d, 7379)
	c.do("AUTH", "laneway")
	if got := c.do("GET", "greeting"); got != "hello" {
		t.Errorf("GET greeting = %q after adding the Plugin again, want the value set before it was removed", got)
	}
}

func TestAPluginIsStillFoundInUseWhenTheProjectsConfigFailsToResolve(t *testing.T) {
	t.Parallel()
	d := kvDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "misbehave"))
	repo := serviceProject(t, "rm-broken", `kv = "1"`, "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	repo.WriteFile("misbehave", "fail\n") // the misbehave Recipe now fails

	kv := d.RunCLI(d.Home, "plugin", "remove", "kv")
	misbehave := d.RunCLI(d.Home, "plugin", "remove", "misbehave")

	if kv.ExitCode == 0 || misbehave.ExitCode == 0 {
		t.Fatalf("removing Plugins a Project whose config fails uses exited 0:\n%s\n%s", kv.Output(), misbehave.Output())
	}
	assertContains(t, kv.Stderr, "  rm-broken: its Service kv:1\n")
	assertContains(t, misbehave.Stderr, "  rm-broken: its Recipe misbehave\n")
}

func TestAPluginWithAnEngineIsFoundInUseByAProjectWhoseConfigCantBeRead(t *testing.T) {
	t.Parallel()
	d := kvDaemon(t)
	repo := serviceProject(t, "rm-unreadable", `kv = "1"`, "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	repo.WriteFile("laneway.local.toml", "[services\n")

	res := d.RunCLI(d.Home, "plugin", "remove", "kv")

	if res.ExitCode == 0 {
		t.Fatalf("removing a Plugin with an Engine while a Project's config can't be read exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "  rm-unreadable: maybe its Services, but its config can't be read")
}

func TestAPluginBuiltIntoLanewayOrNotInstalledCantBeRemoved(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)

	engine := d.RunCLI(d.Home, "plugin", "remove", "redis")
	generator := d.RunCLI(d.Home, "plugin", "remove", "claude")
	unknown := d.RunCLI(d.Home, "plugin", "remove", "nope")

	for _, res := range []testkit.CLIResult{engine, generator, unknown} {
		if res.ExitCode == 0 {
			t.Fatalf("removing a built-in or unknown Plugin exited 0:\n%s", res.Output())
		}
	}
	assertContains(t, engine.Stderr, "Plugin redis is built into Laneway, so it can't be removed")
	assertContains(t, generator.Stderr, "Plugin claude is built into Laneway, so it can't be removed")
	assertContains(t, unknown.Stderr, "no Plugin named nope is installed; `lane plugin list` lists them")
}
