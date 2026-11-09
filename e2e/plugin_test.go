package e2e

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/daemon"
	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// Plugins through Seam 1: test Plugins are small Go programs testkit builds
// for wasip1 (internal/testkit/testplugins), added from their directory with
// `lane plugin add`.

// procfileRepo is a Project the procfile test Plugin's Recipe detects: its
// Procfile declares the Processes, one `name: command` per line.
func procfileRepo(t *testing.T, name, procfile string) *gitfixture.Repo {
	t.Helper()
	return gitfixture.New(t, name).WriteFile("Procfile", procfile).Commit("a Procfile app")
}

func TestAProjectARecipePluginDetectsRunsTheProcessesTheRecipeReturns(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "procfile"))
	repo := procfileRepo(t, "procfile-app", "web: "+server+" --name procfile-web\n")

	d.MustRunCLI(repo.Dir, "link", "--yes")

	if web := d.GetServer("https://procfile-app.localhost/"); web.Name != "procfile-web" {
		t.Errorf("the Domain is served by %q, want the procfile Recipe's web Process", web.Name)
	}
}

// withManifest replaces the manifest of the Plugin in dir.
func withManifest(t *testing.T, dir, manifest string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAddingAPluginShowsWhatItMayDoAndAddsNothingUnlessConfirmed(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := withManifest(t, testkit.Plugin(t, "procfile"), `{
  "name": "procfile",
  "version": "0.2.0",
  "api_version": 1,
  "recipes": [{"name": "procfile", "detect": {"any": [{"file": "Procfile"}, {"file": "Procfile.dev"}]}}],
  "capabilities": {"effects": ["exec", "http"], "http": ["ntfy.sh"]}
}`)

	res := d.RunCLIWithInput(d.Home, "n\n", "plugin", "add", plugin)

	if res.ExitCode == 0 {
		t.Fatalf("declining exited 0:\n%s", res.Output())
	}
	for _, want := range []string{
		"Plugin procfile 0.2.0, from " + plugin,
		"adds the Recipe procfile, for Projects where Procfile exists or Procfile.dev exists",
		"may run commands in Worktrees",
		"may send HTTP requests, to ntfy.sh",
		"Add it? [y/N]",
		"nothing was added; run `lane plugin add --yes <dir>`",
	} {
		assertContains(t, res.Output(), want)
	}
	if list := d.MustRunCLI(d.Home, "plugin", "list"); !strings.Contains(list.Stdout, "No Plugins are installed") {
		t.Errorf("after declining, lane plugin list says:\n%s", list.Stdout)
	}

	res = d.RunCLIWithInput(d.Home, "y\n", "plugin", "add", plugin)
	if res.ExitCode != 0 {
		t.Fatalf("confirming exited %d:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stdout, "Added Plugin procfile 0.2.0")
}

func TestPluginListShowsEachInstalledPluginsVersionSourceAndWhatItAdds(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := testkit.Plugin(t, "procfile")
	d.MustRunCLI(filepath.Dir(plugin), "plugin", "add", "--yes", "./procfile")

	list := d.MustRunCLI(d.Home, "plugin", "list").Stdout

	if fields := strings.Fields(lineWith(t, list, "procfile")); !slices.Equal(fields, []string{"procfile", "0.1.0", plugin, "Recipe", "procfile"}) {
		t.Errorf("lane plugin list shows %q for the procfile Plugin, want its name, version, directory and Recipe:\n%s", fields, list)
	}
}

// lineWith is the first line of out starting with prefix.
func lineWith(t *testing.T, out, prefix string) string {
	t.Helper()
	for line := range strings.Lines(out) {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("no line starts with %q in:\n%s", prefix, out)
	return ""
}

func TestAPluginTargetingAnotherPluginAPIVersionIsRefused(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := withManifest(t, testkit.Plugin(t, "procfile"), `{
  "name": "procfile",
  "version": "2.0.0",
  "api_version": 2,
  "recipes": [{"name": "procfile", "detect": {"file": "Procfile"}}]
}`)

	res := d.RunCLI(d.Home, "plugin", "add", "--yes", plugin)

	if res.ExitCode == 0 {
		t.Fatalf("adding a Plugin for Plugin API 2 exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "Plugin procfile targets Plugin API version 2, but this Laneway speaks only version 1")
	assertContains(t, res.Stderr, "update Laneway")
	if list := d.MustRunCLI(d.Home, "plugin", "list"); !strings.Contains(list.Stdout, "No Plugins are installed") {
		t.Errorf("the refused Plugin is listed:\n%s", list.Stdout)
	}
}

func TestAPluginWithAMalformedDetectionRuleIsRefusedNamingTheRule(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := withManifest(t, testkit.Plugin(t, "procfile"), `{
  "name": "procfile",
  "version": "0.1.0",
  "api_version": 1,
  "recipes": [{"name": "procfile", "detect": {"all": [{"file": "Procfile"}, {"file": "Gemfile", "gem": "foreman"}]}}]
}`)

	res := d.RunCLI(d.Home, "plugin", "add", "--yes", plugin)

	if res.ExitCode == 0 {
		t.Fatalf("adding a Plugin with a malformed rule exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "recipes[0] (procfile): detect.all[1]: a detection rule sets exactly one of file, composer, gem, package, all or any, but this one sets file and gem")
}

func TestInfoSaysWhichRuleOfAPluginsRecipeDetectedTheProject(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "procfile"))
	repo := procfileRepo(t, "procfile-info", "web: sleep 60\nworker: sleep 60\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	info := d.MustRunCLI(repo.Dir, "info").Stdout

	assertContains(t, info, "Recipe     procfile, because Procfile exists")
	assertContains(t, info, "worker")
}

func TestEditingAFileAPluginsRecipeReadChangesWhatItReturns(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "procfile"))
	repo := procfileRepo(t, "procfile-edit", "web: sleep 60\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	if info := d.MustRunCLI(repo.Dir, "info").Stdout; strings.Contains(info, "worker") {
		t.Fatalf("lane info shows a worker before the Procfile has one:\n%s", info)
	}

	repo.WriteFile("Procfile", "web: sleep 60\nworker: sleep 60\n")

	assertContains(t, d.MustRunCLI(repo.Dir, "info").Stdout, "worker")
}

func TestAnAddedPluginKeepsWorkingWhenItsSourceDirectoryIsGone(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	plugin := testkit.Plugin(t, "procfile")
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", plugin)
	if err := os.RemoveAll(plugin); err != nil {
		t.Fatal(err)
	}
	d.Restart()
	repo := procfileRepo(t, "procfile-gone", "web: "+server+" --name procfile-web\n")

	d.MustRunCLI(repo.Dir, "link", "--yes")

	if web := d.GetServer("https://procfile-gone.localhost/"); web.Name != "procfile-web" {
		t.Errorf("the Domain is served by %q, want the procfile Recipe's web Process", web.Name)
	}
}

// misbehaveRepo is a Project the misbehave test Plugin's Recipe detects, told
// to do what.
func misbehaveRepo(t *testing.T, name, what string) *gitfixture.Repo {
	t.Helper()
	return gitfixture.New(t, name).WriteFile(".gitignore", ".env\n").WriteFile("misbehave", what+"\n").Commit("a misbehaving app")
}

func TestARecipePluginThatTakesTooLongIsStoppedAndTheLinkFails(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t, func(c *daemon.Config) { c.PluginTimeout = time.Second })
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "misbehave"))
	repo := misbehaveRepo(t, "hangs", "hang")

	res := d.RunCLI(repo.Dir, "link", "--yes")

	if res.ExitCode == 0 {
		t.Fatalf("linking a Project whose Recipe hangs exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "the misbehave Recipe (Plugin misbehave): took longer than 1s, so it was stopped")
}

func TestARecipePluginThatFailsFailsTheLinkWithWhatItWroteToStderr(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "misbehave"))
	repo := misbehaveRepo(t, "fails", "fail")

	res := d.RunCLI(repo.Dir, "link", "--yes")

	if res.ExitCode == 0 {
		t.Fatalf("linking a Project whose Recipe fails exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "the misbehave Recipe (Plugin misbehave): its module exited with status 3:\nmisbehave: something broke")
}

func TestAPluginSeesTheProjectReadOnly(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "misbehave"))
	repo := misbehaveRepo(t, "writes", "write")

	d.MustRunCLI(repo.Dir, "link", "--yes")

	if _, err := os.Stat(filepath.Join(repo.Dir, "written-by-plugin")); err == nil {
		t.Error("the Plugin wrote a file in the Project")
	}
	if got := envValue(t, filepath.Join(repo.Dir, ".env"), "WRITE_RESULT"); got == "written" || got == "" {
		t.Errorf("the Plugin's write ended with %q, want an error", got)
	}
}

func TestWhatAPluginWritesToStderrGoesToTheDaemonsLog(t *testing.T) {
	t.Parallel()
	log := &syncBuffer{}
	d := testkit.StartDaemon(t, func(c *daemon.Config) { c.Logger = slog.New(slog.NewTextHandler(log, nil)) })
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "misbehave"))
	repo := misbehaveRepo(t, "chatty", "hello from misbehave")

	d.MustRunCLI(repo.Dir, "link", "--yes")

	assertContains(t, log.String(), `plugin=misbehave stderr="hello from misbehave"`)
}

