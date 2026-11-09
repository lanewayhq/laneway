package e2e

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/container/containertest"
	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// The Catalog through Seam 1: each test serves its own Catalog
// (testkit.NewCatalog), signed with a test key the daemon is told to trust,
// and publishes test Plugins to it.

// catalogDaemon is a daemon that fetches from cat, whose fake container
// engine can run the kv Engine's image.
func catalogDaemon(t *testing.T, cat *testkit.Catalog, opts ...testkit.Option) *testkit.Daemon {
	t.Helper()
	d := testkit.StartDaemon(t, append([]testkit.Option{cat.Option()}, opts...)...)
	d.Engine.Serve(kvImage, containertest.Redis)
	return d
}

func TestLinkingAProjectWhoseConfigNamesAnEngineInTheCatalogFetchesItsPlugin(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(testkit.Plugin(t, "kv"))
	d := catalogDaemon(t, cat)
	repo := serviceProject(t, "kv-fetched", `kv = "1"`, "[env]\nKV_PORT = \"{kv_port}\"\n")

	out := d.MustRunCLI(repo.Dir, "link", "--yes")

	assertContains(t, out.Stdout, "Fetched Plugin kv 0.1.0 from the Catalog")
	assertContains(t, readFile(t, repo.Dir+"/.env"), "KV_PORT=7379\n")
	list := d.MustRunCLI(d.Home, "plugin", "list").Stdout
	if !strings.Contains(list, "kv") || !strings.Contains(list, "Catalog") {
		t.Fatalf("plugin list doesn't show kv from the Catalog:\n%s", list)
	}
}

// fullCatalog is a Catalog listing the procfile (a Recipe), layers
// (Integrations) and kv (an Engine) test Plugins.
func fullCatalog(t *testing.T) *testkit.Catalog {
	t.Helper()
	cat := testkit.NewCatalog(t)
	cat.Publish(testkit.Plugin(t, "procfile"))
	cat.Publish(testkit.Plugin(t, "layers"))
	cat.Publish(testkit.Plugin(t, "kv"))
	return cat
}

// installedPlugins are the names `lane plugin list` shows.
func installedPlugins(t *testing.T, d *testkit.Daemon) []string {
	t.Helper()
	var names []string
	for _, line := range strings.Split(d.MustRunCLI(d.Home, "plugin", "list").Stdout, "\n")[1:] {
		if f := strings.Fields(line); len(f) > 0 {
			names = append(names, f[0])
		}
	}
	return names
}

func TestLinkingFetchesTheCatalogRecipeAndIntegrationsThatDetectTheProjectAndNothingElse(t *testing.T) {
	t.Parallel()
	d := catalogDaemon(t, fullCatalog(t))
	repo := procfileRepo(t, "detected-app", "web: sleep 60\n").
		WriteFile("assets.json", layerJSON(t, map[string]any{"processes": map[string]any{
			"assets": map[string]any{"command": "sleep 60"},
		}})).
		Commit("assets")

	out := d.MustRunCLI(repo.Dir, "link", "--yes").Stdout

	assertContains(t, out, "Fetched Plugin procfile 0.1.0 from the Catalog")
	assertContains(t, out, "Fetched Plugin layers 0.1.0 from the Catalog")
	if got := installedPlugins(t, d); !slices.Equal(got, []string{"layers", "procfile"}) {
		t.Fatalf("installed Plugins = %v, want only layers and procfile", got)
	}
	info := d.MustRunCLI(repo.Dir, "info").Stdout
	assertLineWith(t, info, "Recipe", "procfile,", "because", "Procfile", "exists")
	assertLineWith(t, info, "Integrations", "assets,", "because", "assets.json", "exists")
}

func TestLinkingFetchesTheCatalogEnginesThatAFetchedIntegrationsConfigNames(t *testing.T) {
	t.Parallel()
	d := catalogDaemon(t, fullCatalog(t))
	repo := gitfixture.New(t, "queued-app").
		WriteFile("queue.json", layerJSON(t, map[string]any{
			"services": map[string]any{"kv": "1"},
			"env":      map[string]any{"QUEUE_PORT": "{kv_port}"},
		})).
		Commit("a queue")

	d.MustRunCLI(repo.Dir, "link", "--yes")

	if got := installedPlugins(t, d); !slices.Equal(got, []string{"kv", "layers"}) {
		t.Fatalf("installed Plugins = %v, want kv and layers", got)
	}
	assertContains(t, readFile(t, repo.Dir+"/.env"), "QUEUE_PORT=7379\n")
}

