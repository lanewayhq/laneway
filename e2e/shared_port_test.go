package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/container/containertest"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// Engines sharing a standard port through Seam 1 (ADR-0005): MySQL and
// MariaDB both use 3306, and here two test Plugins' Engines, kv and twin,
// both use 7379. Whichever Engine's default version is registered first
// keeps the standard port; the other falls back to its version block
// (57301 for version 1 of an Engine on 7379), and every port the Registry
// records stays where it is.

// twinImage is the twin Engine's image.
const twinImage = "example.test/twin"

// twinPlugin is a test Plugin adding the twin Engine: the kv test Plugin
// renamed, so its Engine has kv's standard port 7379 and default version 1.
func twinPlugin(t *testing.T) string {
	t.Helper()
	kv := testkit.Plugin(t, "kv")
	dir := filepath.Join(t.TempDir(), "twin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := strings.NewReplacer(`name = "kv"`, `name = "twin"`, kvImage, twinImage).
		Replace(readFile(t, filepath.Join(kv, "kv.toml")))
	writeFile(t, filepath.Join(dir, "twin.toml"), manifest)
	return withManifest(t, dir, `{"name": "twin", "version": "0.1.0", "api_version": 1, "engines": ["twin.toml"]}`)
}

// sharedPortDaemon is a daemon with the kv and twin Plugins added, whose
// fake container engine can run both Engines' images.
func sharedPortDaemon(t *testing.T) *testkit.Daemon {
	t.Helper()
	d := testkit.StartDaemon(t)
	addSharedPortPlugins(t, d)
	return d
}

func addSharedPortPlugins(t *testing.T, d *testkit.Daemon) {
	t.Helper()
	d.Engine.Serve(kvImage, containertest.Redis)
	d.Engine.Serve(twinImage, containertest.Redis)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "kv"))
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", twinPlugin(t))
}

// servicePort is the port `lane services` shows for engine at version.
func servicePort(t *testing.T, d *testkit.Daemon, engine, version string) string {
	t.Helper()
	return serviceRow(t, d, engine, version)[3]
}

// assertServed checks that the Service listening on port answers, and that
// it is container's.
func assertServed(t *testing.T, d *testkit.Daemon, port int, container string) {
	t.Helper()
	c := dialRedis(t, d, port)
	defer c.conn.Close()
	if got := c.do("AUTH", "laneway"); got != "+OK" {
		t.Fatalf("AUTH on port %d = %q, want +OK", port, got)
	}
	if got := c.do("PING"); got != "+PONG" {
		t.Fatalf("PING on port %d = %q, want +PONG", port, got)
	}
	if !d.Engine.IsRunning(container) {
		t.Fatalf("connecting to port %d didn't start %s: %v", port, container, d.Engine.Containers())
	}
}

func TestTwoEnginesDefaultVersionsSharingAStandardPortBothLinkAndTheSecondGetsAPortFromItsVersionBlock(t *testing.T) {
	t.Parallel()
	d := sharedPortDaemon(t)
	first := serviceProject(t, "shared-first", `kv = "1"`, "[env]\nKV_PORT = \"{kv_port}\"\n")
	second := serviceProject(t, "shared-second", `twin = "1"`, "[env]\nTWIN_PORT = \"{twin_port}\"\n")

	d.MustRunCLI(first.Dir, "link", "--yes")
	d.MustRunCLI(second.Dir, "link", "--yes")
	wt := second.AddWorktree("feature-twin")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	check := func(when string) {
		t.Helper()
		if got := servicePort(t, d, "kv", "1"); got != "7379" {
			t.Errorf("%s: kv:1 has port %s, want the standard 7379, as it was registered first", when, got)
		}
		if got := servicePort(t, d, "twin", "1"); got != "57301" {
			t.Errorf("%s: twin:1 has port %s, want 57301 from its version block", when, got)
		}
		assertFile(t, filepath.Join(first.Dir, ".env"), "KV_PORT=7379\n")
		assertFile(t, filepath.Join(second.Dir, ".env"), "TWIN_PORT=57301\n")
		assertFile(t, filepath.Join(wt.Dir, ".env"), "TWIN_PORT=57301\n")
	}
	check("after Linking")
	assertServed(t, d, 7379, "laneway-kv-1")
	assertServed(t, d, 57301, "laneway-twin-1")

	d.MustRunCLI(second.Dir, "link", "--yes")
	check("after Linking the second Project again")

	d.Restart()
	check("after the daemon restarted")
	assertServed(t, d, 7379, "laneway-kv-1")
	assertServed(t, d, 57301, "laneway-twin-1")
}

func TestARegistryWrittenByAnEarlierLanewayKeepsEveryRecordedPort(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Stop()
	// An earlier Laneway gave only kv's default version the standard port,
	// and versions 2 of kv and twin ports in their shared block, in the
	// order they were registered.
	writeFile(t, d.Paths.Registry(), `{"version": 1, "projects": [], "services": [
		{"engine": "kv", "version": "1", "port": 7379},
		{"engine": "twin", "version": "2", "port": 57302},
		{"engine": "kv", "version": "2", "port": 57303}
	]}`)
	d.Start()
	addSharedPortPlugins(t, d)

	for _, p := range []struct{ name, services string }{
		{"earlier-twin-1", `twin = "1"`},
		{"earlier-kv-1", `kv = "1"`},
		{"earlier-kv-2", `kv = "2"`},
		{"earlier-twin-2", `twin = "2"`},
	} {
		d.MustRunCLI(serviceProject(t, p.name, p.services, "").Dir, "link", "--yes")
	}

	for _, s := range []struct{ engine, version, port string }{
		{"kv", "1", "7379"},
		{"twin", "2", "57302"},
		{"kv", "2", "57303"},
		{"twin", "1", "57301"},
	} {
		if got := servicePort(t, d, s.engine, s.version); got != s.port {
			t.Errorf("%s:%s has port %s, want %s", s.engine, s.version, got, s.port)
		}
	}
}
