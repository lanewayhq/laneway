package e2e

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

func TestAPageLoadOnAWorktreesDomainGoesToItsPrimaryDomain(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "redirect-domain")
	wt := repo.AddWorktree("t3-298e5f25")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")

	assertRedirectsTo(t, d, "https://t3-298e5f25.redirect-domain.localhost/orders?page=2",
		"https://checkout.redirect-domain.localhost/orders?page=2")
}

func TestAPageLoadOnAnOlderAliasGoesToTheNewest(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "redirect-older")
	wt := repo.AddWorktree("t3-298e5f25")
	d.MustRunCLI(repo.Dir, "link")
	// The newest sorts first, so "newest" can't be read off a sorted list.
	d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "basket")

	assertRedirectsTo(t, d, "https://checkout.redirect-older.localhost/cart",
		"https://basket.redirect-older.localhost/cart")
	assertRedirectsTo(t, d, "https://t3-298e5f25.redirect-older.localhost/",
		"https://basket.redirect-older.localhost/")
	// The Primary Domain itself is served.
	url := "https://basket.redirect-older.localhost/"
	if got := serverResponse(t, url, d.Navigate(url)); got.Name != "web@t3-298e5f25" {
		t.Fatalf("a page load on %s reached %q, want the Worktree's Primary Process", url, got.Name)
	}
}

func TestOnlyPageLoadsAreRedirectedToThePrimaryDomain(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "redirect-only")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "demo")

	// A fetch, an API client or curl.
	if got := d.GetServer("https://feature.redirect-only.localhost/api/orders"); got.Name != "web@feature" {
		t.Fatalf("a GET that isn't a page load reached %q, want the Worktree's Primary Process", got.Name)
	}
	// A form post navigates too, but its body mustn't be sent elsewhere.
	url := "https://feature.redirect-only.localhost/orders"
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
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if got := serverResponse(t, url, testkit.Response{Response: res, Body: string(body)}); got.Name != "web@feature" {
		t.Fatalf("a form post on %s reached %q, want the Worktree's Primary Process", url, got.Name)
	}
	// A Process's host under an old name is never redirected: stale pages
	// load their assets from it.
	url = "https://vite.feature.redirect-only.localhost/"
	if got := serverResponse(t, url, d.Navigate(url)); got.Name != "vite@feature" {
		t.Fatalf("a page load on %s reached %q, want the Worktree's vite Process", url, got.Name)
	}
}

func TestAWorktreeWithoutAnAliasIsNotRedirected(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "redirect-none")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(repo.Dir, "link")

	url := "https://feature.redirect-none.localhost/"
	if got := serverResponse(t, url, d.Navigate(url)); got.Name != "web@feature" {
		t.Fatalf("a page load on %s reached %q, want the Worktree's Primary Process", url, got.Name)
	}

	d.MustRunCLI(wt.Dir, "worktree", "alias", "demo")
	assertRedirectsTo(t, d, url, "https://demo.redirect-none.localhost/")

	// Removing the Alias undoes the redirect at once.
	d.MustRunCLI(wt.Dir, "worktree", "alias", "--remove", "demo")
	if got := serverResponse(t, url, d.Navigate(url)); got.Name != "web@feature" {
		t.Fatalf("after --remove a page load on %s reached %q, want the Worktree's Primary Process", url, got.Name)
	}
}

func TestTheMainWorktreesDomainRedirectsToItsAlias(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "redirect-main")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "alias", "demo")

	assertRedirectsTo(t, d, "https://redirect-main.localhost/orders?page=2",
		"https://demo.redirect-main.localhost/orders?page=2")
	url := "https://demo.redirect-main.localhost/"
	if got := serverResponse(t, url, d.Navigate(url)); got.Name != "web@redirect-main" {
		t.Fatalf("a page load on %s reached %q, want the main Worktree's Primary Process", url, got.Name)
	}
}

// assertRedirectsTo checks that a page load on url is sent to want with an
// uncached 307.
func assertRedirectsTo(t *testing.T, d *testkit.Daemon, url, want string) {
	t.Helper()
	res := d.Navigate(url)
	if res.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("a page load on %s got %d, want 307 to %s:\n%s", url, res.StatusCode, want, res.Body)
	}
	if got := res.Header.Get("Location"); got != want {
		t.Fatalf("a page load on %s was sent to %q, want %q", url, got, want)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("the redirect from %s has Cache-Control %q, want no-store", url, got)
	}
}
