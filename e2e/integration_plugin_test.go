package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// Integrations from Plugins through Seam 1, with the layers test Plugin
// (internal/testkit/testplugins/layers): each of its Integrations returns the
// layer the Project's <name>.json spells out, and its layered Recipe the one
// in layered.json.

// layerJSON is a layer as the layers test Plugin reads it.
func layerJSON(t *testing.T, layer map[string]any) string {
	t.Helper()
	data, err := json.Marshal(layer)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAnIntegrationAPluginAddsRunsItsProcessesAlongsideTheRecipes(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "procfile"))
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "layers"))
	repo := procfileRepo(t, "assets-app", "web: "+server+" --name assets-web\n").
		WriteFile("assets.json", layerJSON(t, map[string]any{"processes": map[string]any{
			"assets": map[string]any{"command": server + " --name assets-server", "http": true},
		}})).
		Commit("an assets server")

	d.MustRunCLI(repo.Dir, "link", "--yes")

	if web := d.GetServer("https://assets-app.localhost/"); web.Name != "assets-web" {
		t.Errorf("the Domain is served by %q, want the Recipe's web Process", web.Name)
	}
	if assets := d.GetServer("https://assets.assets-app.localhost/"); assets.Name != "assets-server" {
		t.Errorf("assets.<domain> is served by %q, want the assets Integration's Process", assets.Name)
	}
	info := d.MustRunCLI(repo.Dir, "info").Stdout
	assertLineWith(t, info, "Integrations", "assets,", "because", "assets.json", "exists")
}

func TestAddingAPluginShowsTheIntegrationsItAddsAndTheListNamesThem(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := testkit.Plugin(t, "layers")

	res := d.MustRunCLI(d.Home, "plugin", "add", "--yes", plugin)

	assertContains(t, res.Stdout, "adds the Integration assets, for Projects where assets.json exists")
	assertContains(t, res.Stdout, "adds the Integration queue, for Projects where queue.json exists")
	list := d.MustRunCLI(d.Home, "plugin", "list").Stdout
	if fields := strings.Fields(lineWith(t, list, "layers")); !slices.Equal(fields, []string{"layers", "0.1.0", plugin, "Integration", "assets,", "Integration", "queue"}) {
		t.Errorf("lane plugin list shows %q for the layers Plugin, want its name, version, directory and Integrations:\n%s", fields, list)
	}
}

// queuePlugin is the layers test Plugin as another Plugin, mailer, adding
// only the queue Integration, so that two Plugins add Integrations.
func queuePlugin(t *testing.T) string {
	t.Helper()
	return withManifest(t, testkit.Plugin(t, "layers"), `{"name": "mailer", "version": "0.1.0", "api_version": 1,
  "integrations": [{"name": "queue", "detect": {"file": "queue.json"}}]}`)
}

// assetsPlugin is the layers test Plugin as the Plugin styles, adding only
// the assets Integration.
func assetsPlugin(t *testing.T) string {
	t.Helper()
	return withManifest(t, testkit.Plugin(t, "layers"), `{"name": "styles", "version": "0.1.0", "api_version": 1,
  "integrations": [{"name": "assets", "detect": {"file": "assets.json"}}]}`)
}

func TestIntegrationsFromTwoPluginsSettingTheSameKeyFailTheLinkNamingBothPlugins(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "procfile"))
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", assetsPlugin(t))
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", queuePlugin(t))
	repo := procfileRepo(t, "conflict-app", "web: sleep 60\n").
		WriteFile("assets.json", `{"env": {"APP_ENV": "assets"}}`).
		WriteFile("queue.json", `{"env": {"APP_ENV": "queue"}}`).
		Commit("two Integrations setting APP_ENV")

	res := d.RunCLI(repo.Dir, "link", "--yes")

	if res.ExitCode == 0 {
		t.Fatalf("linking with conflicting Integrations exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `the assets Integration (Plugin styles) and the queue Integration (Plugin mailer) both set env.APP_ENV, to "assets" and "queue"`)
	assertContains(t, res.Stderr, "set env.APP_ENV in laneway.toml to settle it")
}

func TestLanewayTomlSettlesIntegrationsFromTwoPluginsSettingTheSameKey(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "procfile"))
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", assetsPlugin(t))
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", queuePlugin(t))
	repo := procfileRepo(t, "settled-app", "web: sleep 60\n").
		WriteFile("assets.json", `{"env": {"APP_ENV": "assets"}}`).
		WriteFile("queue.json", `{"env": {"APP_ENV": "queue"}}`).
		WriteFile("laneway.toml", "[env]\nAPP_ENV = \"local\"\n").
		Commit("laneway.toml settles APP_ENV")

	d.MustRunCLI(repo.Dir, "link", "--yes")

	assertContains(t, readFile(t, filepath.Join(repo.Dir, ".env")), "APP_ENV=local")
}

