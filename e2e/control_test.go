package e2e

import (
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

func TestDownStopsTheWorktreeAndTheNextRequestStartsItAgain(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, pidFile := pidProject(t, "ctl-down", "")
	d.MustRunCLI(repo.Dir, "link")
	d.GetServer("https://ctl-down.localhost/")
	pid := waitForPID(t, pidFile)

	res := d.MustRunCLI(repo.Dir, "down")
	assertContains(t, res.Stdout, "ctl-down.localhost is stopped")
	waitForExit(t, pid)
	if w := worktreeState(t, d, "ctl-down.localhost"); w.State != "stopped" || len(w.Processes) != 0 {
		t.Fatalf("after down: state %q, Processes %v; want stopped with none running", w.State, w.Processes)
	}

	d.GetServer("https://ctl-down.localhost/")
	waitForNewPID(t, pidFile, pid)
	waitForState(t, d, "ctl-down.localhost", "running")
}

func TestUpAndRestartActOnAnotherWorktreeWithTheWorktreeFlag(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, pidFile := pidProject(t, "ctl-other", "")
	d.MustRunCLI(repo.Dir, "link")
	wt := repo.AddWorktree("feature-x")
	waitForWorktree(t, d, "ctl-other", "feature-x")

	res := d.MustRunCLI(repo.Dir, "up", "--worktree", "feature-x")
	assertContains(t, res.Stdout, "feature-x.ctl-other.localhost is running (web:running)")
	pid := waitForPID(t, pidFile)
	if state := worktreeState(t, d, "ctl-other.localhost").State; state != "stopped" {
		t.Fatalf("the main Worktree is %q after `up --worktree feature-x`, want stopped", state)
	}

	d.MustRunCLI(repo.Dir, "restart", "--worktree", wt.Dir)
	waitForExit(t, pid)
	waitForNewPID(t, pidFile, pid)
	waitForState(t, d, "feature-x.ctl-other.localhost", "running")

	res = d.RunCLI(repo.Dir, "up", "--worktree", "nope")
	if res.ExitCode == 0 {
		t.Fatalf("up --worktree nope succeeded:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `Project "ctl-other" has no Worktree "nope"; its Worktrees are ctl-other, feature-x`)
}

func TestLogsOfAStoppedProcessShowItsKeptOutput(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "ctl-logs").
		WriteFile("portby.toml", processConfig("echo boot; exec "+testkit.TestServer(t))).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")
	startByBrowsing(t, d, "ctl-logs.localhost")
	waitForLogs(t, d, repo.Dir, []string{"logs", "web"}, "boot")

	d.MustRunCLI(repo.Dir, "down")

	assertLines(t, d.MustRunCLI(repo.Dir, "logs", "web").Stdout, "boot")
	res := d.RunCLI(repo.Dir, "logs", "wbe")
	if res.ExitCode == 0 {
		t.Fatalf("logs of an unknown Process succeeded:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `this Worktree has no Process "wbe"; its Processes are web`)
}

func TestDownOneProcessLeavesTheOthersAndARequestStartsItAgain(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, webPID, otherPID := modesProject(t, "ctl-one", "lazy")
	d.MustRunCLI(repo.Dir, "link")
	d.GetServer("https://ctl-one.localhost/")
	web, other := waitForPID(t, webPID), waitForPID(t, otherPID)

	res := d.MustRunCLI(repo.Dir, "down", "web")
	assertContains(t, res.Stdout, "(other:running)")
	waitForExit(t, web)

	d.GetServer("https://ctl-one.localhost/")
	waitForNewPID(t, webPID, web)
	if !alive(other) {
		t.Fatalf("down web stopped the other Process")
	}

	res = d.RunCLI(repo.Dir, "restart", "nope")
	if res.ExitCode == 0 {
		t.Fatalf("restart of an unknown Process succeeded:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `this Worktree has no Process "nope"; its Processes are other, web`)
}
