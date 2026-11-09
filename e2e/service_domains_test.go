package e2e

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/clock"
	"github.com/lanewayhq/laneway/internal/container"
	"github.com/lanewayhq/laneway/internal/container/containertest"
	"github.com/lanewayhq/laneway/internal/daemon"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// storeImage is the image of the store test Engine, whose Service Domains
// are files (its port) and store-console (its web_port).
const storeImage = "example.test/store"

// storeServer is a fake store container's server on one of its ports: it
// answers every request with what it is, such as "api example.test/store:1
// GET /photos/cat.jpg", so a test sees which Service and port a request
// reached.
func storeServer(what string) containertest.Server {
	return func(ln net.Listener, spec container.Spec, data *containertest.Volume) {
		http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "%s %s %s %s", what, spec.Image, r.Method, r.URL.Path)
		}))
	}
}

// storeDaemon is a daemon whose Dashboard is at dash.localhost, with the
// store Plugin added and its image served by the fake container engine.
func storeDaemon(t *testing.T, dash string, opts ...testkit.Option) *testkit.Daemon {
	t.Helper()
	opts = append(opts, func(c *daemon.Config) { c.DashboardDomain = dash + ".localhost" })
	d := testkit.StartDaemon(t, opts...)
	d.Engine.Serve(storeImage, storeServer("api"))
	d.Engine.ServePort(storeImage, 7481, storeServer("console"))
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "store"))
	return d
}

func TestAServiceDomainStartsItsServiceAndForwardsToTheEnginesPort(t *testing.T) {
	t.Parallel()
	d := storeDaemon(t, "sd-forward")
	d.MustRunCLI(serviceProject(t, "sd-forward-shop", `store = "1"`, "").Dir, "link")
	if calls := d.Engine.Calls(); containsCall(calls, "start") {
		t.Fatalf("linking started a container: %v", calls)
	}

	res := d.Get("https://files.sd-forward.localhost/photos/cat.jpg")

	if res.StatusCode != http.StatusOK || res.Body != "api example.test/store:1 GET /photos/cat.jpg" {
		t.Fatalf("GET the files Service Domain = %d %q, want the store's API to answer", res.StatusCode, res.Body)
	}
	if state := serviceState(t, d, "store", "1"); state != "running" {
		t.Fatalf("store:1 is %s after a request to its Service Domain, want running", state)
	}
	res = d.Get("https://store-console.sd-forward.localhost/")
	if res.StatusCode != http.StatusOK || res.Body != "console example.test/store:1 GET /" {
		t.Fatalf("GET the store-console Service Domain = %d %q, want the store's console to answer", res.StatusCode, res.Body)
	}
	if n := countCalls(d.Engine.Calls(), "start"); n != 1 {
		t.Fatalf("the container was started %d times, want once: %v", n, d.Engine.Calls())
	}
}

func TestAServiceDomainServesTheDefaultVersionsServiceElseTheFirstServedOne(t *testing.T) {
	t.Parallel()
	d := storeDaemon(t, "sd-version")
	d.MustRunCLI(serviceProject(t, "sd-version-old", `store = "0.9"`, "").Dir, "link")

	if res := d.Get("https://files.sd-version.localhost/"); res.Body != "api example.test/store:0.9 GET /" {
		t.Fatalf("with only store:0.9 served, the Service Domain answers %d %q, want store:0.9", res.StatusCode, res.Body)
	}

	d.MustRunCLI(serviceProject(t, "sd-version-new", `store = "1"`, "").Dir, "link")

	if res := d.Get("https://files.sd-version.localhost/"); res.Body != "api example.test/store:1 GET /" {
		t.Fatalf("with store:1 served too, the Service Domain answers %d %q, want the default version, store:1", res.StatusCode, res.Body)
	}
}

func TestAServiceDomainsURLIsTheEnginesEndpointEnvPatchVariable(t *testing.T) {
	t.Parallel()
	d := storeDaemon(t, "sd-env")
	repo := serviceProject(t, "sd-env-shop", `store = "1"`, "[env]\nSTORE_ENDPOINT = \"{store_endpoint}\"\n")

	d.MustRunCLI(repo.Dir, "link", "--yes")

	assertContains(t, readFile(t, filepath.Join(repo.Dir, ".env")), "STORE_ENDPOINT=https://files.sd-env.localhost\n")
}

func TestLinkingAProjectUsingTheEndpointOfAnEngineInTheCatalogFetchesItAndFillsItIn(t *testing.T) {
	t.Parallel()
	cat := testkit.NewCatalog(t)
	cat.Publish(testkit.Plugin(t, "store"))
	d := testkit.StartDaemon(t, cat.Option(), func(c *daemon.Config) { c.DashboardDomain = "sd-fetch.localhost" })
	repo := serviceProject(t, "sd-fetch-shop", `store = "1"`, "[env]\nSTORE_ENDPOINT = \"{store_endpoint}\"\n")

	d.MustRunCLI(repo.Dir, "link", "--yes")

	assertContains(t, readFile(t, filepath.Join(repo.Dir, ".env")), "STORE_ENDPOINT=https://files.sd-fetch.localhost\n")
}