func TestIntegrationLayersApplyAfterTheRecipesInIntegrationNameOrderWhicheverPluginAddsThem(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", withManifest(t, testkit.Plugin(t, "layers"), `{"name": "base", "version": "0.1.0", "api_version": 1,
  "recipes": [{"name": "layered", "detect": {"file": "layered.json"}}]}`))
	// mailer adds queue and styles adds assets: Plugin names sort the other
	// way from their Integrations'.
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", queuePlugin(t))
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", assetsPlugin(t))
	repo := gitfixture.New(t, "ordered-app").
		WriteFile("layered.json", layerJSON(t, map[string]any{
			"env":       map[string]any{"SOURCE": "recipe", "RECIPE": "yes"},
			"processes": map[string]any{"web": map[string]any{"command": "sleep 60", "primary": true}},
			"worktree":  map[string]any{"on_create": []string{"echo recipe >> order.txt"}},
		})).
		WriteFile("assets.json", layerJSON(t, map[string]any{
			"env":      map[string]any{"SOURCE": "assets"},
			"worktree": map[string]any{"on_create": []string{"echo assets >> order.txt"}},
		})).
		WriteFile("queue.json", layerJSON(t, map[string]any{
			"worktree": map[string]any{"on_create": []string{"echo queue >> order.txt"}},
		})).
		WriteFile("laneway.toml", "[worktree]\non_create = [\"echo project >> order.txt\"]\n").
		Commit("a layered app")

	d.MustRunCLI(repo.Dir, "link", "--yes")

	order := filepath.Join(repo.Dir, "order.txt")
	testkit.WaitUntil(t, 10*time.Second, "every setup step", func() bool {
		data, _ := os.ReadFile(order)
		return strings.Count(string(data), "\n") == 4
	})
	if got, want := readFile(t, order), "recipe\nassets\nqueue\nproject\n"; got != want {
		t.Errorf("setup steps ran in the order\n%s\nwant the Recipe's, then the Integrations' by name, then laneway.toml's:\n%s", got, want)
	}
	env := readFile(t, filepath.Join(repo.Dir, ".env"))
	assertContains(t, env, "SOURCE=assets")
	assertContains(t, env, "RECIPE=yes")
}

func TestAPluginWithAMalformedIntegrationRuleIsRefusedNamingTheRule(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := withManifest(t, testkit.Plugin(t, "layers"), `{"name": "layers", "version": "0.1.0", "api_version": 1,
  "integrations": [{"name": "assets", "detect": {"any": []}}]}`)

	res := d.RunCLI(d.Home, "plugin", "add", "--yes", plugin)

	if res.ExitCode == 0 {
		t.Fatalf("adding a Plugin with a malformed Integration rule exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "integrations[0] (assets): detect.any")
}

func TestAPluginWhoseIntegrationHasTheNameOfAnotherPluginsIsRefused(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", assetsPlugin(t))
	assets := withManifest(t, testkit.Plugin(t, "layers"), `{"name": "layers", "version": "0.1.0", "api_version": 1,
  "integrations": [{"name": "assets", "detect": {"file": "webpack.config.js"}}]}`)

	res := d.RunCLI(d.Home, "plugin", "add", "--yes", assets)

	if res.ExitCode == 0 {
		t.Errorf("adding a Plugin whose Integration's name is taken exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "Plugin layers adds Integration assets, but Plugin styles already has an Integration of that name")
}

func TestAnIntegrationPluginChoosingThePrimaryProcessFailsTheLinkNamingThePlugin(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "procfile"))
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", assetsPlugin(t))
	repo := procfileRepo(t, "takeover-app", "web: sleep 60\n").
		WriteFile("assets.json", `{"processes": {"assets": {"command": "sleep 60", "primary": true}}}`).
		Commit("an Integration taking the Domain")

	res := d.RunCLI(repo.Dir, "link", "--yes")

	if res.ExitCode == 0 {
		t.Fatalf("linking with an Integration choosing the Primary Process exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "the assets Integration (Plugin styles): processes.assets.primary: only a Recipe or laneway.toml chooses the Primary Process")
}

func TestInitWritesTheLayerOfAnInstalledPluginsIntegrationWithoutTheDaemon(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "procfile"))
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", assetsPlugin(t))
	d.Stop()
	repo := procfileRepo(t, "assets-init", "web: serve --port {port}\n").
		WriteFile("assets.json", `{"processes": {"assets": {"command": "build --watch"}}}`).
		Commit("an assets watcher")

	d.MustRunCLI(repo.Dir, "init")

	toml := readFile(t, filepath.Join(repo.Dir, "laneway.toml"))
	assertContains(t, toml, "the procfile Recipe's and the assets Integration's defaults")
	assertContains(t, toml, `command = "build --watch"`)
}
