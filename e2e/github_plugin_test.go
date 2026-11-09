package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// Third-party Plugins from GitHub through Seam 1: each test serves its own
// GitHub releases (testkit.NewGitHub) and points the daemon at them, so no
// test reaches the real GitHub.

// moduleSHA256 is the hash of the module of the Plugin in dir.
func moduleSHA256(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "plugin.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestAddingAPluginFromGitHubInstallsItsLatestReleasePinnedToItsHash(t *testing.T) {
	t.Parallel()
	gh := testkit.NewGitHub(t)
	plugin := testkit.Plugin(t, "procfile")
	gh.Release(t, "acme/procfile", "v0.1.0", plugin)
	d := testkit.StartDaemon(t, gh.Option())
	server := testkit.TestServer(t)

	out := d.MustRunCLI(d.Home, "plugin", "add", "--yes", "github.com/acme/procfile").Stdout

	assertContains(t, out, "Plugin procfile 0.1.0, from github.com/acme/procfile")
	assertContains(t, out, "Added Plugin procfile 0.1.0")
	assertContains(t, out, "pinned to SHA-256 "+moduleSHA256(t, plugin))
	assertLineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "procfile", "0.1.0", "github.com/acme/procfile")
	repo := procfileRepo(t, "github-procfile-app", "web: "+server+" --name gh-web\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	if web := d.GetServer("https://github-procfile-app.localhost/"); web.Name != "gh-web" {
		t.Errorf("the Domain is served by %q, want the web Process of the Recipe added from GitHub", web.Name)
	}
}

// procfileRelease is a release of the procfile Plugin at version whose
// module isn't the procfile test Plugin's, so its hash differs.
func procfileRelease(t *testing.T, version string) string {
	t.Helper()
	return withManifest(t, testkit.Plugin(t, "layers"), `{"name": "procfile", "version": "`+version+`", "api_version": 1,
  "recipes": [{"name": "procfile", "detect": {"file": "Procfile"}}]}`)
}

func TestANewGitHubReleaseIsntPickedUpUntilPluginUpdateWhichShowsItsNewHash(t *testing.T) {
	t.Parallel()
	gh := testkit.NewGitHub(t)
	old := testkit.Plugin(t, "procfile")
	gh.Release(t, "acme/procfile", "v0.1.0", old)
	d := testkit.StartDaemon(t, gh.Option())
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", "github.com/acme/procfile")
	newer := procfileRelease(t, "0.2.0")
	gh.Release(t, "acme/procfile", "v0.2.0", newer)
	d.Restart()

	assertLineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "procfile", "0.1.0", "github.com/acme/procfile")
	declined := d.RunCLIWithInput(d.Home, "n\n", "plugin", "update", "procfile")

	if declined.ExitCode == 0 {
		t.Fatalf("declining the update exited 0:\n%s", declined.Output())
	}
	for _, want := range []string{
		"Plugin procfile 0.2.0, from github.com/acme/procfile",
		"replaces the installed procfile 0.1.0",
		"SHA-256 " + moduleSHA256(t, old) + " becomes " + moduleSHA256(t, newer),
		"Update it? [y/N]",
	} {
		assertContains(t, declined.Output(), want)
	}
	assertLineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "procfile", "0.1.0")

	out := d.MustRunCLI(d.Home, "plugin", "update", "--yes", "procfile").Stdout
	assertContains(t, out, "Updated Plugin procfile to 0.2.0, pinned to SHA-256 "+moduleSHA256(t, newer))
	assertLineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "procfile", "0.2.0", "github.com/acme/procfile")
	assertContains(t, d.MustRunCLI(d.Home, "plugin", "update", "procfile").Stdout,
		"Plugin procfile is up to date: 0.2.0, SHA-256 "+moduleSHA256(t, newer))
}

// namesPlugins is a repository whose committed laneway.toml names Plugins.
func namesPlugins(t *testing.T, name, plugins string) *gitfixture.Repo {
	t.Helper()
	return gitfixture.New(t, name).WriteFile("laneway.toml", "plugins = ["+plugins+"]\n").Commit("name Plugins")
}

