package e2e

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// modesProject is a repo with a lazy Primary Process web and one more HTTP
// Process, other (served at other.<domain>), with the given Start Mode. Each writes its pid to a file
// outside the repo; it returns their paths.
func modesProject(t *testing.T, name, mode string) (repo *gitfixture.Repo, webPID, otherPID string) {
	t.Helper()
	server := testkit.TestServer(t)
	pids := gitfixture.TempDir(t)
	webPID, otherPID = filepath.Join(pids, "web"), filepath.Join(pids, "other")
	config := fmt.Sprintf("idle_timeout = \"10m\"\n\n"+
		"[processes.web]\ncommand = %q\nprimary = true\n\n"+
		"[processes.other]\ncommand = %q\nhttp = true\nstart_mode = %q\n",
		"echo $$ > "+webPID+"; exec "+server,
		"echo $$ > "+otherPID+"; exec "+server+" --name other",
		mode)
	repo = gitfixture.New(t, name).WriteFile("portby.toml", config).Commit("declare Processes")
	return repo, webPID, otherPID
}

// waitForNewPID waits for a Process other than old to write its pid to file.
func waitForNewPID(t *testing.T, file string, old int) int {
	t.Helper()
	var pid int
	eventually(t, "a new Process to start", func() bool {
		b, err := os.ReadFile(file)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && pid != old
	})
	return pid
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestAlwaysProcessesStartWithTheDaemonAndAreNotStoppedByIdle(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	repo, webPID, otherPID := modesProject(t, "mode-always", "always")

	d.MustRunCLI(repo.Dir, "link")
	first := waitForPID(t, otherPID)

	d.Restart()
	waitForExit(t, first)
	other := waitForNewPID(t, otherPID, first)
	if state := worktreeState(t, d, "mode-always.localhost").State; state != "stopped" {
		t.Fatalf("Worktree state %q with only its always Process running, want stopped", state)
	}

	d.GetServer("https://mode-always.localhost/")
	web := waitForPID(t, webPID)
	clk.Advance(waitForTimer(t, clk))
	waitForExit(t, web)
	waitForState(t, d, "mode-always.localhost", "idle")
	if !alive(other) {
		t.Fatalf("Idle stopped the always Process")
	}
}

func TestManualProcessesOnlyStartThroughUp(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	repo, webPID, otherPID := modesProject(t, "mode-manual", "manual")
	d.MustRunCLI(repo.Dir, "link")

	d.GetServer("https://mode-manual.localhost/")
	web := waitForPID(t, webPID)
	waitForState(t, d, "mode-manual.localhost", "running")
	if _, err := os.Stat(otherPID); err == nil {
		t.Fatalf("a request started the manual Process")
	}

	// Its <process>.<domain> says how to start it.
	page := d.Get("https://other.mode-manual.localhost/")
	if page.StatusCode != http.StatusBadGateway {
		t.Fatalf("request to a manual Process that isn't running: status %d, want 502:\n%s", page.StatusCode, page.Body)
	}
	assertContains(t, page.Body, "portby up other")

	d.MustRunCLI(repo.Dir, "up", "other")
	other := waitForPID(t, otherPID)
	if res := d.GetServer("https://other.mode-manual.localhost/"); res.Name != "other" {
		t.Fatalf("other.mode-manual.localhost served by %q, want other", res.Name)
	}

	// Idle stops the lazy Primary Process but leaves what was started by hand.
	clk.Advance(waitForTimer(t, clk))
	waitForExit(t, web)
	waitForState(t, d, "mode-manual.localhost", "idle")
	if !alive(other) {
		t.Fatalf("Idle stopped the manual Process")
	}
}