// kvRelease is the kv test Plugin with its manifest saying version and
// apiVersion.
func kvRelease(t *testing.T, version string, apiVersion int) string {
	t.Helper()
	return withManifest(t, testkit.Plugin(t, "kv"), fmt.Sprintf(
		`{"name": "kv", "version": %q, "api_version": %d, "engines": ["kv.toml"]}`, version, apiVersion))
}

func TestLinkingFetchesThePluginsLatestReleaseForThisLanewaysPluginAPI(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(kvRelease(t, "1.2.0", 1))
	cat.Publish(kvRelease(t, "1.10.0", 1))
	cat.Publish(kvRelease(t, "2.0.0", 2))
	cat.Publish(kvRelease(t, "1.9.0", 1))
	d := catalogDaemon(t, cat)
	repo := serviceProject(t, "kv-latest", `kv = "1"`, "")

	out := d.MustRunCLI(repo.Dir, "link", "--yes").Stdout

	assertContains(t, out, "Fetched Plugin kv 1.10.0 from the Catalog")
	assertLineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "kv", "1.10.0", "Catalog")
}

func TestACatalogNotSignedWithLanewaysKeyIsRefusedAndNothingIsFetchedFromIt(t *testing.T) {
	t.Parallel()
	cat := fullCatalog(t)
	cat.Forge()
	d := catalogDaemon(t, cat)
	plain := gitfixture.New(t, "plain-app").WriteFile("README", "hi\n").Commit("plain")
	needsKV := serviceProject(t, "kv-forged", `kv = "1"`, "")

	res := d.MustRunCLI(plain.Dir, "link", "--yes")
	assertContains(t, res.Stderr, "isn't signed with Laneway's Catalog key")
	failed := d.RunCLI(needsKV.Dir, "link", "--yes")

	if failed.ExitCode == 0 {
		t.Fatalf("Linking a Project needing kv from a forged Catalog exited 0:\n%s", failed.Output())
	}
	assertContains(t, failed.Stderr, `Laneway has no Service "kv"`)
	assertContains(t, failed.Stderr, "isn't signed with Laneway's Catalog key")
	if got := installedPlugins(t, d); len(got) != 0 {
		t.Fatalf("installed Plugins = %v, want none", got)
	}
}

