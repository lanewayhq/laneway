package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/clock"
	"github.com/lanewayhq/laneway/internal/daemon"
	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// Plugin updates through Seam 1: Catalog Plugins stay at the version first
// installed until `lane plugin update`, which moves them to the latest
// release that every pin in a Linked Project's laneway.toml allows.

func TestACatalogPluginStaysAtItsVersionUntilPluginUpdate(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(kvRelease(t, "1.0.0", 1))
	d := catalogDaemon(t, cat)
	d.MustRunCLI(serviceProject(t, "kv-first", `kv = "1"`, "").Dir, "link", "--yes")
	cat.Publish(kvRelease(t, "1.1.0", 1))

	// Linking again reads the new Catalog, but changes nothing installed.
	d.MustRunCLI(serviceProject(t, "kv-second", `kv = "1"`, "").Dir, "link", "--yes")
	assertLineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "kv", "1.0.0", "Catalog")

	out := d.MustRunCLI(d.Home, "plugin", "update").Stdout

	assertContains(t, out, "Updated Plugin kv 1.0.0 → 1.1.0")
	assertLineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "kv", "1.1.0", "Catalog")
}

// hookedRelease is the hooked test Plugin at version, its manifest declaring
// capabilities (a JSON object).
func hookedRelease(t *testing.T, version, capabilities string) string {
	t.Helper()
	return withManifest(t, testkit.Plugin(t, "hooked"), fmt.Sprintf(`{"name": "hooked", "version": %q, "api_version": 1,
  "recipes": [{"name": "hooked", "detect": {"file": "hooked"}}],
  "hooks": ["ready"],
  "capabilities": %s}`, version, capabilities))
}

func TestAnUpdateAskingForNewCapabilitiesNeedsConfirming(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(hookedRelease(t, "1.0.0", `{"effects": ["env", "warning"]}`))
	d := catalogDaemon(t, cat)
	d.MustRunCLI(hookedProject(t, "hooked-caps", "", "{}").Dir, "link", "--yes")
	cat.Publish(hookedRelease(t, "1.1.0", `{"effects": ["env", "exec", "http", "warning"], "http": ["ntfy.sh"]}`))

	declined := d.RunCLIWithInput(d.Home, "n\n", "plugin", "update")

	if declined.ExitCode == 0 {
		t.Fatalf("declining the update exited 0:\n%s", declined.Output())
	}
	assertContains(t, declined.Stdout, "Plugin hooked 1.0.0 → 1.1.0")
	assertContains(t, declined.Stdout, "may now run commands in Worktrees")
	assertContains(t, declined.Stdout, "may now send HTTP requests to ntfy.sh")
	if strings.Contains(declined.Stdout, "set keys in Worktrees' env files") {
		t.Errorf("the update shows a capability the installed version already has:\n%s", declined.Stdout)
	}
	assertContains(t, declined.Stderr, "nothing was updated")
	assertLineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "hooked", "1.0.0", "Catalog")

	confirmed := d.RunCLIWithInput(d.Home, "y\n", "plugin", "update")

	if confirmed.ExitCode != 0 {
		t.Fatalf("confirming the update exited %d:\n%s", confirmed.ExitCode, confirmed.Output())
	}
	assertContains(t, confirmed.Stdout, "Updated Plugin hooked 1.0.0 → 1.1.0")
	assertLineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "hooked", "1.1.0", "Catalog")
}

func TestYesSkipsConfirmingAnUpdatesNewCapabilities(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(hookedRelease(t, "1.0.0", `{"effects": ["warning"]}`))
	d := catalogDaemon(t, cat)
	d.MustRunCLI(hookedProject(t, "hooked-yes", "", "{}").Dir, "link", "--yes")
	cat.Publish(hookedRelease(t, "2.0.0", `{"effects": ["exec", "warning"]}`))

	out := d.MustRunCLI(d.Home, "plugin", "update", "hooked", "--yes").Stdout

	assertContains(t, out, "Updated Plugin hooked 1.0.0 → 2.0.0")
	assertLineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "hooked", "2.0.0", "Catalog")
}

func TestAVersionPinnedInLanewayTomlIsRespectedWhenFetchingAndUpdating(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(kvRelease(t, "1.0.0", 1))
	cat.Publish(kvRelease(t, "1.1.0", 1))
	d := catalogDaemon(t, cat)
	pinned := serviceProject(t, "kv-pinned", `kv = "1"`, "[plugin_versions]\nkv = \"~1.0\"\n")

	out := d.MustRunCLI(pinned.Dir, "link", "--yes").Stdout

	assertContains(t, out, "Fetched Plugin kv 1.0.0 from the Catalog")

	cat.Publish(kvRelease(t, "1.0.7", 1))
	cat.Publish(kvRelease(t, "2.0.0", 1))
	out = d.MustRunCLI(d.Home, "plugin", "update").Stdout

	assertContains(t, out, "Updated Plugin kv 1.0.0 → 1.0.7")
	assertLineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "kv", "1.0.7", "Catalog")
}

