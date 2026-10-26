package e2e

import (
	"context"
	"net/http"
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
	// Crashes soon after its first start only. (A Process that exits
	// before it is up fails the lazy start instead: see lazy_test.go.)
	repo := gitfixture.New(t, "crash-once").
		WriteFile("portby.toml", processConfig(
			"echo boot; if [ -e crashed ]; then exec "+server+"; fi; touch crashed; exec "+server+" --exit-after 300ms")).
		Commit("declare web Process")

	d.MustRunCLI(repo.Dir, "link")
	d.GetServer("https://crash-once.localhost/")

	waitForLogs(t, d, repo.Dir, []string{"logs", "web"}, "restarting in 1s")
	if delay := waitForTimer(t, clk); delay != time.Second {
		t.Fatalf("first restart scheduled after %s, want 1s", delay)
	}
	if boots := countBoots(d, repo.Dir); boots != 1 {
		t.Fatalf("Process started %d times before its backoff passed, want 1", boots)
	}

	clk.Advance(time.Second)

	waitForBoots(t, d, repo.Dir, 2)
	d.GetServer("https://crash-once.localhost/")
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
	repo := gitfixture.New(t, "crash-loop").
		WriteFile("portby.toml", processConfig("echo boot; exec "+testkit.TestServer(t)+" --exit-after 300ms")).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")
	d.GetServer("https://crash-loop.localhost/")

	// Each restart waits twice as long as the one before.
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		delay := waitForTimer(t, clk)
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

	// It is not restarted, however long we wait.
	if n := clk.Pending(); n != 0 {
		t.Fatalf("%d calls still scheduled after the Process failed, want none", n)
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
}

func TestCrashesSpreadOverMoreThanTwoMinutesDoNotFailAProcess(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	d := testkit.StartDaemon(t, testkit.WithClock(clk))
	repo := gitfixture.New(t, "crash-sometimes").
		WriteFile("portby.toml", processConfig("echo boot; exec "+testkit.TestServer(t)+" --exit-after 300ms")).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")
	d.GetServer("https://crash-sometimes.localhost/")

	// Each restart happens a minute after it's due, so no 2 minutes ever
	// hold more than 3 crashes.
	for boots := 2; boots <= 8; boots++ {
		clk.Advance(waitForTimer(t, clk) + time.Minute)
		waitForBoots(t, d, repo.Dir, boots)
	}

	waitForTimer(t, clk)
	res := d.MustRunCLI(d.Home, "status")
	assertContains(t, res.Stdout, "web:restarting")
}
