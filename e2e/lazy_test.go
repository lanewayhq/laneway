package e2e

import (
	"encoding/json"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// startCounting wraps command so every start of the Process appends a line to
// a file outside the repo, and returns the file's path.
func startCounting(t *testing.T, command string) (string, string) {
	t.Helper()
	log := filepath.Join(gitfixture.TempDir(t), "starts.log")
	return "echo started >> " + log + " && exec " + command, log
}

// starts is how many times a startCounting Process has been started.
func starts(t *testing.T, log string) int {
	t.Helper()
	b, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "started\n")
}

func TestLinkStartsNothingUntilTheFirstRequest(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	command, log := startCounting(t, testkit.TestServer(t))
	repo := gitfixture.New(t, "lazy-first").
		WriteFile("portby.toml", processConfig(command)).
		Commit("declare web Process")

	d.MustRunCLI(repo.Dir, "link")
	// Long enough for a Process started by link to have run its first line.
	time.Sleep(500 * time.Millisecond)
	if n := starts(t, log); n != 0 {
		t.Fatalf("Process started %d times after link, want 0", n)
	}

	d.GetServer("https://lazy-first.localhost/")
	if n := starts(t, log); n != 1 {
		t.Fatalf("Process started %d times after the first request, want 1", n)
	}
}

func TestSimultaneousFirstRequestsStartTheWorktreeOnce(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	command, log := startCounting(t, testkit.TestServer(t)+" --delay 1s")
	repo := gitfixture.New(t, "lazy-once").
		WriteFile("portby.toml", processConfig(command)).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")

	client := d.HTTPS()
	statuses := make([]int, 10)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Go(func() {
			if resp, err := client.Get("https://lazy-once.localhost/"); err == nil {
				resp.Body.Close()
				statuses[i] = resp.StatusCode
			}
		})
	}
	wg.Wait()

	for i, status := range statuses {
		if status != http.StatusOK {
			t.Errorf("request %d: status %d, want 200", i, status)
		}
	}

	if n := starts(t, log); n != 1 {
		t.Fatalf("Process started %d times for 10 simultaneous first requests, want 1", n)
	}
}

func TestBrowserGetsAnAutoRefreshingStartingPageUntilTheAppIsUp(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "lazy-page").
		WriteFile("portby.toml", processConfig(testkit.TestServer(t)+" --name web --delay 2s")).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")
	// Until then a navigation gets the setup page (#15), not this one.
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	page := d.Navigate("https://lazy-page.localhost/dashboard")

	if page.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("first navigation: status %d, want 503:\n%s", page.StatusCode, page.Body)
	}
	if page.Header.Get("Refresh") == "" {
		t.Fatalf("starting page has no Refresh header: %v", page.Header)
	}
	assertContains(t, page.Header.Get("Content-Type"), "text/html")
	assertContains(t, page.Body, "Starting")
	assertContains(t, page.Body, "lazy-page")

	// What the browser does on each refresh, until the app loads.
	deadline := time.Now().Add(15 * time.Second)
	for page.StatusCode == http.StatusServiceUnavailable && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		page = d.Navigate("https://lazy-page.localhost/dashboard")
	}
	var res testkit.ServerResponse
	if page.StatusCode != http.StatusOK || json.Unmarshal([]byte(page.Body), &res) != nil || res.Path != "/dashboard" {
		t.Fatalf("after the Process came up: status %d, want the app's 200:\n%s", page.StatusCode, page.Body)
	}
}

func TestHeldRequestFailsWithGatewayTimeoutAfterTheStartTimeout(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "lazy-timeout").
		WriteFile("portby.toml", "start_timeout = \"1s\"\n"+processConfig(testkit.TestServer(t)+" --bind-only")).
		Commit("declare a web Process that never listens")
	d.MustRunCLI(repo.Dir, "link")

	began := time.Now()
	res := d.Get("https://lazy-timeout.localhost/api/orders")
	took := time.Since(began)

	if res.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status %d, want 504:\n%s", res.StatusCode, res.Body)
	}
	if took < time.Second || took > 10*time.Second {
		t.Fatalf("request was held for %v, want about the 1s start timeout", took)
	}
	assertContains(t, res.Body, "1s")
}

func TestAnotherProgramOnThePrimaryProcessPortIsNotMistakenForIt(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	pidFile := filepath.Join(gitfixture.TempDir(t), "pid")
	// The Process never listens, but starts a program in a session of its
	// own that listens on the Process's port, as another program that took
	// the port would.
	command := "setsid " + testkit.TestServer(t) + " --name intruder & echo $! > " + pidFile + "; exec sleep 600"
	repo := gitfixture.New(t, "lazy-intruder").
		WriteFile("portby.toml", "start_timeout = \"1s\"\n"+processConfig(command)).
		Commit("declare a web Process whose port another program takes")
	d.MustRunCLI(repo.Dir, "link")

	res := d.Get("https://lazy-intruder.localhost/")
	pid := waitForPID(t, pidFile)
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })

	if res.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status %d, want 504 (the Process never listened):\n%s", res.StatusCode, res.Body)
	}
}

// navigateUntilStarted navigates to url, as the starting page's refreshes do,
// until it gets something other than the starting page.
func navigateUntilStarted(t *testing.T, d *testkit.Daemon, url string) testkit.Response {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	page := d.Navigate(url)
	for page.StatusCode == http.StatusServiceUnavailable && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		page = d.Navigate(url)
	}
	return page
}

