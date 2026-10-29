package e2e

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// The Laravel Recipe's vite Process through Seam 1: the Laravel-shaped
// Project of laravel_test.go using laravel-vite-plugin, and a fake vite.

// laravelViteRepo is laravelRepo with laravel-vite-plugin in package.json
// and the vite.config.js a fresh Laravel app has.
func laravelViteRepo(t *testing.T, name string) *gitfixture.Repo {
	t.Helper()
	return laravelRepo(t, name).
		WriteFile("package.json", `{"private": true, "type": "module", "devDependencies": {"laravel-vite-plugin": "^3.1", "vite": "^8.0.0"}}`+"\n").
		WriteFile("vite.config.js", `import { defineConfig } from 'vite';
import laravel from 'laravel-vite-plugin';

export default defineConfig({
    plugins: [laravel({ input: ['resources/css/app.css', 'resources/js/app.js'], refresh: true })],
});
`).
		Commit("use laravel-vite-plugin")
}

// viteStubs are laravelStubs whose pnpm install also puts a fake vite in
// node_modules/.bin: a test server reporting its arguments and
// LARAVEL_BYPASS_ENV_CHECK as its name.
func viteStubs(t *testing.T, d *testkit.Daemon) {
	t.Helper()
	laravelStubs(t, d)
	vite := testkit.WriteExecutable(t, gitfixture.TempDir(t), "vite", `args="$*"
while [ $# -gt 0 ]; do case "$1" in --port) port="$2"; shift ;; esac; shift; done
exec `+testkit.TestServer(t)+` --port="$port" --name "vite bypass=$LARAVEL_BYPASS_ENV_CHECK $args"
`)
	testkit.WriteExecutable(t, d.Bin, "pnpm", `mkdir -p node_modules/.bin && echo "pnpm $*" > node_modules/.installed-by && cp `+
		vite+` node_modules/.bin/vite`+"\n")
}

// viteConfig is the config the fake vite at domain was started with: its
// path and contents.
func viteConfig(t *testing.T, d *testkit.Daemon, domain string) (string, string) {
	t.Helper()
	vite := d.GetServer("https://" + domain + "/@vite/client")
	for _, want := range []string{"vite bypass=1 ", " --host 127.0.0.1 ", " --port " + vite.ListenPort + " ", " --strictPort"} {
		if !strings.Contains(vite.Name, want) {
			t.Errorf("%s reached %q, want %q in it", domain, vite.Name, want)
		}
	}
	_, rest, ok := strings.Cut(vite.Name, " --config ")
	if !ok {
		t.Fatalf("%s reached %q, want a vite with --config", domain, vite.Name)
	}
	path, _, _ := strings.Cut(rest, " ")
	return path, readFile(t, path)
}