func TestPinsInEveryLinkedProjectMustAllowTheVersionUpdatedTo(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(kvRelease(t, "1.0.0", 1))
	d := catalogDaemon(t, cat)
	d.MustRunCLI(serviceProject(t, "kv-major", `kv = "1"`, "[plugin_versions]\nkv = \"^1.0\"\n").Dir, "link", "--yes")
	d.MustRunCLI(serviceProject(t, "kv-minor", `kv = "1"`, "[plugin_versions]\nkv = \">=1.0.0 <1.3.0\"\n").Dir, "link", "--yes")
	for _, v := range []string{"1.2.0", "1.3.0", "2.0.0"} {
		cat.Publish(kvRelease(t, v, 1))
	}

	out := d.MustRunCLI(d.Home, "plugin", "update", "kv").Stdout

	assertContains(t, out, "Updated Plugin kv 1.0.0 → 1.2.0")
}

func TestAPinNoReleaseSatisfiesLeavesThePluginAsItIsWithAWarning(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(kvRelease(t, "1.0.0", 1))
	d := catalogDaemon(t, cat)
	d.MustRunCLI(serviceProject(t, "kv-old", `kv = "1"`, "[plugin_versions]\nkv = \"1\"\n").Dir, "link", "--yes")
	d.MustRunCLI(serviceProject(t, "kv-new", `kv = "1"`, "").Dir, "link", "--yes")
	cat.Publish(kvRelease(t, "2.0.0", 1))
	repo := serviceProject(t, "kv-new-2", `kv = "1"`, "[plugin_versions]\nkv = \"2\"\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	res := d.MustRunCLI(d.Home, "plugin", "update")

	assertContains(t, res.Stdout, "Every Plugin from the Catalog is up to date.")
	assertContains(t, res.Stderr, "no release of Plugin kv is allowed by every pin")
	assertContains(t, res.Stderr, `"1" in `)
	assertContains(t, res.Stderr, `"2" in `+repo.Dir+"/laneway.toml")
	assertLineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "kv", "1.0.0", "Catalog")
}

