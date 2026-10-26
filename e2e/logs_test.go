package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/daemon"
	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

func TestLogsShowAProcessOutput(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	repo := gitfixture.New(t, "logs-show").
		WriteFile("portby.toml", processConfig("echo hello from web; echo oops >&2; exec "+server)).
		Commit("declare web Process")

	d.MustRunCLI(repo.Dir, "link")

	out := waitForLogs(t, d, repo.Dir, []string{"logs", "web"}, "oops")
	assertLines(t, out, "hello from web", "oops")
}

// waitForLogs runs `portby <args>` in dir until its output contains want,
// because Process output reaches the daemon asynchronously. It returns the
// last stdout.
func waitForLogs(t *testing.T, d *testkit.Daemon, dir string, args []string, want string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		res := d.MustRunCLI(dir, args...)
		if strings.Contains(res.Stdout, want) {
			return res.Stdout
		}
		if time.Now().After(deadline) {
			t.Fatalf("portby %s never showed %q; last output:\n%s", strings.Join(args, " "), want, res.Output())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// assertLines checks out consists of exactly the lines want, in order.
func assertLines(t *testing.T, out string, want ...string) {
	t.Helper()
	got := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestLogsWithoutAProcessInterleaveAllOfThemWithNamePrefixes(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	// Two Processes take turns writing, handing over through files so the
	// order they wrote in is known.
	waitFor := func(file string) string { return "while [ ! -e " + file + " ]; do sleep 0.01; done; " }
	repo := gitfixture.New(t, "logs-interleave").
		WriteFile("portby.toml", "[processes.web]\nprimary = true\ncommand = \""+
			"echo first; touch a; "+waitFor("b")+"echo third; touch c; exec sleep 600\"\n"+
			"[processes.worker]\ncommand = \""+
			waitFor("a")+"echo second; touch b; "+waitFor("c")+"echo fourth; exec sleep 600\"\n").
		Commit("declare two Processes")

	d.MustRunCLI(repo.Dir, "link")

	out := waitForLogs(t, d, repo.Dir, []string{"logs"}, "fourth")
	assertLines(t, out, "web | first", "worker | second", "web | third", "worker | fourth")
}

func TestLogsFollowPrintsNewOutputAsItIsWritten(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "logs-follow").
		WriteFile("portby.toml", processConfig(
			"echo before; while [ ! -e go ]; do sleep 0.01; done; echo after; exec sleep 600")).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")
	waitForLogs(t, d, repo.Dir, []string{"logs", "web"}, "before")

	follow := d.StartCLI(repo.Dir, "logs", "web", "-f")
	follow.WaitForOutput("before")
	repo.WriteFile("go", "")
	follow.WaitForOutput("after")

	res := follow.Stop()
	if res.ExitCode != 0 {
		t.Fatalf("portby logs -f exited %d:\n%s", res.ExitCode, res.Output())
	}
	assertLines(t, res.Stdout, "before", "after")
}

func TestLogsFollowEndsWhenTheDaemonStops(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "logs-follow-stop").
		WriteFile("portby.toml", processConfig("echo hello; exec sleep 600")).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")
	follow := d.StartCLI(repo.Dir, "logs", "-f")
	follow.WaitForOutput("web | hello")

	d.Stop()

	follow.Wait()
}

func TestFullOutputIsWrittenToALogFileInTheStateDirectory(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "logs-file").
		WriteFile("portby.toml", processConfig("echo run; echo err >&2; exec sleep 600")).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")
	logFile := filepath.Join(d.Paths.StateDir, "logs", "logs-file.localhost", "web.log")
	waitForFile(t, logFile, "run\nerr\n")

	// Output from the next start is added to the same file.
	d.Restart()
	waitForFile(t, logFile, "run\nerr\nrun\nerr\n")
}

// waitForFile waits until the file at path holds exactly want.
func waitForFile(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil && string(data) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never held %q; last read: %q, %v", path, want, data, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLogFilesRotateAndKeepTheNewestOutput(t *testing.T) {
	t.Parallel()
	const maxBytes = 1000
	d := testkit.StartDaemon(t, func(c *daemon.Config) { c.LogFileMaxBytes = maxBytes })
	// 100 lines of 50 bytes: five times what one file may hold.
	repo := gitfixture.New(t, "logs-rotate").
		WriteFile("portby.toml", processConfig(
			`for i in $(seq 100 199); do printf 'line %s ........................................\n' $i; done; echo done; exec sleep 600`)).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")
	dir := filepath.Join(d.Paths.StateDir, "logs", "logs-rotate.localhost")
	waitForLogs(t, d, repo.Dir, []string{"logs", "web"}, "done")

	// The newest output is in web.log, older output in web.log.1 (newer)
	// to web.log.3 (oldest), and nothing older is kept.
	var kept string
	for _, name := range []string{"web.log.3", "web.log.2", "web.log.1", "web.log"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > maxBytes {
			t.Errorf("%s is %d bytes, want at most %d", name, len(data), maxBytes)
		}
		kept += string(data)
	}
	if _, err := os.Stat(filepath.Join(dir, "web.log.4")); !os.IsNotExist(err) {
		t.Errorf("web.log.4 exists (err = %v); want only 3 old files kept", err)
	}
	lines := strings.Split(strings.TrimSuffix(kept, "\n"), "\n")
	if len(lines) < 50 || lines[len(lines)-1] != "done" {
		t.Fatalf("log files hold %d lines ending %q; want the newest output, ending with done:\n%s",
			len(lines), lines[len(lines)-1], kept)
	}
	// What's kept is an unbroken run of the newest lines.
	first, _ := strconv.Atoi(strings.Fields(lines[0])[1])
	for i, l := range lines[:len(lines)-1] {
		if want := fmt.Sprintf("line %d ", first+i); !strings.HasPrefix(l, want) {
			t.Fatalf("kept line %d = %q, want it to start %q", i, l, want)
		}
	}
	if first+len(lines)-1 != 200 {
		t.Fatalf("kept lines end at %d, want 199", first+len(lines)-2)
	}
}

func TestLogsForAnUnknownProcessListTheRealOnes(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "logs-unknown").
		WriteFile("portby.toml", processConfig("exec sleep 600")).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")

	res := d.RunCLI(repo.Dir, "logs", "wbe")

	if res.ExitCode == 0 {
		t.Fatalf("portby logs wbe succeeded:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `no Process "wbe"`)
	assertContains(t, res.Stderr, "web")
}

func TestLogsOutsideALinkedProjectFailClearly(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "logs-unlinked")

	res := d.RunCLI(repo.Dir, "logs")

	if res.ExitCode == 0 {
		t.Fatalf("portby logs outside a Linked Project succeeded:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "portby link")
}
