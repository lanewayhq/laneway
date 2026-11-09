package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// Machine-wide Plugins through Seam 1: added with `lane plugin add
// --global`, turned on only in personal config (the global config or
// laneway.local.toml). The notify Plugin here is the hooked test Plugin's
// module with a manifest that only hooks Ready and may send http effects
// to a local HTTP server: a notify-on-Ready Plugin.

// notifyPlugin is a notify-on-Ready Plugin that may reach server.
func notifyPlugin(t *testing.T, server *requestLog) string {
	t.Helper()
	return withManifest(t, testkit.Plugin(t, "hooked"), fmt.Sprintf(`{
  "name": "notify",
  "version": "0.1.0",
  "api_version": 1,
  "hooks": ["ready"],
  "capabilities": {"effects": ["http"], "http": [%q]}
}`, server.host()))
}

// notifiedRepo is a Project whose Ready hooks, if the notify Plugin is on,
// POST "<project> is Ready at <url>" to server.
func notifiedRepo(t *testing.T, name string, server *requestLog, files ...string) *gitfixture.Repo {
	t.Helper()
	repo := gitfixture.New(t, name).
		WriteFile(".gitignore", ".env\nlaneway.local.toml\n").
		WriteFile("laneway.toml", "[env]\nAPP_URL = \"{url}\"\n").
		WriteFile("hooks.json", hooksJSON(t, map[string]any{"ready": map[string]any{"effects": []any{
			map[string]any{"kind": "http", "http": map[string]any{"method": "POST", "url": server.URL + "/notify", "body": "{project} is Ready at {url}"}},
		}}}))
	for i := 0; i+1 < len(files); i += 2 {
		repo.WriteFile(files[i], files[i+1])
	}
	return repo.Commit("a Project")
}

// globalConfig is d's global config file.
func globalConfig(d *testkit.Daemon) string {
	return filepath.Join(d.Home, ".config", "laneway", "config.toml")
}

// writeGlobalConfig writes d's global config file.
func writeGlobalConfig(t *testing.T, d *testkit.Daemon, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(globalConfig(d)), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, globalConfig(d), content)
}

// bodies are the bodies of the requests server got.
func bodies(server *requestLog) []string {
	var out []string
	for _, r := range server.received() {
		out = append(out, r.Body)
	}
	slices.Sort(out)
	return out
}

func TestAMachineWidePluginTurnedOnInTheGlobalConfigNotifiesWhenAnyProjectIsReady(t *testing.T) {
	t.Parallel()
	server := newRequestLog(t)
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--global", "--yes", notifyPlugin(t, server))
	writeGlobalConfig(t, d, "plugins = [\"notify\"]\n")
	shop, blog := notifiedRepo(t, "notify-shop", server), notifiedRepo(t, "notify-blog", server)

	d.MustRunCLI(shop.Dir, "link", "--yes")
	d.MustRunCLI(blog.Dir, "link", "--yes")

	server.waitForRequests(t, 2)
	want := []string{"notify-blog is Ready at https://notify-blog.localhost", "notify-shop is Ready at https://notify-shop.localhost"}
	if got := bodies(server); !slices.Equal(got, want) {
		t.Errorf("the server got %q, want %q", got, want)
	}
}

func TestAMachineWidePluginTurnedOnInLanewayLocalTomlFiresOnlyInThatProject(t *testing.T) {
	t.Parallel()
	server := newRequestLog(t)
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--global", "--yes", notifyPlugin(t, server))
	off := notifiedRepo(t, "notify-off", server)
	on := notifiedRepo(t, "notify-on", server)
	writeTestFile(t, filepath.Join(on.Dir, "laneway.local.toml"), "plugins = [\"notify\"]\n")

	d.MustRunCLI(off.Dir, "link", "--yes")
	d.MustRunCLI(off.Dir, "worktree", "ready")
	d.MustRunCLI(on.Dir, "link", "--yes")
	d.MustRunCLI(on.Dir, "worktree", "ready")

	server.waitForRequests(t, 1)
	want := []string{"notify-on is Ready at https://notify-on.localhost"}
	if got := bodies(server); !slices.Equal(got, want) {
		t.Errorf("the server got %q, want only %q", got, want)
	}
}

