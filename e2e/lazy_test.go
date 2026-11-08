package e2e

import (
	"context"
	"encoding/json"
	"html"
	"io"
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

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
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
		WriteFile("laneway.toml", processConfig(command)).
		Commit("declare web Process")

	d.MustRunCLI(repo.Dir, "link")
	// Once Ready, a Worktree link had started would be starting or running.
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	if w := worktreeState(t, d, "lazy-first.localhost"); w.State != "stopped" || len(w.Processes) != 0 {
		t.Fatalf("after link the Worktree is %q with Processes %+v, want stopped with none", w.State, w.Processes)
	}
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
		WriteFile("laneway.toml", processConfig(command)).
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

func TestABrowserPageLoadIsHeldUntilTheAppIsUp(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "lazy-page").
		WriteFile("laneway.toml", processConfig(testkit.TestServer(t)+" --name web --delay 2s")).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")
	// Until then a page load gets the Preparing page (#15).
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	began := time.Now()
	page := d.Navigate("https://lazy-page.localhost/dashboard")
	took := time.Since(began)

	var res testkit.ServerResponse
	if page.StatusCode != http.StatusOK || json.Unmarshal([]byte(page.Body), &res) != nil || res.Path != "/dashboard" {
		t.Fatalf("page load: status %d, want the app's 200 for /dashboard:\n%s", page.StatusCode, page.Body)
	}
	if refresh := page.Header.Get("Refresh"); refresh != "" {
		t.Fatalf("page load has a Refresh header %q, want none", refresh)
	}
	if took < 2*time.Second || took > 15*time.Second {
		t.Fatalf("page load was held for %v, want about the Process's 2s delay", took)
	}
}

func TestHeldRequestFailsWithGatewayTimeoutAfterTheStartTimeout(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "lazy-timeout").
		WriteFile("laneway.toml", "start_timeout = \"1s\"\n"+processConfig(testkit.TestServer(t)+" --bind-only")).
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
		WriteFile("laneway.toml", "start_timeout = \"1s\"\n"+processConfig(command)).
		Commit("declare a web Process whose port another program takes")
	d.MustRunCLI(repo.Dir, "link")

	res := d.Get("https://lazy-intruder.localhost/")
	pid := waitForPID(t, pidFile)
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })

	if res.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status %d, want 504 (the Process never listened):\n%s", res.StatusCode, res.Body)
	}
}

// navigateUntilPrepared navigates to url, as the Preparing page's refreshes
// do, until it gets something other than the Preparing page, and returns
// that.
func navigateUntilPrepared(t *testing.T, d *testkit.Daemon, url string) testkit.Response {
	t.Helper()
	var page testkit.Response
	testkit.WaitUntil(t, 15*time.Second, url+" to stop answering with the Preparing page", func() bool {
		page = d.Navigate(url)
		return page.StatusCode != http.StatusServiceUnavailable
	})
	return page
}

func TestBrowserGetsAnErrorPageWhenThePrimaryProcessExitsWhileStarting(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	command := "for i in $(seq 1 60); do echo line-$i; done; echo to-stderr >&2; exit 3"
	repo := gitfixture.New(t, "lazy-exit").
		WriteFile("laneway.toml", processConfig(command)).
		Commit("declare a web Process that crashes")
	d.MustRunCLI(repo.Dir, "link")

	page := d.Navigate("https://lazy-exit.localhost/")

	if page.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d, want 502:\n%s", page.StatusCode, page.Body)
	}
	assertContains(t, page.Body, "Laneway")
	assertContains(t, page.Body, html.EscapeString(command))
	// The last 50 lines of output, from both stdout and stderr. The last
	// may be Laneway's own note that the Process exited.
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
		WriteFile("laneway.toml", "start_timeout = \"1s\"\n"+processConfig(command)).
		Commit("declare a web Process that never listens")
	d.MustRunCLI(repo.Dir, "link")

	page := d.Navigate("https://lazy-slow.localhost/")

	if page.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status %d, want 504:\n%s", page.StatusCode, page.Body)
	}
	assertContains(t, page.Body, html.EscapeString(command))
	assertContains(t, page.Body, "still-booting")
	assertContains(t, page.Body, "start_timeout")
}

// crashOnceCommand is a Process command that exits on its first start only,
// like an app whose bug was fixed after its failure was seen.
func crashOnceCommand(t *testing.T) string {
	marker := filepath.Join(gitfixture.TempDir(t), "crashed-once")
	return "if [ -e " + marker + " ]; then exec " + testkit.TestServer(t) + "; fi; touch " + marker + "; exit 1"
}