func TestAServiceDomainExplainsHowToDeclareItsEngineWhenNoProjectUsesIt(t *testing.T) {
	t.Parallel()
	d := storeDaemon(t, "sd-unused")
	d.MustRunCLI(serviceProject(t, "sd-unused-redis", `redis = "7"`, "").Dir, "link")

	res := d.Get("https://store-console.sd-unused.localhost/")

	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("GET the Service Domain with no store Service = %d, want 502:\n%s", res.StatusCode, res.Body)
	}
	assertContains(t, res.Body, "no Project uses store yet")
	assertContains(t, res.Body, "store = &#34;1&#34;")
	if calls := d.Engine.Calls(); containsCall(calls, "start") {
		t.Fatalf("visiting the Service Domain started a container: %v", calls)
	}
}

func TestADomainUnderTheDashboardsThatNoEngineDeclaresSaysWhatServesThere(t *testing.T) {
	t.Parallel()
	d := storeDaemon(t, "sd-nothing")

	res := d.Get("https://photos.sd-nothing.localhost/")

	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("GET a Domain no Engine declares = %d, want 502:\n%s", res.StatusCode, res.Body)
	}
	assertContains(t, res.Body, "nothing is served at photos.sd-nothing.localhost yet")
	assertContains(t, res.Body, "Linking it fetches its Plugin from the Catalog")
}

func TestNoProjectCanBeServedUnderTheDashboardsDomainWhereServiceDomainsAre(t *testing.T) {
	t.Parallel()
	d := storeDaemon(t, "sd-reserved")
	repo := serviceProject(t, "sd-reserved-shop", `store = "1"`, "")

	// Its Worktrees would be <worktree>.sd-reserved.localhost, such as
	// files.sd-reserved.localhost.
	res := d.RunCLI(repo.Dir, "link", "--name", "sd-reserved")

	if res.ExitCode == 0 {
		t.Fatalf("linking a Project named after the Dashboard's Domain exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "where the Dashboard is")
}

func TestAPluginWhoseServiceDomainIsAnotherPluginsIsRefusedNamingBoth(t *testing.T) {
	t.Parallel()
	d := storeDaemon(t, "sd-clash")
	plugin := testkit.Plugin(t, "kv")
	writeFile(t, filepath.Join(plugin, "kv.toml"), readFile(t, filepath.Join(plugin, "kv.toml"))+
		"\n[[domains]]\nlabel = \"files\"\nport = 7379\n")

	res := d.RunCLI(d.Home, "plugin", "add", "--yes", plugin)

	if res.ExitCode == 0 {
		t.Fatalf("adding a second files Service Domain exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "Plugin kv's Engine kv declares the Service Domain files, but Plugin store's Engine store already does")
}

func TestAPluginWhoseTwoEnginesDeclareOneServiceDomainIsRefused(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := testkit.Plugin(t, "store")
	writeFile(t, filepath.Join(plugin, "archive.toml"),
		strings.Replace(readFile(t, filepath.Join(plugin, "store.toml")), `name = "store"`, `name = "archive"`, 1))
	writeFile(t, filepath.Join(plugin, "plugin.json"),
		strings.Replace(readFile(t, filepath.Join(plugin, "plugin.json")), `["store.toml"]`, `["store.toml", "archive.toml"]`, 1))

	res := d.RunCLI(d.Home, "plugin", "add", "--yes", plugin)

	if res.ExitCode == 0 {
		t.Fatalf("adding a Plugin declaring the files Service Domain twice exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "Plugin store's Engines store and archive both declare the Service Domain files")
}

func TestARequestToAServiceDomainKeepsItsServiceFromGoingIdleUntilItEnds(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	d := storeDaemon(t, "sd-idle", testkit.WithClock(clk))
	arrived, finish := make(chan struct{}), make(chan struct{})
	d.Engine.Serve(storeImage, func(ln net.Listener, spec container.Spec, data *containertest.Volume) {
		http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/big.zip" {
				return // the readiness probe
			}
			close(arrived)
			<-finish
			io.WriteString(w, "downloaded")
		}))
	})
	d.MustRunCLI(serviceProject(t, "sd-idle-shop", `store = "1"`, "").Dir, "link")

	type result struct {
		body string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		res, err := d.HTTPS().Get("https://files.sd-idle.localhost/big.zip")
		if err != nil {
			done <- result{err: err}
			return
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		done <- result{string(body), err}
	}()
	select {
	case <-arrived:
	case <-time.After(30 * time.Second):
		t.Fatal("the download never reached the store")
	}
	clk.Advance(time.Hour)
	if state := serviceState(t, d, "store", "1"); state != "running" {
		t.Fatalf("store:1 is %s with a download in flight, want running", state)
	}

	close(finish)
	if res := <-done; res.err != nil || res.body != "downloaded" {
		t.Fatalf("the download answered %q, %v", res.body, res.err)
	}
	if delay := waitForTimer(t, clk); delay != 15*time.Minute {
		t.Fatalf("Idle scheduled %s after the download, want the default 15m", delay)
	}
	clk.Advance(15 * time.Minute)
	waitForServiceState(t, d, "store", "1", "idle")
}
