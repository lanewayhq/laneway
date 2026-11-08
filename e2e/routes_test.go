package e2e

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// routedProject is a repo with a Primary Process `web` and an HTTP Process
// `vite`, both test servers reporting "<process>@<Worktree dir basename>",
// plus a `queue` Process that isn't HTTP.
func routedProject(t *testing.T, name string) *gitfixture.Repo {
	t.Helper()
	server := testkit.TestServer(t)
	report := func(process string) string {
		return fmt.Sprintf(`%s --name "%s@$(basename "$(pwd)")"`, server, process)
	}
	return gitfixture.New(t, name).
		WriteFile("laneway.toml", fmt.Sprintf(
			"[processes.web]\ncommand = %q\nprimary = true\n\n"+
				"[processes.vite]\ncommand = %q\nhttp = true\n\n"+
				"[processes.queue]\ncommand = %q\n",
			report("web"), report("vite"), `echo "PORT=[$PORT]"; exec sleep 600`)).
		Commit("declare web, vite and queue Processes")
}

func TestHTTPProcessIsServedAtItsNameUnderTheDomain(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "routes-proc")
	d.MustRunCLI(repo.Dir, "link")

	vite := d.GetServer("https://vite.routes-proc.localhost/@vite/client")
	if vite.Name != "vite@routes-proc" || vite.Host != "vite.routes-proc.localhost" || vite.Path != "/@vite/client" {
		t.Fatalf("vite.routes-proc.localhost reached %+v, want the vite Process", vite)
	}
	if web := d.GetServer("https://routes-proc.localhost/"); web.Name != "web@routes-proc" {
		t.Fatalf("routes-proc.localhost reached %q, want the Primary Process", web.Name)
	}
}

func TestOnlyHTTPProcessesGetAPortAndARoute(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := routedProject(t, "routes-nonhttp")
	d.MustRunCLI(repo.Dir, "link")
	d.GetServer("https://routes-nonhttp.localhost/")

	waitForLogs(t, d, repo.Dir, []string{"logs", "queue"}, "PORT=[]")
	assertNotServed(t, d, "https://queue.routes-nonhttp.localhost/")
	// The Primary Process is served at the Domain itself, not at web.<domain>.
	assertNotServed(t, d, "https://web.routes-nonhttp.localhost/")
}

func TestConfigWithHTTPProcessesButNoPrimaryProcessIsRejected(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "routes-noprimary").
		WriteFile("laneway.toml", "[processes.web]\ncommand = \"serve\"\n\n"+
			"[processes.vite]\ncommand = \"vite\"\nhttp = true\n").
		Commit("declare two Processes, neither Primary")

	res := d.RunCLI(repo.Dir, "link")

	if res.ExitCode == 0 {
		t.Fatalf("link succeeded without a Primary Process:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "laneway.toml")
	assertContains(t, res.Stderr, "processes.vite.http")
	assertContains(t, res.Stderr, "Primary Process")
}

// assertNotServed checks that the proxy answers url with its "Nothing is
// served here" page.
func assertNotServed(t *testing.T, d *testkit.Daemon, url string) {
	t.Helper()
	res := d.Get(url)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("GET %s = %d, want 404 (nothing served):\n%s", url, res.StatusCode, res.Body)
	}
}
