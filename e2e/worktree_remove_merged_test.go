package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/clock"
	"github.com/lanewayhq/laneway/internal/daemon"
	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// waitForMerge waits until GET /v1/projects reports a merge status for the
// Project's linked Worktree name that satisfies cond.
func waitForMerge(t *testing.T, d *testkit.Daemon, project, name, what string, cond func(api.MergeStatus) bool) {
	t.Helper()
	eventually(t, "Worktree "+name+" to be "+what, func() bool {
		w, ok := findWorktree(t, d, project, name)
		return ok && w.Merge != nil && cond(*w.Merge)
	})
}

// assertKept fails the test unless the Worktree's checkout is still there
// and the API still lists it.
func assertKept(t *testing.T, d *testkit.Daemon, project, name, dir string) {
	t.Helper()
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("Worktree %s of %s went: %v", name, project, err)
	}
	if _, ok := findWorktree(t, d, project, name); !ok {
		t.Errorf("GET /v1/projects no longer lists Worktree %s of %s", name, project)
	}
}

func TestRemoveMergedRemovesOnlyTheCurrentProjectsMergedCleanWorktrees(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, done := mergeProject(t, d, "bulk-shop", "done")
	wip := repo.AddWorktree("wip")
	wip.WriteFile("a.txt", "a\n").Commit("Add a")
	dirty := repo.AddWorktree("dirty")
	dirty.WriteFile("notes.txt", "draft\n")
	locked := repo.AddWorktree("locked")
	repo.Git("worktree", "lock", "--reason", "an agent is working here", locked.Dir)
	_, otherDone := mergeProject(t, d, "bulk-other", "done")
	waitForMerge(t, d, "bulk-shop", "wip", "unmerged", func(m api.MergeStatus) bool { return !m.Merged })
	waitForMerge(t, d, "bulk-shop", "dirty", "changed", func(m api.MergeStatus) bool { return !m.Clean })
	waitForWorktree(t, d, "bulk-shop", "locked")

	res := d.RunCLI(repo.Dir, "worktree", "remove", "--merged", "--force")

	if res.ExitCode != 0 {
		t.Fatalf("remove --merged exited %d:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stdout, `Removed Worktree "done" of bulk-shop and its branch done`)
	assertGone(t, done.Dir)
	if hasBranch(t, repo.Dir, "done") {
		t.Errorf("the merged branch done was kept")
	}
	assertKept(t, d, "bulk-shop", "wip", wip.Dir)
	assertKept(t, d, "bulk-shop", "dirty", dirty.Dir)
	assertKept(t, d, "bulk-shop", "locked", locked.Dir)
	if strings.Contains(res.Output(), `"locked"`) {
		t.Errorf("the locked Worktree was listed or reported:\n%s", res.Output())
	}
	assertKept(t, d, "bulk-other", "done", otherDone.Dir)
}

func TestRemoveMergedAllCoversEveryProjectAndTheirDatabasesBecomeOrphans(t *testing.T) {
	t.Parallel()
	d, _ := orphanDaemon(t)
	var removed []string
	for _, name := range []string{"bulk-all-shop", "bulk-all-blog"} {
		repo := postgresProject(t, name, "")
		d.MustRunCLI(repo.Dir, "link", "--yes")
		d.MustRunCLI(repo.Dir, "worktree", "ready")
		wt := repo.AddWorktree("done")
		d.MustRunCLI(wt.Dir, "worktree", "ready")
		removed = append(removed, wt.Dir)
	}
	elsewhere := gitfixture.TempDir(t)

	res := d.RunCLI(elsewhere, "worktree", "remove", "--merged", "--all", "--force")

	if res.ExitCode != 0 {
		t.Fatalf("remove --merged --all exited %d:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stdout, `Removed Worktree "done" of bulk-all-shop and its branch done`)
	assertContains(t, res.Stdout, `Removed Worktree "done" of bulk-all-blog and its branch done`)
	for _, dir := range removed {
		assertGone(t, dir)
	}
	waitForOrphans(t, d, "bulk_all_shop__done", "bulk_all_blog__done")
}

func TestRemoveMergedAsksOnceAndRemovesNothingUnlessTheAnswerIsYes(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, one := mergeProject(t, d, "bulk-ask", "one")
	two := repo.AddWorktree("two")
	waitForWorktree(t, d, "bulk-ask", "two")

	for what, res := range map[string]testkit.CLIResult{
		"no":            d.RunCLIInTerminal(repo.Dir, "n\n", "worktree", "remove", "--merged"),
		"no answer":     d.RunCLIInTerminal(repo.Dir, "", "worktree", "remove", "--merged"),
		"no one to ask": d.RunCLI(repo.Dir, "worktree", "remove", "--merged"),
	} {
		if res.ExitCode == 0 {
			t.Fatalf("%s: remove --merged exited 0:\n%s", what, res.Output())
		}
		assertContains(t, res.Stdout, `"one" of bulk-ask, on the branch one, at `+one.Dir)
		assertContains(t, res.Stdout, `"two" of bulk-ask, on the branch two, at `+two.Dir)
		assertContains(t, res.Stderr, "nothing was removed")
		assertKept(t, d, "bulk-ask", "one", one.Dir)
		assertKept(t, d, "bulk-ask", "two", two.Dir)
	}

	res := d.RunCLIInTerminal(repo.Dir, "y\n", "worktree", "remove", "--merged")

	if res.ExitCode != 0 {
		t.Fatalf("remove --merged, confirmed, exited %d:\n%s", res.ExitCode, res.Output())
	}
	if n := strings.Count(res.Stdout, "[y/N]"); n != 1 {
		t.Errorf("asked %d times, want once:\n%s", n, res.Stdout)
	}
	assertContains(t, res.Stdout, "Remove these 2 Worktrees? [y/N]")
	assertGone(t, one.Dir)
	assertGone(t, two.Dir)
	waitForNoWorktree(t, d, "bulk-ask", "one")
	waitForNoWorktree(t, d, "bulk-ask", "two")

	res = d.MustRunCLI(repo.Dir, "worktree", "remove", "--merged")
	assertContains(t, res.Stdout, "No Worktree of bulk-ask is merged and clean, so there is nothing to remove.")
}

// answerAfter is a terminal's input that first runs change, as if someone
// changed something while the question was on screen, and then answers.
type answerAfter struct {
	change func()
	answer io.Reader
}

func (a *answerAfter) Read(p []byte) (int, error) {
	if a.change != nil {
		a.change()
		a.change = nil
	}
	return a.answer.Read(p)
}

func TestRemoveMergedSkipsAndReportsAWorktreeThatChangedAfterItWasListed(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, committed := mergeProject(t, d, "bulk-changed", "committed")
	edited := repo.AddWorktree("edited")
	locked := repo.AddWorktree("locked")
	done := repo.AddWorktree("done")
	waitForWorktree(t, d, "bulk-changed", "done")
	waitForWorktree(t, d, "bulk-changed", "locked")
	waitForWorktree(t, d, "bulk-changed", "edited")

	res := d.RunCLIInTerminalReading(repo.Dir, &answerAfter{
		change: func() {
			committed.WriteFile("a.txt", "a\n").Commit("Add a")
			edited.WriteFile("notes.txt", "draft\n")
			repo.Git("worktree", "lock", "--reason", "an agent is working here", locked.Dir)
		},
		answer: strings.NewReader("y\n"),
	}, "worktree", "remove", "--merged")

	if res.ExitCode == 0 {
		t.Fatalf("remove --merged exited 0 though it skipped Worktrees:\n%s", res.Output())
	}
	assertContains(t, res.Stdout, "Remove these 4 Worktrees? [y/N]")
	assertContains(t, res.Stdout, `Removed Worktree "done" of bulk-changed and its branch done`)
	assertContains(t, res.Stderr, `skipped Worktree "committed" of bulk-changed: Worktree "committed" changed since the preview`)
	assertContains(t, res.Stderr, `skipped Worktree "edited" of bulk-changed: Worktree "edited" changed since the preview`)
	assertContains(t, res.Stderr, `skipped Worktree "locked" of bulk-changed: Worktree "locked" is locked: an agent is working here`)
	assertContains(t, res.Stderr, "3 of 4 Worktrees were not removed")
	assertGone(t, done.Dir)
	assertKept(t, d, "bulk-changed", "committed", committed.Dir)
	assertKept(t, d, "bulk-changed", "edited", edited.Dir)
	assertKept(t, d, "bulk-changed", "locked", locked.Dir)
	if !hasBranch(t, repo.Dir, "committed") {
		t.Error("the branch of the skipped Worktree committed went")
	}
}

func TestRemoveMergedTakesNoNameNorDeleteBranchAndAllNeedsMerged(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, wt := mergeProject(t, d, "bulk-usage", "done")

	for _, args := range [][]string{
		{"worktree", "remove", "done", "--merged", "--force"},
		{"worktree", "remove", "--merged", "--delete-branch", "--force"},
		{"worktree", "remove", "--all", "--force"},
	} {
		res := d.RunCLI(repo.Dir, args...)
		if res.ExitCode != 2 {
			t.Errorf("lane %v exited %d, want 2 (usage):\n%s", args, res.ExitCode, res.Output())
		}
	}
	assertKept(t, d, "bulk-usage", "done", wt.Dir)
}

// removeAllRequest sends POST /v1/worktrees/remove to the Dashboard at dash
// from its own pages, and decodes the response.
func removeAllRequest(t *testing.T, d *testkit.Daemon, dash string, req api.RemoveAllRequest) api.RemoveAllResponse {
	t.Helper()
	status, body := dashboardRequest(t, d, http.MethodPost, dash+"/api/v1/worktrees/remove", dash, req)
	if status != http.StatusOK {
		t.Fatalf("POST /v1/worktrees/remove: status %d:\n%s", status, body)
	}
	var res api.RemoveAllResponse
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("%v:\n%s", err, body)
	}
	return res
}

func TestTheDashboardRemovesExactlyTheListedWorktreesInOneRequest(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-remove-all")
	repo, one := mergeProject(t, d, "dash-remove-all-shop", "one")
	two := repo.AddWorktree("two")
	changed := repo.AddWorktree("changed")
	locked := repo.AddWorktree("locked")
	unlisted := repo.AddWorktree("unlisted")
	for _, name := range []string{"two", "changed", "locked", "unlisted"} {
		waitForWorktree(t, d, "dash-remove-all-shop", name)
	}
	// What "Remove all N" lists: the merged, clean, unlocked Worktrees, as
	// GET /v1/projects reports them.
	var listed []api.RemoveRequest
	for _, name := range []string{"one", "two", "changed", "locked"} {
		w, _ := findWorktree(t, d, "dash-remove-all-shop", name)
		if m := w.Merge; m == nil || !m.Merged || !m.Clean || m.Locked {
			t.Fatalf("Worktree %s isn't ready to remove: %+v", name, m)
		}
		listed = append(listed, api.RemoveRequest{Dir: w.Path, Branch: w.Merge.Branch})
	}
	req := api.RemoveAllRequest{Worktrees: listed}
	// Changed while the developer looked at the list.
	changed.WriteFile("a.txt", "a\n").Commit("Add a")
	repo.Git("worktree", "lock", "--reason", "in use by an agent", locked.Dir)

	for _, origin := range []string{"", "https://evil.example"} {
		if status, body := dashboardRequest(t, d, http.MethodPost, dash+"/api/v1/worktrees/remove", origin, req); status != http.StatusForbidden {
			t.Errorf("Origin %q: status %d, want 403:\n%s", origin, status, body)
		}
	}
	assertKept(t, d, "dash-remove-all-shop", "one", one.Dir)

	res := removeAllRequest(t, d, dash, req)

	if len(res.Results) != 4 {
		t.Fatalf("got %d results, want one for each of the 4 listed: %+v", len(res.Results), res.Results)
	}
	for i, name := range []string{"one", "two"} {
		r := res.Results[i]
		if r.Error != nil || !r.Removed || !r.BranchDeleted || r.Worktree.Name != name {
			t.Errorf("result %d = %+v, error %+v; want %s removed with its branch", i, r.RemoveResponse, r.Error, name)
		}
	}
	if r := res.Results[2]; r.Removed || r.Error == nil || r.Error.Code != api.CodeWorktreeChanged || r.Worktree.Name != "changed" {
		t.Errorf("result for changed = %+v, error %+v; want not removed, %s", r.RemoveResponse, r.Error, api.CodeWorktreeChanged)
	}
	if r := res.Results[3]; r.Removed || r.Error == nil || r.Error.Code != api.CodeWorktreeLocked ||
		!r.Locked || r.LockReason != "in use by an agent" {
		t.Errorf("result for locked = %+v, error %+v; want not removed, %s with its reason", r.RemoveResponse, r.Error, api.CodeWorktreeLocked)
	}
	assertGone(t, one.Dir)
	assertGone(t, two.Dir)
	assertKept(t, d, "dash-remove-all-shop", "changed", changed.Dir)
	assertKept(t, d, "dash-remove-all-shop", "locked", locked.Dir)
	assertKept(t, d, "dash-remove-all-shop", "unlisted", unlisted.Dir)
	for _, name := range []string{"one", "two"} {
		if _, ok := findWorktree(t, d, "dash-remove-all-shop", name); ok {
			t.Errorf("GET /v1/projects still lists the removed Worktree %s", name)
		}
	}
}

func TestTheDashboardsRemoveAllSendsOnlyDirAndBranchAndTheDatabasesBecomeOrphans(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(orphanEpoch)
	domain := "dash-remove-all-orphans.localhost"
	d := testkit.StartDaemon(t, testkit.WithClock(clk), func(c *daemon.Config) { c.DashboardDomain = domain })
	dash := "https://" + domain
	repo := postgresProject(t, "dash-bulk-shop", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	var listed []map[string]string
	var dirs []string
	for _, name := range []string{"feature-x", "feature-y"} {
		wt := repo.AddWorktree(name)
		d.MustRunCLI(wt.Dir, "worktree", "ready")
		waitForMerge(t, d, "dash-bulk-shop", name, "merged and clean", func(m api.MergeStatus) bool { return m.Merged && m.Clean })
		w, _ := findWorktree(t, d, "dash-bulk-shop", name)
		// Exactly what "Remove all N" sends for each Worktree it listed.
		listed = append(listed, map[string]string{"dir": w.Path, "branch": w.Merge.Branch})
		dirs = append(dirs, wt.Dir)
	}

	status, body := dashboardRequest(t, d, http.MethodPost, dash+"/api/v1/worktrees/remove", dash,
		map[string]any{"worktrees": listed})

	if status != http.StatusOK {
		t.Fatalf("POST /v1/worktrees/remove: status %d:\n%s", status, body)
	}
	var res api.RemoveAllResponse
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("%v:\n%s", err, body)
	}
	want := []string{"dash_bulk_shop__feature_x", "dash_bulk_shop__feature_y"}
	if len(res.Results) != len(want) {
		t.Fatalf("got %d results, want %d: %+v", len(res.Results), len(want), res.Results)
	}
	for i, r := range res.Results {
		if r.Error != nil || !r.Removed {
			t.Errorf("result %d: removed %v, error %+v; want removed", i, r.Removed, r.Error)
		}
		if len(r.Orphans) != 1 || r.Orphans[0].Name != want[i] || r.Orphans[0].Expires.IsZero() {
			t.Errorf("result %d: Orphans %+v, want only %s with when it is dropped", i, r.Orphans, want[i])
		}
	}
	for _, dir := range dirs {
		assertGone(t, dir)
	}
	waitForOrphans(t, d, want...)
}