// syncBuffer is a bytes.Buffer safe to write from the daemon's goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestInitWritesTheLayerOfAnInstalledPluginsRecipeWithoutTheDaemon(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "procfile"))
	d.Stop()
	repo := procfileRepo(t, "procfile-init", "web: serve --port {port}\nworker: work\n")

	d.MustRunCLI(repo.Dir, "init")

	toml := readFile(t, filepath.Join(repo.Dir, "laneway.toml"))
	assertContains(t, toml, "the procfile Recipe's defaults")
	assertContains(t, toml, `command = "work"`)
}

func TestAddingAPluginAgainReplacesTheInstalledOne(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := testkit.Plugin(t, "procfile")
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", plugin)
	withManifest(t, plugin, `{"name": "procfile", "version": "0.2.0", "api_version": 1, "recipes": [{"name": "procfile", "detect": {"file": "Procfile"}}]}`)

	res := d.MustRunCLI(d.Home, "plugin", "add", "--yes", plugin)

	assertContains(t, res.Stdout, "replaces the installed procfile 0.1.0")
	assertContains(t, lineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "procfile"), "0.2.0")
}

func TestAPluginWhoseRecipeHasTheNameOfLanewaysOwnIsRefused(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := withManifest(t, testkit.Plugin(t, "procfile"), `{"name": "procfile", "version": "0.1.0", "api_version": 1, "recipes": [{"name": "laravel", "detect": {"file": "artisan"}}]}`)

	res := d.RunCLI(d.Home, "plugin", "add", "--yes", plugin)

	if res.ExitCode == 0 {
		t.Fatalf("adding a second laravel Recipe exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "Plugin procfile adds Recipe laravel, but Laneway already has a Recipe of that name")
}
