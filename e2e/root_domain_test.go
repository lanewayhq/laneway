package e2e

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/daemon"
	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// rootDomainDaemon starts a daemon whose Root Domain is root.
func rootDomainDaemon(t *testing.T, root string) *testkit.Daemon {
	t.Helper()
	return testkit.StartDaemon(t, func(c *daemon.Config) { c.RootDomain = root })
}

func TestEveryDomainIsUnderTheRootDomain(t *testing.T) {
	t.Parallel()
	d := rootDomainDaemon(t, "rd-domains.test")
	repo := routedProject(t, "shop")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "demo")

	if got := d.GetServer("https://shop.rd-domains.test/"); got.Name != "web@shop" {
		t.Fatalf("the main Worktree's Domain reached %q, want its Primary Process", got.Name)
	}
	if got := d.GetServer("https://vite.demo.shop.rd-domains.test/"); got.Name != "vite@feature" {
		t.Fatalf("the vite host of the Worktree's Alias reached %q, want its vite Process", got.Name)
	}
	assertRedirectsTo(t, d, "https://feature.shop.rd-domains.test/orders",
		"https://demo.shop.rd-domains.test/orders")
}

func TestTheDashboardIsUnderTheRootDomain(t *testing.T) {
	t.Parallel()
	d := rootDomainDaemon(t, "rd-dashboard.test")

	page := d.Navigate("https://laneway.rd-dashboard.test/")

	if page.StatusCode != http.StatusOK || !strings.Contains(page.Body, "<title>Laneway</title>") {
		t.Fatalf("status %d, want the Dashboard:\n%s", page.StatusCode, page.Body)
	}
}

func TestPlainHTTPRedirectsNamesUnderTheRootDomain(t *testing.T) {
	t.Parallel()
	d := rootDomainDaemon(t, "rd-http.test")
	_, httpsPort, _ := net.SplitHostPort(d.ProxyAddr())
	req, err := http.NewRequest(http.MethodGet, "http://"+d.HTTPAddr()+"/cart", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "shop.rd-http.test"
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}

	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	want := "https://shop.rd-http.test:" + httpsPort + "/cart"
	if res.StatusCode != http.StatusPermanentRedirect || res.Header.Get("Location") != want {
		t.Fatalf("status %d to %q, want 308 to %q", res.StatusCode, res.Header.Get("Location"), want)
	}
}

// personalRootDomainDaemon starts a daemon whose personal config sets
// root_domain = root, as a developer would, with a Linked Project name.
func personalRootDomainDaemon(t *testing.T, root string) *testkit.Daemon {
	t.Helper()
	d := testkit.StartDaemon(t)
	writeGlobalConfig(t, d, "root_domain = \""+root+"\"\n")
	d.Restart()
	return d
}

func TestTheRootDomainInPersonalConfigIsServedAfterARestart(t *testing.T) {
	t.Parallel()
	d := personalRootDomainDaemon(t, "rd-config.example.test")
	repo := routedProject(t, "shop")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "demo")

	if got := d.GetServer("https://shop.rd-config.example.test/"); got.Name != "web@shop" {
		t.Fatalf("the main Worktree's Domain reached %q, want its Primary Process", got.Name)
	}
	if got := d.GetServer("https://vite.demo.shop.rd-config.example.test/"); got.Name != "vite@feature" {
		t.Fatalf("the Alias's vite host reached %q, want the Worktree's vite Process", got.Name)
	}
	assertRedirectsTo(t, d, "https://feature.shop.rd-config.example.test/orders",
		"https://demo.shop.rd-config.example.test/orders")
	if page := d.Navigate("https://laneway.rd-config.example.test/"); !strings.Contains(page.Body, "<title>Laneway</title>") {
		t.Fatalf("status %d, want the Dashboard:\n%s", page.StatusCode, page.Body)
	}
}