func TestLinkingAProjectWhoseConfigNamesAPluginOutsideTheCatalogSaysToAddItByHandAndFetchesNothing(t *testing.T) {
	t.Parallel()
	d := catalogDaemon(t, fullCatalog(t))
	repo := namesPlugins(t, "names-notify", `"notify"`)

	res := d.RunCLI(repo.Dir, "link", "--yes")

	if res.ExitCode == 0 {
		t.Fatalf("Linking a Project naming a Plugin that isn't installed exited 0:\n%s", res.Output())
	}
	for _, want := range []string{
		filepath.Join(repo.Dir, "laneway.toml") + ": plugins names notify, but no Plugin notify is installed, and it isn't in the Catalog",
		"`lane plugin add github.com/<owner>/<repo>`",
	} {
		assertContains(t, res.Stderr, want)
	}
	if got := installedPlugins(t, d); len(got) != 0 {
		t.Fatalf("installed Plugins = %v, want none", got)
	}

	d.MustRunCLI(d.Home, "plugin", "add", "--yes", withManifest(t, testkit.Plugin(t, "procfile"),
		`{"name": "notify", "version": "0.1.0", "api_version": 1, "recipes": [{"name": "notify", "detect": {"file": "NOTIFY"}}]}`))
	d.MustRunCLI(repo.Dir, "link", "--yes")
}

func TestLinkingAProjectWhoseConfigNamesACatalogPluginFetchesIt(t *testing.T) {
	t.Parallel()
	d := catalogDaemon(t, fullCatalog(t))
	repo := namesPlugins(t, "names-layers", `"layers"`)

	out := d.MustRunCLI(repo.Dir, "link", "--yes").Stdout

	assertContains(t, out, "Fetched Plugin layers 0.1.0 from the Catalog")
	if got := installedPlugins(t, d); !slices.Equal(got, []string{"layers"}) {
		t.Fatalf("installed Plugins = %v, want only layers", got)
	}
}

func TestLinkingOfflineAProjectWhoseConfigNamesAPluginNotInstalledSaysHowToGetIt(t *testing.T) {
	t.Parallel()
	cat := fullCatalog(t)
	cat.Offline()
	d := catalogDaemon(t, cat)
	repo := namesPlugins(t, "names-offline", `"notify"`)

	res := d.RunCLI(repo.Dir, "link", "--yes")

	if res.ExitCode == 0 {
		t.Fatalf("Linking offline a Project naming a Plugin that isn't installed exited 0:\n%s", res.Output())
	}
	for _, want := range []string{
		filepath.Join(repo.Dir, "laneway.toml") + ": plugins names notify, but no Plugin notify is installed",
		"couldn't check whether the Catalog has it",
		"run `lane link` again",
		"`lane plugin add github.com/<owner>/<repo>`",
	} {
		assertContains(t, res.Stderr, want)
	}
}

func TestAddingAPluginFromOneGitHubReleaseKeepsItThereThroughUpdates(t *testing.T) {
	t.Parallel()
	gh := testkit.NewGitHub(t)
	gh.Release(t, "acme/procfile", "v0.1.0", testkit.Plugin(t, "procfile"))
	gh.Release(t, "acme/procfile", "v0.2.0", procfileRelease(t, "0.2.0"))
	d := testkit.StartDaemon(t, gh.Option())

	d.MustRunCLI(d.Home, "plugin", "add", "--yes", "github.com/acme/procfile@v0.1.0")

	assertLineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "procfile", "0.1.0", "github.com/acme/procfile@v0.1.0")
	assertContains(t, d.MustRunCLI(d.Home, "plugin", "update", "--yes").Stdout, "Plugin procfile is up to date: 0.1.0")
}

func TestAddingAPluginFromAGitHubRepositoryWithoutAReleaseSaysWhatAReleaseNeeds(t *testing.T) {
	t.Parallel()
	gh := testkit.NewGitHub(t)
	d := testkit.StartDaemon(t, gh.Option())

	res := d.RunCLI(d.Home, "plugin", "add", "--yes", "github.com/acme/nothing")

	if res.ExitCode == 0 {
		t.Fatalf("adding from a repository without releases exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "github.com/acme/nothing has no latest release with a plugin.tar.gz asset")
	if got := installedPlugins(t, d); len(got) != 0 {
		t.Fatalf("installed Plugins = %v, want none", got)
	}
}

func TestUpdatingAMachineWidePluginFromGitHubKeepsItMachineWide(t *testing.T) {
	t.Parallel()
	gh := testkit.NewGitHub(t)
	gh.Release(t, "acme/procfile", "v0.1.0", testkit.Plugin(t, "procfile"))
	d := testkit.StartDaemon(t, gh.Option())
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", "--global", "github.com/acme/procfile")
	gh.Release(t, "acme/procfile", "v0.2.0", procfileRelease(t, "0.2.0"))

	d.MustRunCLI(d.Home, "plugin", "update", "--yes", "procfile")

	assertLineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "procfile", "(machine-wide)", "0.2.0", "github.com/acme/procfile")
}
