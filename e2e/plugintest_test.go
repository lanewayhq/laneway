package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/testkit"
)

// `lane plugin test` through Seam 1: a test Plugin's directory holds test
// cases under testdata/<case>/ (a fixture Project in project/, sample events
// in events/, and the golden file golden.json), and the command runs the
// Plugin against them with no daemon.

// noDaemon is a test daemon's home with the daemon stopped, for commands
// that must work without one.
func noDaemon(t *testing.T) *testkit.Daemon {
	t.Helper()
	d := testkit.StartDaemon(t)
	d.Stop()
	return d
}

// writeCase writes files (path → content) into the test case of the Plugin
// in dir, such as "project/Procfile" or "events/ready.json".
func writeCase(t *testing.T, dir, name string, files map[string]string) string {
	t.Helper()
	root := filepath.Join(dir, "testdata", name)
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestPluginTestUpdateWritesWhatARecipeReturnsForAFixtureProjectToItsGoldenFile(t *testing.T) {
	t.Parallel()
	d := noDaemon(t)
	plugin := testkit.Plugin(t, "procfile")
	root := writeCase(t, plugin, "web-and-worker", map[string]string{
		"project/Procfile": "web: bin/server\nworker: bin/worker\n",
	})

	res := d.MustRunCLI(plugin, "plugin", "test", "--update")

	assertContains(t, res.Stdout, "updated testdata/web-and-worker/golden.json")
	golden := readFile(t, filepath.Join(root, "golden.json"))
	for _, want := range []string{
		`"recipes": {`,
		`"procfile": {`,
		`"detected": "Procfile exists"`,
		`"command": "bin/server"`,
		`"command": "bin/worker"`,
		`"primary": true`,
	} {
		assertContains(t, golden, want)
	}

	again := d.MustRunCLI(plugin, "plugin", "test")

	assertContains(t, again.Stdout, "ok   web-and-worker")
}

func TestPluginTestReportsAChangeInWhatAPluginReturnsAsADiffAndFails(t *testing.T) {
	t.Parallel()
	d := noDaemon(t)
	plugin := testkit.Plugin(t, "procfile")
	root := writeCase(t, plugin, "web", map[string]string{"project/Procfile": "web: bin/server\n"})
	d.MustRunCLI(plugin, "plugin", "test", "--update")
	accepted := readFile(t, filepath.Join(root, "golden.json"))
	writeCase(t, plugin, "web", map[string]string{"project/Procfile": "web: bin/server --verbose\n"})

	res := d.RunCLI(plugin, "plugin", "test")

	if res.ExitCode == 0 {
		t.Fatalf("lane plugin test exited 0 after the Plugin's output changed:\n%s", res.Output())
	}
	for _, want := range []string{
		"FAIL web: the Plugin returned something other than testdata/web/golden.json holds:",
		"--- testdata/web/golden.json",
		`-            "command": "bin/server",`,
		`+            "command": "bin/server --verbose",`,
		"1 of 1 test cases failed; if what the Plugin returns now is right, run `lane plugin test --update`",
	} {
		assertContains(t, res.Output(), want)
	}
	if golden := readFile(t, filepath.Join(root, "golden.json")); golden != accepted {
		t.Errorf("lane plugin test without --update changed the golden file:\n%s", golden)
	}
}

func TestPluginTestFailsACaseWithNoGoldenFileYet(t *testing.T) {
	t.Parallel()
	d := noDaemon(t)
	plugin := testkit.Plugin(t, "procfile")
	writeCase(t, plugin, "web", map[string]string{"project/Procfile": "web: bin/server\n"})

	res := d.RunCLI(plugin, "plugin", "test")

	if res.ExitCode == 0 {
		t.Fatalf("lane plugin test exited 0 with no golden file:\n%s", res.Output())
	}
	assertContains(t, res.Output(), "FAIL web: testdata/web/golden.json doesn't exist yet")
}

func TestPluginTestRecordsTheEffectsAHookReturnsForASampleEventAndWhichLanewayWouldRefuse(t *testing.T) {
	t.Parallel()
	d := noDaemon(t)
	plugin := testkit.Plugin(t, "hooked")
	root := writeCase(t, plugin, "feature-ready", map[string]string{
		"project/hooked": "",
		"project/hooks.json": `{"ready": {"effects": [
  {"kind": "exec", "command": ["notify", "{worktree} of {project} is at {url}"]},
  {"kind": "http", "message": "not declared"}
]}}`,
		"events/ready.json": `{"name": "ready",
  "project": {"name": "shop", "path": "/src/shop"},
  "worktree": {"name": "feature", "path": "/src/shop-feature", "main": false,
    "domain": "feature.shop.localhost", "url": "https://feature.shop.localhost/"}}`,
	})

	res := d.MustRunCLI(plugin, "plugin", "test", "--update")

	assertContains(t, res.Stdout, "updated testdata/feature-ready/golden.json")
	golden := readFile(t, filepath.Join(root, "golden.json"))
	for _, want := range []string{
		`"events": {`,
		`"ready": {`,
		`"kind": "exec"`,
		`"feature of shop is at https://feature.shop.localhost/"`,
		"Plugin hooked's Ready hook returned a",
		"http effect, but its manifest doesn't declare it may send HTTP requests, so Laneway refused it",
	} {
		assertContains(t, golden, want)
	}
	if strings.Contains(golden, "not declared") {
		t.Errorf("the refused http effect is in the golden file as an effect:\n%s", golden)
	}
	d.MustRunCLI(plugin, "plugin", "test")
}

func TestPluginTestFailsACaseWhoseSampleEventThePluginDoesntHook(t *testing.T) {
	t.Parallel()
	d := noDaemon(t)
	plugin := withManifest(t, testkit.Plugin(t, "hooked"), `{
  "name": "hooked",
  "version": "0.1.0",
  "api_version": 1,
  "hooks": ["ready"]
}`)
	writeCase(t, plugin, "preparing", map[string]string{
		"events/preparing.json": `{"name": "preparing", "project": {"name": "shop", "path": "/src/shop"}}`,
	})

	res := d.RunCLI(plugin, "plugin", "test", "--update")

	if res.ExitCode == 0 {
		t.Fatalf("lane plugin test --update exited 0 for an event the Plugin doesn't hook:\n%s", res.Output())
	}
	assertContains(t, res.Output(), `FAIL preparing: events/preparing.json is a "preparing" event, but Plugin hooked doesn't hook it; add it to hooks in plugin.json`)
	if _, err := os.Stat(filepath.Join(plugin, "testdata", "preparing", "golden.json")); err == nil {
		t.Error("a golden file was written for a case that couldn't be run")
	}
}

func TestPluginTestRecordsTheLayersOfOnlyTheIntegrationsThatDetectTheFixtureProject(t *testing.T) {
	t.Parallel()
	d := noDaemon(t)
	plugin := testkit.Plugin(t, "layers")
	root := writeCase(t, plugin, "assets-only", map[string]string{
		"project/assets.json": `{"processes": {"vite": {"command": "npx vite --port {port}"}}}`,
	})

	d.MustRunCLI(plugin, "plugin", "test", "--update")

	golden := readFile(t, filepath.Join(root, "golden.json"))
	for _, want := range []string{
		`"integrations": {`,
		`"assets": {`,
		`"detected": "assets.json exists"`,
		`"command": "npx vite --port {port}"`,
	} {
		assertContains(t, golden, want)
	}
	if strings.Contains(golden, `"queue"`) {
		t.Errorf("the golden file has the queue Integration, which doesn't detect the Project:\n%s", golden)
	}
}

func TestPluginTestRecordsTheFileEffectsAGeneratorReturnsForTheFixtureProject(t *testing.T) {
	t.Parallel()
	d := noDaemon(t)
	plugin := testkit.Plugin(t, "blocks")
	root := writeCase(t, plugin, "notes", map[string]string{
		"project/blocks.json": `[{"kind": "file", "file": {"path": "docs/NOTES.md", "markdown": "Use lane exec.\n"}}]`,
	})

	d.MustRunCLI(plugin, "plugin", "test", "--update")

	golden := readFile(t, filepath.Join(root, "golden.json"))
	for _, want := range []string{`"generators": {`, `"kind": "file"`, `"path": "docs/NOTES.md"`, `"markdown": "Use lane exec.\n"`} {
		assertContains(t, golden, want)
	}
	if _, err := os.Stat(filepath.Join(root, "project", "docs", "NOTES.md")); err == nil {
		t.Error("lane plugin test wrote the Generator's file into the fixture Project")
	}
}

func TestPluginTestRecordsARecipeThatFailsAndKeepsRunningTheOtherCases(t *testing.T) {
	t.Parallel()
	d := noDaemon(t)
	plugin := testkit.Plugin(t, "misbehave")
	broken := writeCase(t, plugin, "broken", map[string]string{"project/misbehave": "fail"})
	writeCase(t, plugin, "fine", map[string]string{"project/misbehave": "fine"})

	res := d.MustRunCLI(plugin, "plugin", "test", "--update")

	assertContains(t, res.Stdout, "updated testdata/broken/golden.json")
	assertContains(t, res.Stdout, "updated testdata/fine/golden.json")
	golden := readFile(t, filepath.Join(broken, "golden.json"))
	assertContains(t, golden, `"error": "the misbehave Recipe (Plugin misbehave): its module exited with status 3:\nmisbehave: something broke"`)
}

func TestPluginTestOfAPluginWithNoTestCasesSaysWhereToAddThem(t *testing.T) {
	t.Parallel()
	d := noDaemon(t)
	plugin := testkit.Plugin(t, "procfile")

	res := d.RunCLI(d.Home, "plugin", "test", plugin)

	if res.ExitCode == 0 {
		t.Fatalf("lane plugin test exited 0 with no test cases:\n%s", res.Output())
	}
	assertContains(t, res.Output(), "Plugin procfile has no test cases; add one as a directory under "+filepath.Join(plugin, "testdata"))
}