func TestAReleaseWhoseSHA256IsntTheCatalogsIsRefused(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(testkit.Plugin(t, "kv"))
	cat.Tamper("kv", "0.1.0")
	d := catalogDaemon(t, cat)
	repo := serviceProject(t, "kv-tampered", `kv = "1"`, "")

	res := d.RunCLI(repo.Dir, "link", "--yes")

	if res.ExitCode == 0 {
		t.Fatalf("Linking with a tampered release exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "couldn't fetch: kv")
	assertContains(t, res.Stderr, "its SHA-256 is")
	if got := installedPlugins(t, d); len(got) != 0 {
		t.Fatalf("installed Plugins = %v, want none", got)
	}
	if _, err := os.Stat(repo.Dir + "/.env"); err == nil {
		t.Fatal("Linking failed but wrote .env")
	}
}

func TestLinkingOfflineFailsNamingThePluginsItCouldntFetch(t *testing.T) {
	t.Parallel()
	cat := fullCatalog(t)
	d := catalogDaemon(t, cat)
	// Linking online once leaves Laneway knowing what the Catalog lists.
	d.MustRunCLI(gitfixture.New(t, "online-app").WriteFile("README", "hi\n").Commit("plain").Dir, "link", "--yes")
	cat.Offline()
	repo := procfileRepo(t, "offline-app", "web: sleep 60\n").
		WriteFile("laneway.toml", "[services]\nkv = \"1\"\n").
		Commit("kv")

	res := d.RunCLI(repo.Dir, "link", "--yes")

	if res.ExitCode == 0 {
		t.Fatalf("Linking offline exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "this Project needs Plugins from the Catalog that Laneway couldn't fetch: kv, procfile")
	assertContains(t, res.Stderr, "run `lane link` again")
	if got := installedPlugins(t, d); len(got) != 0 {
		t.Fatalf("installed Plugins = %v, want none", got)
	}
	if out := d.MustRunCLI(d.Home, "status").Output(); strings.Contains(out, "offline-app") || !strings.Contains(out, "online-app") {
		t.Fatalf("the Project was Linked anyway:\n%s", out)
	}
}

func TestLinkingWithoutEverReachingTheCatalogSaysSoWhenTheConfigNamesAnUnknownEngine(t *testing.T) {
	t.Parallel()
	cat := fullCatalog(t)
	cat.Offline()
	d := catalogDaemon(t, cat)
	repo := serviceProject(t, "never-online", `kv = "1"`, "")

	res := d.RunCLI(repo.Dir, "link", "--yes")

	if res.ExitCode == 0 {
		t.Fatalf("Linking exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `Laneway has no Service "kv"`)
	assertContains(t, res.Stderr, "Laneway couldn't check the Catalog for Plugins this Project needs")
}

func TestLinkingAfterConfirmingTheEnvPatchShowsTheFetchedPluginsAndWarningsOnce(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(testkit.Plugin(t, "kv"))
	d := catalogDaemon(t, cat)
	repo := serviceProject(t, "kv-confirmed", `kv = "1"`, "[env]\nKV_PORT = \"{kv_port}\"\n")

	res := d.RunCLIWithInput(repo.Dir, "y\n", "link")

	if res.ExitCode != 0 {
		t.Fatalf("lane link exited %d:\n%s", res.ExitCode, res.Output())
	}
	if n := strings.Count(res.Stdout, "Fetched Plugin kv 0.1.0 from the Catalog"); n != 1 {
		t.Fatalf("the fetched Plugin is shown %d times, want once:\n%s", n, res.Stdout)
	}
	assertContains(t, readFile(t, repo.Dir+"/.env"), "KV_PORT=7379\n")

	cat.Forge()
	other := serviceProject(t, "kv-confirmed-2", `kv = "1"`, "[env]\nKV_PORT = \"{kv_port}\"\n")
	res = d.RunCLIWithInput(other.Dir, "y\n", "link")
	if n := strings.Count(res.Stderr, "isn't signed with Laneway's Catalog key"); n != 1 {
		t.Fatalf("the refused Catalog is warned about %d times, want once:\n%s", n, res.Stderr)
	}
}

func TestACatalogRecipeIsntFetchedForAProjectARecipeLanewayHasDetects(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(withManifest(t, testkit.Plugin(t, "layers"), `{"name": "layers", "version": "0.1.0", "api_version": 1,
  "recipes": [{"name": "layered", "detect": {"file": "Procfile"}}]}`))
	d := catalogDaemon(t, cat)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "procfile"))
	repo := procfileRepo(t, "hand-added-recipe", "web: sleep 60\n")

	out := d.MustRunCLI(repo.Dir, "link", "--yes").Stdout

	if strings.Contains(out, "Fetched Plugin") {
		t.Fatalf("Linking fetched a Plugin:\n%s", out)
	}
	assertLineWith(t, d.MustRunCLI(repo.Dir, "info").Stdout, "Recipe", "procfile,")
}

func TestAPluginAddedByHandIsntReplacedByTheCatalogs(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(kvRelease(t, "9.0.0", 1))
	d := catalogDaemon(t, cat)
	local := testkit.Plugin(t, "kv")
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", local)
	repo := serviceProject(t, "kv-local", `kv = "1"`, "")

	out := d.MustRunCLI(repo.Dir, "link", "--yes").Stdout

	if strings.Contains(out, "Fetched Plugin") {
		t.Fatalf("Linking fetched a Plugin:\n%s", out)
	}
	assertLineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "kv", "0.1.0", local)
}

func TestLinkingWithoutEverReachingTheCatalogWarnsThatPluginsItNeedsMayNotBeThere(t *testing.T) {
	t.Parallel()
	cat := fullCatalog(t)
	cat.Offline()
	d := catalogDaemon(t, cat)
	repo := procfileRepo(t, "never-online-procfile", "web: sleep 60\n")

	res := d.MustRunCLI(repo.Dir, "link", "--yes")

	assertContains(t, res.Stderr, "warning: Laneway has never been able to fetch the Catalog")
	assertContains(t, res.Stderr, "so it couldn't fetch any Plugin from it this Project needs")
	assertContains(t, res.Stderr, "run `lane link` again")
}

func TestARecipeServiceWhoseEngineIsntInstalledOrInTheCatalogIsNamedAsUnknown(t *testing.T) {
	t.Parallel()
	d := catalogDaemon(t, testkit.NewCatalog(t))
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.FirstPartyPlugin(t, "laravel"))
	repo := laravelRepo(t, "lara-no-engines")

	res := d.RunCLI(repo.Dir, "link", "--yes")

	if res.ExitCode == 0 {
		t.Fatalf("Linking exited 0 without the Engines the Recipe names:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `the laravel Recipe (Plugin laravel): services.mailpit: Laneway has no Service "mailpit"`)
}
