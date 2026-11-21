package e2e

import (
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/daemon"
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