// handshake dials d's proxy for the name serverName and completes a TLS
// handshake, which needs a certificate for it.
func handshake(d *testkit.Daemon, serverName string) error {
	conn, err := tls.Dial("tcp", d.ProxyAddr(), &tls.Config{RootCAs: d.CAPool(), ServerName: serverName})
	if err == nil {
		conn.Close()
	}
	return err
}

func TestOnlyNamesLanewayServesUnderACustomRootDomainGetACertificate(t *testing.T) {
	t.Parallel()
	d := personalRootDomainDaemon(t, "rd-certs.example.test")
	repo := routedProject(t, "certs")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "demo")

	for _, name := range []string{
		"certs.rd-certs.example.test",
		"feature.certs.rd-certs.example.test",
		"demo.certs.rd-certs.example.test",
		"vite.demo.certs.rd-certs.example.test",
		"laneway.rd-certs.example.test",
		// .localhost names Laneway serves under the Root Domain get one
		// for the page naming their new URL.
		"certs.localhost",
		"vite.demo.certs.localhost",
	} {
		if err := handshake(d, name); err != nil {
			t.Errorf("%s got no certificate: %v", name, err)
		}
	}
	for _, name := range []string{
		"nope.rd-certs.example.test",
		"rd-certs.example.test",
		"queue.certs.rd-certs.example.test",
		"nope.feature.certs.rd-certs.example.test",
		"x.y.laneway.rd-certs.example.test",
		// .localhost names that move to nothing Laneway serves.
		"nope.certs.localhost",
		"rd-certs-unknown.localhost",
	} {
		if err := handshake(d, name); err == nil {
			t.Errorf("%s got a certificate, want none: Laneway serves nothing there", name)
		}
	}
}

func TestAServiceDomainIsServedUnderACustomRootDomain(t *testing.T) {
	t.Parallel()
	d := personalRootDomainDaemon(t, "rd-service.example.test")
	d.Engine.Serve(storeImage, storeServer("api"))
	d.Engine.ServePort(storeImage, 7481, storeServer("console"))
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "store"))
	repo := serviceProject(t, "rd-service-shop", `store = "1"`, "[env]\nSTORE_ENDPOINT = \"{store_endpoint}\"\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	res := d.Get("https://files.laneway.rd-service.example.test/photos/cat.jpg")

	if res.StatusCode != http.StatusOK || res.Body != "api example.test/store:1 GET /photos/cat.jpg" {
		t.Fatalf("GET the files Service Domain = %d %q, want the store's API to answer", res.StatusCode, res.Body)
	}
	assertContains(t, readFile(t, filepath.Join(repo.Dir, ".env")),
		"STORE_ENDPOINT=https://files.laneway.rd-service.example.test\n")
	if err := handshake(d, "nope.laneway.rd-service.example.test"); err == nil {
		t.Errorf("an undeclared Service Domain got a certificate, want none")
	}
}

func TestEveryNameUnderTheLocalhostRootDomainStillGetsACertificate(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)

	if err := handshake(d, "rd-unknown-name.localhost"); err != nil {
		t.Errorf("an unknown .localhost name got no certificate, want one for the not-served page: %v", err)
	}
}

func TestAProjectsConfigCantSetTheRootDomainOrListenAddresses(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	for _, key := range []string{"root_domain = \"lw.example.test\"", "listen = [\"tailscale\"]"} {
		for _, file := range []string{"laneway.toml", "laneway.local.toml"} {
			repo := gitfixture.New(t, "rd-project-config").
				WriteFile("laneway.toml", processConfig("serve")).
				Commit("declare web Process").
				WriteFile(file, key+"\n"+processConfig("serve"))

			res := d.RunCLI(repo.Dir, "link")

			if res.ExitCode == 0 {
				t.Fatalf("link succeeded with %s in %s:\n%s", key, file, res.Output())
			}
			assertContains(t, res.Stderr, file)
			assertContains(t, res.Stderr, strings.Fields(key)[0])
			assertContains(t, res.Stderr, "config.toml")
		}
	}
}

