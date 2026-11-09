package e2e

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/container/containertest"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// Engine Plugins through Seam 1: the kv test Plugin
// (internal/testkit/testplugins/kv) adds an Engine with its manifest alone,
// and the fake container engine serves its image with Redis's protocol.

// kvImage is the kv Engine's image.
const kvImage = "example.test/kv"

// kvDaemon is a daemon with the kv Plugin added, whose fake container engine
// can run the kv Engine's image.
func kvDaemon(t *testing.T, opts ...testkit.Option) *testkit.Daemon {
	t.Helper()
	d := testkit.StartDaemon(t, opts...)
	d.Engine.Serve(kvImage, containertest.Redis)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "kv"))
	return d
}

func TestAServiceOfAPluginsEngineStartsOnItsFirstConnectionAndStopsWhenIdle(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	d.Engine.Serve(kvImage, containertest.Redis)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "kv"))
	repo := serviceProject(t, "kv-lazy", `kv = "1"`, "")
	d.MustRunCLI(repo.Dir, "link")
	if calls := d.Engine.Calls(); containsCall(calls, "start") {
		t.Fatalf("linking started a container: %v", calls)
	}

	c := dialRedis(t, d, 7379)
	if got := c.do("AUTH", "laneway"); got != "+OK" {
		t.Fatalf("AUTH = %q, want +OK", got)
	}
	if got := c.do("PING"); got != "+PONG" {
		t.Fatalf("PING = %q, want +PONG", got)
	}
	if !d.Engine.IsRunning("laneway-kv-1") {
		t.Fatalf("the kv container isn't running: %v", d.Engine.Containers())
	}
	c.conn.Close()

	if delay := waitForTimer(t, clk); delay != 15*time.Minute {
		t.Fatalf("Idle scheduled after %s, want the default 15m", delay)
	}
	clk.Advance(15 * time.Minute)
	waitForServiceState(t, d, "kv", "1", "idle")
	if d.Engine.IsRunning("laneway-kv-1") {
		t.Fatal("the kv container is still running after its idle timeout")
	}
}

func TestAPluginsEnginesServiceVariablesAreEnvPatchVariables(t *testing.T) {
	t.Parallel()
	d := kvDaemon(t)
	repo := serviceProject(t, "kv-env", `kv = "1"`,
		"[env]\nKV_HOST = \"{kv_host}\"\nKV_PORT = \"{kv_port}\"\nKV_PASSWORD = \"{kv_password}\"\n")

	d.MustRunCLI(repo.Dir, "link", "--yes")

	env := readFile(t, filepath.Join(repo.Dir, ".env"))
	for _, line := range []string{"KV_HOST=" + d.ServiceHost(), "KV_PORT=7379", "KV_PASSWORD=laneway"} {
		assertContains(t, env, line+"\n")
	}
}

func TestAddingAnEnginePluginShowsItsEnginesAndPluginListListsThem(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)

	add := d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "kv"))

	assertContains(t, add.Stdout, "adds the Engine kv, image example.test/kv, port 7379")
	assertContains(t, lineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "kv"), "Engine kv")
}

func TestAPluginWhoseEngineHasTheNameOfAnotherPluginsIsRefused(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.FirstPartyPlugin(t, "redis"))
	plugin := testkit.Plugin(t, "kv")
	writeFile(t, filepath.Join(plugin, "kv.toml"), strings.Replace(readFile(t, filepath.Join(plugin, "kv.toml")), `name = "kv"`, `name = "redis"`, 1))

	res := d.RunCLI(d.Home, "plugin", "add", "--yes", plugin)

	if res.ExitCode == 0 {
		t.Fatalf("adding a second redis Engine exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "Plugin kv adds the Engine redis, but Plugin redis already has an Engine of that name")
}

func TestAPluginWithAnInvalidEngineManifestIsRefusedNamingTheFileAndKey(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := testkit.Plugin(t, "kv")
	writeFile(t, filepath.Join(plugin, "kv.toml"), strings.Replace(readFile(t, filepath.Join(plugin, "kv.toml")), "port = 7379", "port = 0", 1))

	res := d.RunCLI(d.Home, "plugin", "add", "--yes", plugin)

	if res.ExitCode == 0 {
		t.Fatalf("adding a Plugin with port = 0 exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, filepath.Join(plugin, "kv.toml")+": port must be the Engine's TCP port")
}

func TestAPluginsEngineIsStillKnownAfterTheDaemonRestarts(t *testing.T) {
	t.Parallel()
	d := kvDaemon(t)
	repo := serviceProject(t, "kv-restart", `kv = "1"`, "")
	d.MustRunCLI(repo.Dir, "link")

	d.Restart()

	c := dialRedis(t, d, 7379)
	if got := c.do("AUTH", "laneway"); got != "+OK" {
		t.Fatalf("AUTH after a restart = %q, want +OK", got)
	}
	assertContains(t, d.MustRunCLI(repo.Dir, "info").Stdout, "kv")
}
