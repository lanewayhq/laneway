package e2e

import (
	"net/http"
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