func TestBrowserGetsAnErrorPageWhenThePrimaryProcessExitsWhileStarting(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	command := "for i in $(seq 1 60); do echo line-$i; done; echo to-stderr >&2; exit 3"
	repo := gitfixture.New(t, "lazy-exit").
		WriteFile("portby.toml", processConfig(command)).
		Commit("declare a web Process that crashes")
	d.MustRunCLI(repo.Dir, "link")

	page := navigateUntilStarted(t, d, "https://lazy-exit.localhost/")

	if page.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d, want 502:\n%s", page.StatusCode, page.Body)
	}
	assertContains(t, page.Body, "Portby")
	assertContains(t, page.Body, html.EscapeString(command))
	// The last 50 lines of output, from both stdout and stderr. The last
	// may be Portby's own note that the Process exited.
	assertContains(t, page.Body, "line-13\n")
	assertContains(t, page.Body, "line-60\n")
	assertContains(t, page.Body, "to-stderr")
	if strings.Contains(page.Body, "line-11\n") {
		t.Fatalf("error page shows more than the last 50 lines:\n%s", page.Body)
	}
}

func TestBrowserGetsAnErrorPageWhenThePrimaryProcessTimesOutWhileStarting(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	command := "echo still-booting; exec " + testkit.TestServer(t) + " --bind-only"
	repo := gitfixture.New(t, "lazy-slow").
		WriteFile("portby.toml", "start_timeout = \"1s\"\n"+processConfig(command)).
		Commit("declare a web Process that never listens")
	d.MustRunCLI(repo.Dir, "link")

	page := navigateUntilStarted(t, d, "https://lazy-slow.localhost/")

	if page.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status %d, want 504:\n%s", page.StatusCode, page.Body)
	}
	assertContains(t, page.Body, html.EscapeString(command))
	assertContains(t, page.Body, "still-booting")
	assertContains(t, page.Body, "start_timeout")
}

func TestReloadingTheErrorPageStartsTheWorktreeAgain(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	// Crashes on its first start only, like an app whose bug was fixed
	// after the error page was shown.
	marker := filepath.Join(gitfixture.TempDir(t), "crashed-once")
	command := "if [ -e " + marker + " ]; then exec " + testkit.TestServer(t) + "; fi; touch " + marker + "; exit 1"
	repo := gitfixture.New(t, "lazy-retry").
		WriteFile("portby.toml", processConfig(command)).
		Commit("declare a web Process that crashes once")
	d.MustRunCLI(repo.Dir, "link")

	if page := navigateUntilStarted(t, d, "https://lazy-retry.localhost/"); page.StatusCode != http.StatusBadGateway {
		t.Fatalf("first start: status %d, want the 502 error page:\n%s", page.StatusCode, page.Body)
	}

	page := navigateUntilStarted(t, d, "https://lazy-retry.localhost/")
	if page.StatusCode != http.StatusOK {
		t.Fatalf("after reloading the error page: status %d, want the app's 200:\n%s", page.StatusCode, page.Body)
	}
}

func TestStoppingTheDaemonEndsAStartInProgress(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	pidFile := filepath.Join(gitfixture.TempDir(t), "pid")
	repo := gitfixture.New(t, "lazy-shutdown").
		WriteFile("portby.toml", processConfig("echo $$ > "+pidFile+"; exec "+testkit.TestServer(t)+" --bind-only")).
		Commit("declare a web Process that never listens")
	d.MustRunCLI(repo.Dir, "link")
	client := d.HTTPS()
	go func() {
		if resp, err := client.Get("https://lazy-shutdown.localhost/"); err == nil {
			resp.Body.Close()
		}
	}()
	pid := waitForPID(t, pidFile)

	began := time.Now()
	d.Stop()

	if took := time.Since(began); took > 10*time.Second {
		t.Fatalf("stopping the daemon took %v with a request held", took)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("Process %d is still running after the daemon stopped", pid)
	}
}

// waitForPID waits for a Process to write its pid to file.
func waitForPID(t *testing.T, file string) int {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(file); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return pid
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no pid in %s", file)
	return 0
}

// startByBrowsing starts the Worktree served at domain the way a browser
// visiting it does, without waiting for its Primary Process to come up, and
// waits until `portby status` shows its Processes running.
func startByBrowsing(t *testing.T, d *testkit.Daemon, domain string) {
	t.Helper()
	url := "https://" + domain + "/"
	if page := d.Navigate(url); page.StatusCode != http.StatusServiceUnavailable && page.StatusCode != http.StatusOK {
		t.Fatalf("visiting %s: status %d:\n%s", url, page.StatusCode, page.Body)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		status := d.MustRunCLI(d.Home, "status").Stdout
		for line := range strings.Lines(status) {
			if slices.Contains(strings.Fields(line), domain) && strings.Contains(line, ":running") {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s's Processes never showed as running:\n%s", domain, status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRemovingAWorktreeEndsAStartInProgress(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	pids := gitfixture.TempDir(t)
	repo := gitfixture.New(t, "lazy-removed").
		WriteFile("portby.toml", processConfig(
			`echo $$ > `+pids+`/"$(basename "$(pwd)")"; exec `+testkit.TestServer(t)+" --bind-only")).
		Commit("declare a web Process that never listens")
	d.MustRunCLI(repo.Dir, "link")
	wt := repo.AddWorktree("doomed")
	waitForWorktree(t, d, "lazy-removed", "doomed")
	startByBrowsing(t, d, "doomed.lazy-removed.localhost")
	pid := waitForPID(t, filepath.Join(pids, "doomed"))

	repo.Git("worktree", "remove", "--force", wt.Dir)
	waitForNoWorktree(t, d, "lazy-removed", "doomed")

	eventually(t, "the removed Worktree's Process to stop", func() bool {
		return syscall.Kill(pid, 0) != nil
	})
	if res := d.Get("https://doomed.lazy-removed.localhost/"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("removed Worktree's Domain: status %d, want 404:\n%s", res.StatusCode, res.Body)
	}
}
