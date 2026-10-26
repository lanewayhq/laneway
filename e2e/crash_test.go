package e2e

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/clock"
	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

func TestCrashedProcessRestartsAfterBackoff(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	d := testkit.StartDaemon(t, testkit.WithClock(clk))
	server := testkit.TestServer(t)
	// Crashes once it is up, on its first start only. (A Process that exits
	// before it is up fails the lazy start instead: see lazy_test.go.)
	crash := crashFile(t)
	repo := gitfixture.New(t, "crash-once").
		WriteFile("portby.toml", processConfig(
			"echo boot; if [ -e "+crash+" ]; then exec "+server+"; fi; exec "+server+" --exit-when "+crash)).
		Commit("declare web Process")

	d.MustRunCLI(repo.Dir, "link")
	d.GetServer("https://crash-once.localhost/")
	crashNow(t, crash)

	waitForLogs(t, d, repo.Dir, []string{"logs", "web"}, "restarting in 1s")
	if delay := waitForRestart(t, clk); delay != time.Second {
		t.Fatalf("first restart scheduled after %s, want 1s", delay)
	}
	if boots := countBoots(d, repo.Dir); boots != 1 {
		t.Fatalf("Process started %d times before its backoff passed, want 1", boots)
	}

	clk.Advance(time.Second)

	waitForBoots(t, d, repo.Dir, 2)
	d.GetServer("https://crash-once.localhost/")
}

// crashFile is a file for the web Process to crash on, with testserver
// --exit-when. Creating it (crashNow) makes the running Process, and every
// later start of it, exit as soon as it is up. Tests create it only after
// the requests that need the Process up: a crash on a timer races them.
func crashFile(t *testing.T) string {
	return filepath.Join(gitfixture.TempDir(t), "crash")
}

func crashNow(t *testing.T, file string) {
	t.Helper()
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitForTimer waits until the daemon schedules something on clk and returns
// how far away it is.
func waitForTimer(t *testing.T, clk *clock.Fake) time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := clk.WaitForTimer(ctx)
	if err != nil {
		t.Fatalf("the daemon never scheduled anything on the clock: %v", err)
	}
	return d
}

// waitForRestart waits until the daemon schedules a crash restart on clk and
// returns how far away it is. Restart backoffs are at most 30s, so the
// Worktree's idle timeout (minutes away) is ignored.
func waitForRestart(t *testing.T, clk *clock.Fake) time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := clk.WaitForTimerWithin(ctx, time.Minute)
	if err != nil {
		t.Fatalf("the daemon never scheduled a restart: %v", err)
	}
	return d
}

// waitForBoots waits until the web Process has printed "boot" n times.
func waitForBoots(t *testing.T, d *testkit.Daemon, dir string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for countBoots(d, dir) != n {
		if time.Now().After(deadline) {
			t.Fatalf("Process started %d times, want %d:\n%s", countBoots(d, dir), n, d.MustRunCLI(dir, "logs", "web").Stdout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// countBoots counts the "boot" lines the web Process has printed.
func countBoots(d *testkit.Daemon, dir string) int {
	out := d.MustRunCLI(dir, "logs", "web").Stdout
	return strings.Count(out, "boot\n")
}

func TestProcessThatKeepsCrashingIsMarkedFailed(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	d := testkit.StartDaemon(t, testkit.WithClock(clk))
	crash := crashFile(t)
	repo := gitfixture.New(t, "crash-loop").
		WriteFile("portby.toml", "idle_timeout = \"24h\"\n"+processConfig("echo boot; exec "+testkit.TestServer(t)+" --exit-when "+crash)).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")
	d.GetServer("https://crash-loop.localhost/")
	crashNow(t, crash)

	// Each restart waits twice as long as the one before.
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		delay := waitForRestart(t, clk)
		if delay != want {
			t.Fatalf("restart %d scheduled after %s, want %s", i+1, delay, want)
		}
		clk.Advance(delay)
		waitForBoots(t, d, repo.Dir, i+2)
	}

	// The fifth crash, 15s after the first, marks it failed.
	waitForLogs(t, d, repo.Dir, []string{"logs", "web"}, "won't be restarted")
	res := d.MustRunCLI(d.Home, "status")
	assertContains(t, res.Stdout, "web:failed")

	// It is not restarted, however long we wait: the only thing still
	// scheduled is its Worktree going Idle, a day away.
	if delay := waitForTimer(t, clk); delay < time.Hour {
		t.Fatalf("something is scheduled %s after the Process failed, want only Idle, a day away", delay)
	}
	clk.Advance(time.Hour)
	time.Sleep(100 * time.Millisecond)
	if boots := countBoots(d, repo.Dir); boots != 5 {
		t.Fatalf("Process started %d times, want 5", boots)
	}

	// Its Domain explains what happened.
	page := d.Get("https://crash-loop.localhost/")
	if page.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502:\n%s", page.StatusCode, page.Body)
	}
	assertContains(t, page.Body, "failed")
	assertContains(t, page.Body, "portby logs web")

	// Once the bug is fixed, `portby up` starts it again.
	if err := os.Remove(crash); err != nil {
		t.Fatal(err)
	}
	d.MustRunCLI(repo.Dir, "up")
	waitForBoots(t, d, repo.Dir, 6)
}

func TestCrashesSpreadOverMoreThanTwoMinutesDoNotFailAProcess(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	d := testkit.StartDaemon(t, testkit.WithClock(clk))
	crash := crashFile(t)
	repo := gitfixture.New(t, "crash-sometimes").
		WriteFile("portby.toml", processConfig("echo boot; exec "+testkit.TestServer(t)+" --exit-when "+crash)).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")
	d.GetServer("https://crash-sometimes.localhost/")
	crashNow(t, crash)

	// Each restart happens a minute after it's due, so no 2 minutes ever
	// hold more than 3 crashes.
	for boots := 2; boots <= 8; boots++ {
		clk.Advance(waitForRestart(t, clk) + time.Minute)
		waitForBoots(t, d, repo.Dir, boots)
	}

	waitForRestart(t, clk)
	res := d.MustRunCLI(d.Home, "status")
	assertContains(t, res.Stdout, "web:restarting")
}