func TestACommittedLanewayTomlCannotTurnOnAMachineWidePlugin(t *testing.T) {
	t.Parallel()
	server := newRequestLog(t)
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--global", "--yes", notifyPlugin(t, server))
	repo := notifiedRepo(t, "notify-committed", server, "laneway.toml", "plugins = [\"notify\"]\n")

	res := d.RunCLI(repo.Dir, "link", "--yes")

	if res.ExitCode == 0 {
		t.Fatalf("linking a Project whose laneway.toml turns on a machine-wide Plugin exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, filepath.Join(repo.Dir, "laneway.toml")+": plugins names notify, a machine-wide Plugin, "+
		"which only your own config can turn on, never a committed laneway.toml; remove it, and turn it on in laneway.local.toml "+
		"for this Project, or under plugins in "+globalConfig(d)+" for every Project")
	if got := server.received(); len(got) != 0 {
		t.Errorf("the machine-wide Plugin's hook fired: %+v", got)
	}
}

func TestAPluginNamedInLanewayTomlHasItsHooksFireThere(t *testing.T) {
	t.Parallel()
	server := newRequestLog(t)
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", notifyPlugin(t, server))
	repo := notifiedRepo(t, "notify-named", server, "laneway.toml", "plugins = [\"notify\"]\n")

	d.MustRunCLI(repo.Dir, "link", "--yes")

	server.waitForRequests(t, 1)
	want := []string{"notify-named is Ready at https://notify-named.localhost"}
	if got := bodies(server); !slices.Equal(got, want) {
		t.Errorf("the server got %q, want %q", got, want)
	}
}

func TestAddingAMachineWidePluginSaysOnlyPersonalConfigTurnsItOnAndHow(t *testing.T) {
	t.Parallel()
	server := newRequestLog(t)
	d := testkit.StartDaemon(t)
	plugin := notifyPlugin(t, server)

	res := d.MustRunCLI(d.Home, "plugin", "add", "--global", "--yes", plugin)

	for _, want := range []string{
		"Plugin notify 0.1.0, from " + plugin + "\n",
		"  machine-wide: only your global config or a Project's laneway.local.toml turns it on, never a committed laneway.toml\n",
		"  hooks Ready, in Projects that turn it on\n",
		"  may send HTTP requests, to " + server.host() + "\n",
		"Added machine-wide Plugin notify 0.1.0\n",
		"Turn it on in every Project with plugins = [\"notify\"] in " + globalConfig(d) + ", or in one Project with it in that Project's laneway.local.toml\n",
	} {
		assertContains(t, res.Stdout, want)
	}
	assertContains(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "notify (machine-wide)")
}

func TestTheGlobalConfigCannotTurnOnAPluginNotAddedWithGlobal(t *testing.T) {
	t.Parallel()
	server := newRequestLog(t)
	d := testkit.StartDaemon(t)
	plugin := notifyPlugin(t, server)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", plugin)
	writeGlobalConfig(t, d, "plugins = [\"notify\"]\n")
	repo := notifiedRepo(t, "notify-not-global", server)

	res := d.RunCLI(repo.Dir, "link", "--yes")

	if res.ExitCode == 0 {
		t.Fatalf("linking exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, globalConfig(d)+": plugins names notify, but Plugin notify wasn't added with --global, "+
		"so it can't be turned on in every Project; add it again with `lane plugin add --global "+plugin+"`, "+
		"or name it in one Project's laneway.local.toml instead")
}

func TestConfigNamingAPluginThatIsNotInstalledSaysToAddIt(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "notify-missing").
		WriteFile("laneway.toml", "[env]\nAPP_URL = \"{url}\"\n").
		WriteFile("laneway.local.toml", "plugins = [\"notify\"]\n").
		Commit("a Project")

	res := d.RunCLI(repo.Dir, "link", "--yes")

	if res.ExitCode == 0 {
		t.Fatalf("linking exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, filepath.Join(repo.Dir, "laneway.local.toml")+": plugins names notify, but no Plugin notify is installed")
	assertContains(t, res.Stderr, "`lane plugin add <dir>`")
	assertContains(t, res.Stderr, "or remove it from plugins")
}