func TestALaravelAppUsingLaravelVitePluginServesViteAtViteDotItsDomain(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	viteStubs(t, d)
	repo := laravelViteRepo(t, "lara-vite")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	if state := worktreeState(t, d, "lara-vite.localhost").State; state != "stopped" {
		t.Fatalf("before any request the Worktree is %q, want stopped: vite is lazy", state)
	}
	// What a Vite killed with SIGKILL leaves behind.
	hot := filepath.Join(repo.Dir, "public", "hot")
	if err := os.WriteFile(hot, []byte("https://vite.lara-vite.localhost:443"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, config := viteConfig(t, d, "vite.lara-vite.localhost")

	if strings.HasPrefix(path, repo.Dir) {
		t.Errorf("the vite config is %s, inside the Worktree; want it outside, in Portby's state", path)
	}
	_, proxyPort, _ := net.SplitHostPort(d.ProxyAddr())
	for _, want := range []string{
		"import projectConfig from " + strconv.Quote(filepath.Join(repo.Dir, "vite.config.js")) + ";",
		"host: " + strconv.Quote("vite.lara-vite.localhost") + ",",
		"protocol: 'wss',",
		"clientPort: " + proxyPort + ",",
		"https: false,",
		"origin: undefined,",
	} {
		assertContains(t, config, want)
	}
	assertNoFile(t, hot)
	waitForState(t, d, "lara-vite.localhost", "running")
	if web := d.GetServer("https://lara-vite.localhost/"); !strings.Contains(web.Name, "artisan serve") {
		t.Errorf("the Domain reached %q, want artisan serve", web.Name)
	}

	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	_, config = viteConfig(t, d, "vite.feature.lara-vite.localhost")
	assertContains(t, config, "import projectConfig from "+strconv.Quote(filepath.Join(wt.Dir, "vite.config.js"))+";")
	assertContains(t, config, "host: "+strconv.Quote("vite.feature.lara-vite.localhost")+",")
}

func TestViteBefore8GetsItsLiveReloadSettingsAsServerHMR(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	viteStubs(t, d)
	repo := laravelViteRepo(t, "lara-vite7")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	if err := os.MkdirAll(filepath.Join(repo.Dir, "node_modules", "vite"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.Dir, "node_modules", "vite", "package.json"), []byte(`{"name": "vite", "version": "7.1.4"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, config := viteConfig(t, d, "vite.lara-vite7.localhost")

	assertContains(t, config, "hmr: hmrOff ? false : {")
	assertContains(t, config, "host: "+strconv.Quote("vite.lara-vite7.localhost")+",")
	assertNotContains(t, config, "ws: hmrOff")
}

func TestAVitePrimaryProcessGetsLiveReloadAtTheDomainItself(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	vite := testkit.WriteExecutable(t, gitfixture.TempDir(t), "vite", `exec `+testkit.TestServer(t)+` --name "vite $*"`+"\n")
	repo := gitfixture.New(t, "vite-primary").
		WriteFile("portby.toml", "[processes.web]\ncommand = \""+vite+" --config {vite_config}\"\n").
		Commit("a Vite app")
	d.MustRunCLI(repo.Dir, "link")

	web := d.GetServer("https://vite-primary.localhost/")

	_, path, _ := strings.Cut(web.Name, "--config ")
	config := readFile(t, path)
	assertContains(t, config, "host: "+strconv.Quote("vite-primary.localhost")+",")
	assertContains(t, config, "const projectConfig = {};") // it has no vite.config.*
}

func TestViteLiveReloadMovesToThePrimaryDomainWhenTheProcessRestarts(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	vite := testkit.WriteExecutable(t, gitfixture.TempDir(t), "vite", `exec `+server+` --name "vite $*"`+"\n")
	repo := gitfixture.New(t, "vite-moves").
		WriteFile("portby.toml", processConfig(server+" --name web")+
			"\n[processes.vite]\ncommand = \""+vite+" --config {vite_config}\"\nhttp = true\n").
		Commit("a web and a vite Process")
	d.MustRunCLI(repo.Dir, "link")
	wt := repo.AddWorktree("t3-298e5f25")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	liveReloadAt := func(host string) string { return "host: " + strconv.Quote(host) + "," }
	_, path, _ := strings.Cut(d.GetServer("https://vite.t3-298e5f25.vite-moves.localhost/").Name, "--config ")
	assertContains(t, readFile(t, path), liveReloadAt("vite.t3-298e5f25.vite-moves.localhost"))

	d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")

	eventuallyContains(t, path, liveReloadAt("vite.checkout.vite-moves.localhost"))
	// Stale pages on the old name still load their assets.
	if got := d.GetServer("https://vite.t3-298e5f25.vite-moves.localhost/").Name; !strings.HasPrefix(got, "vite ") {
		t.Errorf("vite.<Domain> reached %q, want the vite Process", got)
	}

	d.MustRunCLI(wt.Dir, "worktree", "alias", "--remove", "checkout")

	eventuallyContains(t, path, liveReloadAt("vite.t3-298e5f25.vite-moves.localhost"))
}

func TestAnOpenLiveReloadConnectionKeepsALaravelWorktreeFromGoingIdle(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	viteStubs(t, d)
	repo := laravelViteRepo(t, "lara-vite-idle")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	d.GetServer("https://vite.lara-vite-idle.localhost/@vite/client")
	waitForState(t, d, "lara-vite-idle.localhost", "running")
	if delay := waitForTimer(t, clk); delay != 15*time.Minute {
		t.Fatalf("Idle scheduled after %s, want the default 15m", delay)
	}

	ws := openWebSocket(t, d, "vite.lara-vite-idle.localhost")
	clk.Advance(time.Hour)

	ws.echo(t, "still there")
	if state := worktreeState(t, d, "lara-vite-idle.localhost").State; state != "running" {
		t.Fatalf("state %q after an hour with the live-reload connection open, want running", state)
	}
	ws.Close()
	// Idle is counted from when the connection closed.
	if delay := waitForTimer(t, clk); delay != 15*time.Minute {
		t.Fatalf("Idle scheduled %s after the connection closed, want 15m", delay)
	}
	clk.Advance(15 * time.Minute)
	waitForState(t, d, "lara-vite-idle.localhost", "idle")
}

// A Laravel page names Vite's address only once public/hot exists, which
// Vite writes when it starts listening: a start isn't done until the other
// HTTP Processes accept connections too.
func TestAStartWaitsForTheWorktreesOtherHTTPProcesses(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	files := gitfixture.TempDir(t)
	webUp, viteGo := filepath.Join(files, "web-up"), filepath.Join(files, "vite-go")
	repo := gitfixture.New(t, "start-waits-vite").
		WriteFile("portby.toml", fmt.Sprintf("[processes.web]\ncommand = %q\nprimary = true\n\n[processes.vite]\ncommand = %q\nhttp = true\n",
			"exec "+server+" --name web --ready-file "+webUp,
			"until [ -e "+viteGo+" ]; do sleep 0.01; done; exec "+server+" --name vite")).
		Commit("web and a vite that waits")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	loadInBackground(t, d, "https://start-waits-vite.localhost/")
	eventually(t, "the web Process to listen", func() bool { _, err := os.Stat(webUp); return err == nil })

	if state := worktreeState(t, d, "start-waits-vite.localhost").State; state != "starting" {
		t.Fatalf("with web up and vite not, the Worktree is %q, want starting", state)
	}
	if err := os.WriteFile(viteGo, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitForState(t, d, "start-waits-vite.localhost", "running")
	if vite := d.GetServer("https://vite.start-waits-vite.localhost/"); vite.Name != "vite" {
		t.Errorf("vite.<domain> reached %q, want vite", vite.Name)
	}
}

func TestAnotherHTTPProcessThatExitsDoesNotFailTheStart(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	repo := gitfixture.New(t, "start-vite-exits").
		WriteFile("portby.toml", fmt.Sprintf("[processes.web]\ncommand = %q\nprimary = true\n\n[processes.vite]\ncommand = %q\nhttp = true\n",
			"exec "+server+" --name web", "echo no node_modules; exit 1")).
		Commit("web and a vite that can't run")
	d.MustRunCLI(repo.Dir, "link")

	if web := d.GetServer("https://start-vite-exits.localhost/"); web.Name != "web" {
		t.Errorf("the Domain reached %q, want web", web.Name)
	}
}