func TestReloadingTheErrorPageStartsTheWorktreeAgain(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	command := crashOnceCommand(t)
	repo := gitfixture.New(t, "lazy-retry").
		WriteFile("laneway.toml", processConfig(command)).
		Commit("declare a web Process that crashes once")
	d.MustRunCLI(repo.Dir, "link")

	if page := d.Navigate("https://lazy-retry.localhost/"); page.StatusCode != http.StatusBadGateway {
		t.Fatalf("first start: status %d, want the 502 error page:\n%s", page.StatusCode, page.Body)
	}

	page := d.Navigate("https://lazy-retry.localhost/")
	if page.StatusCode != http.StatusOK {
		t.Fatalf("after reloading the error page: status %d, want the app's 200:\n%s", page.StatusCode, page.Body)
	}
}

func TestRequestingAgainAfterAFailedStartStartsTheWorktreeAgain(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	command := crashOnceCommand(t)
	repo := gitfixture.New(t, "lazy-retry-curl").
		WriteFile("laneway.toml", processConfig(command)).
		Commit("declare a web Process that crashes once")
	d.MustRunCLI(repo.Dir, "link")

	if resp := d.Get("https://lazy-retry-curl.localhost/"); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("first start: status %d, want the 502 error page:\n%s", resp.StatusCode, resp.Body)
	}

	resp := d.Get("https://lazy-retry-curl.localhost/")
	var res testkit.ServerResponse
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(resp.Body), &res) != nil {
		t.Fatalf("requesting again: status %d, want the app's 200:\n%s", resp.StatusCode, resp.Body)
	}
}

func TestSimultaneousRequestsShareAFailedStartAndRetryItOnce(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	command, log := startCounting(t, "sleep 1; exit 1")
	repo := gitfixture.New(t, "lazy-fail-once").
		WriteFile("laneway.toml", processConfig(command)).
		Commit("declare a web Process that always crashes")
	d.MustRunCLI(repo.Dir, "link")

	client := d.HTTPS()
	// requestAll sends 10 simultaneous requests and fails the test unless
	// every one gets the same 502 error page.
	requestAll := func(round string) {
		t.Helper()
		statuses := make([]int, 10)
		bodies := make([]string, 10)
		var wg sync.WaitGroup
		for i := range statuses {
			wg.Go(func() {
				resp, err := client.Get("https://lazy-fail-once.localhost/")
				if err != nil {
					return
				}
				defer resp.Body.Close()
				b, _ := io.ReadAll(resp.Body)
				statuses[i], bodies[i] = resp.StatusCode, string(b)
			})
		}
		wg.Wait()
		for i, status := range statuses {
			if status != http.StatusBadGateway {
				t.Fatalf("%s, request %d: status %d, want 502:\n%s", round, i, status, bodies[i])
			}
			if bodies[i] != bodies[0] {
				t.Fatalf("%s, request %d got a different failure than request 0:\n%s\n---\n%s", round, i, bodies[i], bodies[0])
			}
		}
	}

	requestAll("first requests")
	if n := starts(t, log); n != 1 {
		t.Fatalf("Process started %d times for 10 simultaneous first requests, want 1", n)
	}

	requestAll("requests after the failure")
	if n := starts(t, log); n != 2 {
		t.Fatalf("Process started %d times after 10 requests that each got the failure, want 2", n)
	}
}

func TestStoppingTheDaemonEndsAStartInProgress(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	pidFile := filepath.Join(gitfixture.TempDir(t), "pid")
	repo := gitfixture.New(t, "lazy-shutdown").
		WriteFile("laneway.toml", processConfig("echo $$ > "+pidFile+"; exec "+testkit.TestServer(t)+" --bind-only")).
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
	var pid int
	testkit.WaitUntil(t, 15*time.Second, "a pid in "+file, func() bool {
		b, err := os.ReadFile(file)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	})
	return pid
}

// loadInBackground starts a browser page load of url, as a tab left
// loading does, without waiting for it to finish. The page load is
// cancelled, like a closed tab, when the test ends.
func loadInBackground(t *testing.T, d *testkit.Daemon, url string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	testkit.SetNavigationHeaders(req)
	client := d.HTTPS()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// startByBrowsing starts the Worktree served at domain the way a browser
// visiting it does, without waiting for its Primary Process to come up, and
// waits until `lane status` shows its Processes running.
func startByBrowsing(t *testing.T, d *testkit.Daemon, domain string) {
	t.Helper()
	loadInBackground(t, d, "https://"+domain+"/")
	testkit.WaitUntil(t, 10*time.Second, "`lane status` to show "+domain+"'s Processes running", func() bool {
		for line := range strings.Lines(d.MustRunCLI(d.Home, "status").Stdout) {
			if slices.Contains(strings.Fields(line), domain) && strings.Contains(line, ":running") {
				return true
			}
		}
		return false
	})
}

func TestRemovingAWorktreeEndsAStartInProgress(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	pids := gitfixture.TempDir(t)
	repo := gitfixture.New(t, "lazy-removed").
		WriteFile("laneway.toml", processConfig(
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
