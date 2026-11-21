package e2e

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/testkit"
)

// Once a Root Domain other than localhost is set, .localhost names are no
// longer served (ADR-0009): a page load there names the equivalent URL
// under the Root Domain, and is not redirected.

func TestAPageLoadOnALocalhostNameShowsItsURLUnderTheRootDomain(t *testing.T) {
	t.Parallel()
	d := rootDomainDaemon(t, "rd-moved.example.test")
	repo := routedProject(t, "moved-shop")
	repo.AddWorktree("feature")
	d.MustRunCLI(repo.Dir, "link")

	assertMovedPage(t, d, "https://feature.moved-shop.localhost/orders?page=2",
		"https://feature.moved-shop.rd-moved.example.test/orders?page=2")
	if w := worktreeState(t, d, "feature.moved-shop.rd-moved.example.test"); w.State != "stopped" {
		t.Fatalf("the Worktree is %s, want stopped: the page load mustn't reach it", w.State)
	}
}

func TestEveryLocalhostNameNamesItsEquivalentUnderTheRootDomain(t *testing.T) {
	t.Parallel()
	d := rootDomainDaemon(t, "rd-equiv.example.test")
	repo := routedProject(t, "moved-equiv")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "basket")

	for url, want := range map[string]string{
		"https://moved-equiv.localhost/":                   "https://moved-equiv.rd-equiv.example.test/",
		"https://checkout.moved-equiv.localhost/cart":      "https://checkout.moved-equiv.rd-equiv.example.test/cart",
		"https://vite.basket.moved-equiv.localhost/@vite/": "https://vite.basket.moved-equiv.rd-equiv.example.test/@vite/",
	} {
		assertMovedPage(t, d, url, want)
	}
}

func TestAServiceDomainsLocalhostNameNamesItsURLUnderTheRootDomain(t *testing.T) {
	t.Parallel()
	d := rootDomainDaemon(t, "rd-moved-service.example.test")
	d.Engine.Serve(storeImage, storeServer("api"))
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "store"))

	// No other test may serve files.laneway.localhost (see dashboard_test.go).
	assertMovedPage(t, d, "https://files.laneway.localhost/photos/cat.jpg",
		"https://files.laneway.rd-moved-service.example.test/photos/cat.jpg")
	assertMovedError(t, "a GET for an object", d.Get("https://files.laneway.localhost/photos/cat.jpg"),
		"https://files.laneway.rd-moved-service.example.test/photos/cat.jpg")
	if calls := d.Engine.Calls(); containsCall(calls, "start") {
		t.Fatalf("a request to the .localhost name started the store's container: %v", calls)
	}
}

func TestPlainHTTPStillSendsLocalhostNamesToHTTPSForTheirPage(t *testing.T) {
	t.Parallel()
	d := rootDomainDaemon(t, "rd-moved-http.example.test")
	_, httpsPort, _ := net.SplitHostPort(d.ProxyAddr())
	req, err := http.NewRequest(http.MethodGet, "http://"+d.HTTPAddr()+"/cart", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "shop.localhost"
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}

	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	want := "https://shop.localhost:" + httpsPort + "/cart"
	if res.StatusCode != http.StatusPermanentRedirect || res.Header.Get("Location") != want {
		t.Fatalf("status %d to %q, want 308 to %q", res.StatusCode, res.Header.Get("Location"), want)
	}
}

func TestAPageLoadOnAnUnknownLocalhostNameSaysNothingIsServedThere(t *testing.T) {
	t.Parallel()
	d := rootDomainDaemon(t, "rd-unknown.example.test")

	res := d.Navigate("https://nope.moved-unknown.localhost/orders")

	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404:\n%s", res.StatusCode, res.Body)
	}
	assertContains(t, res.Body, "nope.moved-unknown.localhost")
	assertContains(t, res.Body, "rd-unknown.example.test")
	if strings.Contains(res.Body, "<a href=") {
		t.Fatalf("the page links somewhere, but nothing is served there under the Root Domain either:\n%s", res.Body)
	}
}