func TestAPinThatIsntAVersionRangeIsAConfigError(t *testing.T) {
	t.Parallel()
	d := catalogDaemon(t, testkit.NewCatalog(t))
	repo := gitfixture.New(t, "bad-pin").WriteFile("laneway.toml", "[plugin_versions]\nkv = \"one point oh\"\n").Commit("bad pin")

	res := d.RunCLI(repo.Dir, "link", "--yes")

	if res.ExitCode == 0 {
		t.Fatalf("Linking with a bad pin exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "plugin_versions.kv")
	assertContains(t, res.Stderr, "isn't a version range")
}

func TestStatusAndTheDashboardAPIShowAvailablePluginUpdates(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	cat := testkit.NewCatalog(t)
	cat.Publish(kvRelease(t, "1.0.0", 1))
	d := catalogDaemon(t, cat, testkit.WithClock(clk), func(c *daemon.Config) { c.DashboardDomain = "dash-updates.localhost" })
	d.MustRunCLI(serviceProject(t, "kv-status", `kv = "1"`, "[plugin_versions]\nkv = \"1\"\n").Dir, "link", "--yes")
	if out := d.MustRunCLI(d.Home, "status").Stdout; strings.Contains(out, "lane plugin update") {
		t.Fatalf("status shows an update before there is one:\n%s", out)
	}
	cat.Publish(kvRelease(t, "1.1.0", 1))
	cat.Publish(kvRelease(t, "2.0.0", 1))
	// Laneway checks the Catalog for updates at most once an hour.
	clk.Advance(time.Hour)

	out := d.MustRunCLI(d.Home, "status").Stdout

	assertContains(t, out, "Plugin updates are available; run `lane plugin update`:")
	assertLineWith(t, out, "kv", "1.0.0", "→", "1.1.0")
	if strings.Contains(out, "2.0.0") {
		t.Errorf("status offers an update the pin doesn't allow:\n%s", out)
	}

	status, body := dashboardRequest(t, d, http.MethodGet, "https://dash-updates.localhost/api/v1/plugins", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/plugins = %d:\n%s", status, body)
	}
	var list api.PluginList
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Plugins) != 1 || list.Plugins[0].Update == nil || list.Plugins[0].Update.From != "1.0.0" || list.Plugins[0].Update.To != "1.1.0" {
		t.Fatalf("GET /api/v1/plugins doesn't show kv's update to 1.1.0:\n%s", body)
	}

	d.MustRunCLI(d.Home, "plugin", "update", "--yes")

	if out := d.MustRunCLI(d.Home, "status").Stdout; strings.Contains(out, "lane plugin update") {
		t.Fatalf("status still shows an update after updating:\n%s", out)
	}
}

// generatorRelease is the blocks test Plugin at version, from the Catalog
// for Projects using its kv Engine, with generator its one Generator.
func generatorRelease(t *testing.T, version, generator string) string {
	t.Helper()
	dir := testkit.Plugin(t, "blocks")
	writeFile(t, dir+"/kv.toml", readFile(t, testkit.Plugin(t, "kv")+"/kv.toml"))
	return withManifest(t, dir, fmt.Sprintf(`{"name": "blocks", "version": %q, "api_version": 1,
  "engines": ["kv.toml"],
  "generators": [{"name": %q}],
  "capabilities": {"effects": ["file", "warning"]}}`, version, generator))
}

// generatorProject is a Project using the blocks Plugin's kv Engine, for
// which its Generator blocks writes NOTES.md and next a newer NOTES.md.
func generatorProject(t *testing.T, name string) *gitfixture.Repo {
	t.Helper()
	return gitfixture.New(t, name).
		WriteFile("laneway.toml", "[services]\nkv = \"1\"\n").
		WriteFile("blocks.json", `[{"kind": "file", "file": {"path": "NOTES.md", "markdown": "Use lane exec.\n"}}]`).
		WriteFile("next.json", `[{"kind": "file", "file": {"path": "NOTES.md", "markdown": "Use lane exec and lane logs.\n"}}]`).
		Commit("generated notes")
}

func TestAfterAnUpdateLanewayReportsOutOfDateGeneratedFilesAndLeavesThemAlone(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(generatorRelease(t, "1.0.0", "blocks"))
	d := catalogDaemon(t, cat)
	generated := generatorProject(t, "notes-generated")
	d.MustRunCLI(generated.Dir, "link", "--yes")
	d.MustRunCLI(generated.Dir, "generate", "blocks")
	notes := readFile(t, generated.Dir+"/NOTES.md")
	// A Project that never ran the Generator has nothing out of date.
	never := generatorProject(t, "notes-never")
	d.MustRunCLI(never.Dir, "link", "--yes")
	writeFile(t, never.Dir+"/NOTES.md", "My own notes.\n")
	cat.Publish(generatorRelease(t, "1.1.0", "next"))

	out := d.MustRunCLI(d.Home, "plugin", "update").Stdout

	assertContains(t, out, "Updated Plugin blocks 1.0.0 → 1.1.0")
	assertContains(t, out, "Generated files are out of date; Laneway doesn't change them itself.")
	assertContains(t, out, "In "+generated.Dir+", run `lane generate blocks` to update NOTES.md")
	if strings.Contains(out, never.Dir) {
		t.Errorf("the update reports files the Generator never wrote:\n%s", out)
	}
	assertFile(t, generated.Dir+"/NOTES.md", notes)

	again := d.MustRunCLI(generated.Dir, "generate", "blocks")

	assertContains(t, again.Stdout, "Updated NOTES.md")
	assertContains(t, readFile(t, generated.Dir+"/NOTES.md"), "Use lane exec and lane logs.")
}

func TestAPluginAddedByHandIsntUpdatedFromTheCatalog(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(kvRelease(t, "9.0.0", 1))
	d := catalogDaemon(t, cat)
	local := testkit.Plugin(t, "kv")
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", local)

	all := d.MustRunCLI(d.Home, "plugin", "update").Stdout
	named := d.RunCLI(d.Home, "plugin", "update", "kv")
	unknown := d.RunCLI(d.Home, "plugin", "update", "nope")

	assertContains(t, all, "Every Plugin from the Catalog is up to date.")
	if named.ExitCode == 0 {
		t.Fatalf("updating a hand-added Plugin exited 0:\n%s", named.Output())
	}
	assertContains(t, named.Stderr, "Plugin kv was added by hand from "+local)
	assertContains(t, named.Stderr, "lane plugin add")
	if unknown.ExitCode == 0 {
		t.Fatalf("updating an unknown Plugin exited 0:\n%s", unknown.Output())
	}
	assertContains(t, unknown.Stderr, "no Plugin named nope is installed")
	assertLineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "kv", "0.1.0", local)
}