func TestAnInvalidRootDomainIsAConfigErrorAndTheLastOneIsKept(t *testing.T) {
	t.Parallel()
	d := personalRootDomainDaemon(t, "rd-invalid.example.test")
	repo := routedProject(t, "rd-invalid-shop")
	d.MustRunCLI(repo.Dir, "link")

	for _, root := range []string{"https://lw.example.test", "lw.example.test:8443", "lw.example.test/x",
		"lw.example.test.", "100.101.102.103", "LW.example.test", "lw..example.test", "-lw.example.test",
		"lw.localhost", tooLongRoot, ""} {
		writeGlobalConfig(t, d, fmt.Sprintf("root_domain = %q\n", root))
		d.Restart()

		page := d.Navigate("https://rd-invalid-shop.rd-invalid.example.test/")

		if !strings.Contains(page.Body, "root_domain") || !strings.Contains(page.Body, "config.toml") {
			t.Errorf("root_domain = %q: status %d, want the config error naming root_domain and the file:\n%s", root, page.StatusCode, page.Body)
		}
	}
}

func TestChangingTheRootDomainAndRestartingMovesEnvFilesAndInfoButNotDatabaseNames(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "rd-env").
		WriteFile(".gitignore", ".env\n.env.testing\n").
		WriteFile("laneway.toml", "[database]\nengine = \"postgres\"\ntest = \"on\"\n\n"+
			"[env]\nAPP_URL = \"{url}\"\nSESSION_DOMAIN = \"{domain}\"\nDB_DATABASE = \"{db}\"\n").
		Commit("declare a Postgres database with Test Databases").
		WriteFile(".env.testing", "APP_ENV=testing\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("t3-7a1c")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")
	eventuallyContains(t, filepath.Join(wt.Dir, ".env"), "APP_URL=https://checkout.rd-env.localhost\n")

	writeGlobalConfig(t, d, "root_domain = \"rd-env.example.test\"\n")
	d.Restart()

	for _, tt := range []struct{ dir, domain, db string }{
		{repo.Dir, "rd-env.rd-env.example.test", "rd_env"},
		{wt.Dir, "checkout.rd-env.rd-env.example.test", "rd_env__t3_7a1c"},
	} {
		for file, db := range map[string]string{".env": tt.db, ".env.testing": tt.db + "_test"} {
			env := readFile(t, filepath.Join(tt.dir, file))
			assertContains(t, env, "APP_URL=https://"+tt.domain+"\n")
			assertContains(t, env, "SESSION_DOMAIN="+tt.domain+"\n")
			assertContains(t, env, "DB_DATABASE="+db+"\n")
		}
		if info := infoJSON(t, d, tt.dir); info["url"] != "https://"+tt.domain+"/" {
			t.Errorf("lane info --json in %s gives url %v, want https://%s/", tt.dir, info["url"], tt.domain)
		}
	}
	if info := infoJSON(t, d, wt.Dir); info["domain"] != "t3-7a1c.rd-env.rd-env.example.test" {
		t.Errorf("lane info --json gives domain %v, want the Worktree's Domain under the Root Domain", info["domain"])
	}
}