func TestOtherRequestsToALocalhostNameErrorAndNeverReachTheProcess(t *testing.T) {
	t.Parallel()
	d := rootDomainDaemon(t, "rd-other.example.test")
	repo := routedProject(t, "moved-other")
	d.MustRunCLI(repo.Dir, "link")
	const want = "https://moved-other.rd-other.example.test/orders?page=2"
	url := "https://moved-other.localhost/orders?page=2"

	// A fetch, an asset or curl.
	assertMovedError(t, "a GET that isn't a page load", d.Get(url), want)
	// A form post.
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader("item=1"))
	if err != nil {
		t.Fatal(err)
	}
	testkit.SetNavigationHeaders(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := d.HTTPS().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	assertMovedError(t, "a form post", testkit.Response{Response: res, Body: string(body)}, want)
	// A WebSocket upgrade, as a browser sends it.
	conn, err := tls.Dial("tcp", d.ProxyAddr(), &tls.Config{
		RootCAs: d.CAPool(), ServerName: "moved-other.localhost", NextProtos: []string{"http/1.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	fmt.Fprint(conn, "GET /orders?page=2 HTTP/1.1\r\nHost: moved-other.localhost\r\nUpgrade: websocket\r\n"+
		"Connection: Upgrade\r\nSec-Fetch-Mode: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"+
		"Sec-WebSocket-Version: 13\r\n\r\n")
	res, err = http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	assertMovedError(t, "a WebSocket upgrade", testkit.Response{Response: res, Body: string(body)}, want)

	if w := worktreeState(t, d, "moved-other.rd-other.example.test"); w.State != "stopped" {
		t.Fatalf("the Worktree is %s, want stopped: no request may reach it", w.State)
	}
}

func TestLocalhostNamesAreServedAgainOnceTheRootDomainIsLocalhost(t *testing.T) {
	t.Parallel()
	d := rootDomainDaemon(t, "rd-reset.example.test")
	repo := routedProject(t, "moved-reset")
	d.MustRunCLI(repo.Dir, "link")
	assertMovedPage(t, d, "https://moved-reset.localhost/", "https://moved-reset.rd-reset.example.test/")

	d.SetRootDomain("localhost")

	if got := d.GetServer("https://moved-reset.localhost/orders"); got.Name != "web@moved-reset" {
		t.Fatalf("moved-reset.localhost reached %q, want the main Worktree's Primary Process", got.Name)
	}
}

// assertMovedError checks that res, the response to what, is an error
// whose body is one line of plain text naming want.
func assertMovedError(t *testing.T, what string, res testkit.Response, want string) {
	t.Helper()
	if res.StatusCode < 400 {
		t.Fatalf("%s got %d, want an error:\n%s", what, res.StatusCode, res.Body)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("%s got Content-Type %q, want plain text:\n%s", what, ct, res.Body)
	}
	if line := strings.TrimSuffix(res.Body, "\n"); strings.Contains(line, "\n") || !strings.Contains(line, want) {
		t.Fatalf("%s got %q, want one line naming %s", what, res.Body, want)
	}
}

// assertMovedPage checks that a page load on url gets an HTML page linking
// want, and isn't a redirect.
func assertMovedPage(t *testing.T, d *testkit.Daemon, url, want string) {
	t.Helper()
	res := d.Navigate(url)
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		t.Fatalf("a page load on %s got a %d to %q, want a page, not a redirect",
			url, res.StatusCode, res.Header.Get("Location"))
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("a page load on %s got Content-Type %q, want an HTML page:\n%s",
			url, res.Header.Get("Content-Type"), res.Body)
	}
	if link := `<a href="` + html.EscapeString(want) + `">`; !strings.Contains(res.Body, link) {
		t.Fatalf("a page load on %s got %d without a link to %s:\n%s", url, res.StatusCode, want, res.Body)
	}
}