func TestChangingTheRootDomainLiveRepatchesEveryWorktreeAndRestartsOnlyRunningProcesses(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, log := appURLProject(t, "rd-live")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("t3-2b4e")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	if got := d.GetServer("https://rd-live.localhost/").Name; got != "https://rd-live.localhost" {
		t.Fatalf("the main Worktree's Process started with APP_URL %q", got)
	}
	started := starts(t, log)

	change := d.SetRootDomain("rd-live.example.test")

	if got := d.GetServer("https://rd-live.rd-live.example.test/").Name; got != "https://rd-live.rd-live.example.test" {
		t.Fatalf("after the change, the main Worktree's Process has APP_URL %q, want the new one", got)
	}
	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "APP_URL=https://t3-2b4e.rd-live.rd-live.example.test\n")
	if n := starts(t, log) - started; n != 1 {
		t.Errorf("Processes started %d times for the change, want 1: only the running one restarts", n)
	}
	if state := worktreeState(t, d, "t3-2b4e.rd-live.rd-live.example.test").State; state != "stopped" {
		t.Errorf("the stopped Worktree is %s after the change, want stopped", state)
	}
	want := daemon.RootDomainChange{Previous: "localhost", Current: "rd-live.example.test",
		Patched: []string{repo.Dir, wt.Dir}, Restarted: map[string][]string{repo.Dir: {"web"}}}
	if !reflect.DeepEqual(change, want) {
		t.Errorf("the change was %+v, want %+v", change, want)
	}
}

func TestBucketOriginsFollowTheRootDomainOnRestartAndLive(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	repo := serviceProject(t, "rd-bucket", `objects = "1"`, "")
	d.MustRunCLI(repo.Dir, "link")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")
	waitForOrigins(t, d, "rd-bucket--feature", "https://feature.rd-bucket.localhost", "https://checkout.rd-bucket.localhost")

	writeGlobalConfig(t, d, "root_domain = \"rd-bucket.example.test\"\n")
	d.Restart()

	waitForOrigins(t, d, "rd-bucket--feature",
		"https://feature.rd-bucket.rd-bucket.example.test", "https://checkout.rd-bucket.rd-bucket.example.test")
	waitForOrigins(t, d, "rd-bucket", "https://rd-bucket.rd-bucket.example.test")

	d.SetRootDomain("rd-bucket-live.example.test")

	waitForOrigins(t, d, "rd-bucket--feature",
		"https://feature.rd-bucket.rd-bucket-live.example.test", "https://checkout.rd-bucket.rd-bucket-live.example.test")
}

func TestOpenOpensThePrimaryDomainUnderTheRootDomain(t *testing.T) {
	t.Parallel()
	d := personalRootDomainDaemon(t, "rd-open.example.test")
	repo := gitfixture.New(t, "rd-open-shop").WriteFile("readme.txt", "hi").Commit("a file")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "alias", "demo")

	d.MustRunCLI(repo.Dir, "open")

	want := []string{"https://demo.rd-open-shop.rd-open.example.test/"}
	if got := d.Platform.OpenedURLs(); !slices.Equal(got, want) {
		t.Fatalf("opened %q, want %q", got, want)
	}
}

// longestRoot is as long as a Root Domain may be (61 characters), and
// tooLongRoot is one character longer: <process>.<worktree>.<project>.<root>,
// with labels of up to 63 characters, must fit DNS's 253.
const (
	longestRoot = "rd-long-names-for-a-remote-machine.a-long-domain.example.test"
	tooLongRoot = "x" + longestRoot
)

// A Process host on a long Worktree name under a long Root Domain is longer
// than a certificate's Common Name may be (64 characters): the name is only
// in the certificate's SAN, which browsers check.
func TestAProcessHostLongerThanACommonNameGetsAValidCertificate(t *testing.T) {
	t.Parallel()
	root := longestRoot
	d := personalRootDomainDaemon(t, root)
	repo := routedProject(t, "rd-long")
	wt := repo.AddWorktree("t3-a-worktree-with-a-name-as-long-as-a-branch-name-can-be-made")
	d.MustRunCLI(repo.Dir, "link")

	host := "vite.t3-a-worktree-with-a-name-as-long-as-a-branch-name-can-be-made.rd-long." + root
	if got := d.GetServer("https://" + host + "/"); got.Name != "vite@"+filepath.Base(wt.Dir) {
		t.Fatalf("%s (%d characters) reached %q, want the Worktree's vite Process", host, len(host), got.Name)
	}
}
