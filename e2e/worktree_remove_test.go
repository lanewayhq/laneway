package e2e

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// statusRows are the first column of each row of `lane status`: the main
// Worktrees' Project names and the linked Worktrees' names.
func statusRows(t *testing.T, d *testkit.Daemon) []string {
	t.Helper()
	res := d.MustRunCLI(d.Home, "status")
	var rows []string
	for _, line := range strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n")[1:] {
		if f := strings.Fields(line); len(f) > 0 {
			rows = append(rows, f[0])
		}
	}
	return rows
}

// assertGone fails the test unless the directory dir no longer exists.
func assertGone(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s still exists (stat: %v)", dir, err)
	}
}

// hasBranch reports whether the repository at dir has the local branch.
func hasBranch(t *testing.T, dir, branch string) bool {
	t.Helper()
	return gitfixture.Git(t, dir, "branch", "--list", branch) != ""
}

func TestRemovingAMergedCleanWorktreeDeletesItsCheckoutAndBranchAndItsDatabaseBecomesAnOrphan(t *testing.T) {
	t.Parallel()
	d, _ := orphanDaemon(t)
	repo := postgresProject(t, "remove-merged", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	res := d.MustRunCLI(repo.Dir, "worktree", "remove", "feature", "--force")

	assertContains(t, res.Stdout, `Removed Worktree "feature"`)
	assertGone(t, wt.Dir)
	if hasBranch(t, repo.Dir, "feature") {
		t.Errorf("the merged branch feature was kept")
	}
	for _, row := range statusRows(t, d) {
		if row == "feature" {
			t.Errorf("lane status still lists the Worktree feature")
		}
	}
	waitForOrphans(t, d, "remove_merged__feature")
}

func TestRemovingAWorktreeWithUnmergedCommitsListsThemAndKeepsItsBranch(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, wt := mergeProject(t, d, "remove-unmerged", "feature")
	wt.WriteFile("a.txt", "a\n").Commit("Add a")
	wt.WriteFile("b.txt", "b\n").Commit("Add b")
	head := wt.Git("rev-parse", "HEAD")

	res := d.RunCLIInTerminal(repo.Dir, "y\n", "worktree", "remove", "feature")

	if res.ExitCode != 0 {
		t.Fatalf("remove exited %d:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stdout, "2 commits are not on the Default Branch main")
	assertContains(t, res.Stdout, head[:7]+" Add b")
	assertContains(t, res.Stdout, "Add a")
	assertContains(t, res.Stdout, `Remove Worktree "feature"? [y/N]`)
	assertGone(t, wt.Dir)
	waitForNoWorktree(t, d, "remove-unmerged", "feature")
	if got := repo.Git("rev-parse", "refs/heads/feature"); got != head {
		t.Fatalf("the branch feature is at %s, want kept at %s", got, head)
	}
}

func TestAWorktreeWithChangesIsOnlyRemovedOnceTheListedFilesAreConfirmed(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, wt := mergeProject(t, d, "remove-changed", "feature")
	wt.WriteFile(".gitignore", ".env\n").Commit("Ignore .env")
	wt.WriteFile(".gitignore", ".env\n*.log\n") // modified
	wt.WriteFile("notes.txt", "draft\n")        // untracked
	wt.WriteFile(".env", "SECRET=1\n")          // ignored

	for _, res := range []testkit.CLIResult{
		d.RunCLIInTerminal(repo.Dir, "", "worktree", "remove", "feature"), // no answer
		d.RunCLI(repo.Dir, "worktree", "remove", "feature"),               // no one to ask
	} {
		if res.ExitCode == 0 {
			t.Fatalf("remove without confirming exited 0:\n%s", res.Output())
		}
		assertContains(t, res.Stdout, "2 files have uncommitted changes, which are discarded")
		assertContains(t, res.Stdout, " M .gitignore")
		assertContains(t, res.Stdout, "?? notes.txt")
		assertContains(t, res.Stderr, "nothing was removed")
		if _, err := os.Stat(wt.Dir + "/notes.txt"); err != nil {
			t.Fatalf("refused, but the changes went: %v", err)
		}
	}

	res := d.RunCLIInTerminal(repo.Dir, "y\n", "worktree", "remove", "feature")

	if res.ExitCode != 0 {
		t.Fatalf("remove, confirmed, exited %d:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stdout, `Remove Worktree "feature" and discard its changes? [y/N]`)
	assertGone(t, wt.Dir)
	waitForNoWorktree(t, d, "remove-changed", "feature")
}

func TestRemovingAWorktreeNotOnABranchWarnsThatItsUnmergedCommitsBecomeUnreachable(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, wt := mergeProject(t, d, "remove-detached", "feature")
	wt.Git("checkout", "--quiet", "--detach")
	wt.WriteFile("a.txt", "a\n").Commit("Add a")

	res := d.RunCLI(repo.Dir, "worktree", "remove", "feature")

	if res.ExitCode == 0 {
		t.Fatalf("remove without a terminal or --force exited 0 though it loses a commit:\n%s", res.Output())
	}
	assertContains(t, res.Stdout, "not on any branch")
	assertContains(t, res.Stdout, "1 commit is not on the Default Branch main")
	assertContains(t, res.Stdout, "Add a")
	assertContains(t, res.Stdout, "No branch keeps them, so they become unreachable")

	res = d.MustRunCLI(repo.Dir, "worktree", "remove", "feature", "--force")
	assertContains(t, res.Stdout, `Removed Worktree "feature"`)
	assertGone(t, wt.Dir)
}

func TestABranchMergedLocallyAtTheRemoteOrByRebaseIsDeletedWithItsWorktree(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	for _, tc := range []struct {
		name  string
		merge func(repo *gitfixture.Repo, wt *gitfixture.Worktree)
	}{
		{"local", func(repo *gitfixture.Repo, wt *gitfixture.Worktree) {
			repo.Git("merge", "--quiet", "--no-ff", "--no-edit", "local") // not pushed
		}},
		{"remote", func(repo *gitfixture.Repo, wt *gitfixture.Worktree) {
			// Merged at the remote and fetched; the local main is behind.
			repo.Git("update-ref", "refs/remotes/origin/main", wt.Git("rev-parse", "HEAD"))
		}},
		{"rebase", func(repo *gitfixture.Repo, wt *gitfixture.Worktree) {
			repo.WriteFile("main.txt", "main\n").Commit("Work on main")
			repo.Git("cherry-pick", "main..rebase")
		}},
	} {
		repo, wt := mergeProject(t, d, "remove-merged-"+tc.name, tc.name)
		// origin can't be reached: removing must not fetch.
		unreachableRemote(repo, "main", repo.Git("rev-parse", "HEAD"))
		wt.WriteFile("a.txt", "a\n").Commit("Add a")
		tc.merge(repo, wt)

		res := d.MustRunCLI(repo.Dir, "worktree", "remove", tc.name)

		assertContains(t, res.Stdout, "It is merged into the Default Branch main")
		assertGone(t, wt.Dir)
		if hasBranch(t, repo.Dir, tc.name) {
			t.Errorf("merged %s: the branch was kept", tc.name)
		}
	}
}

func TestTheMainWorktreeAndALockedWorktreeAreNotRemoved(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, wt := mergeProject(t, d, "remove-refused", "feature")
	repo.Git("worktree", "lock", "--reason", "an agent is working here", wt.Dir)

	for _, args := range [][]string{
		{"worktree", "remove", "--force"}, // the current Worktree: the main one
		{"worktree", "remove", "remove-refused", "--force"},
	} {
		res := d.RunCLI(repo.Dir, args...)
		if res.ExitCode == 0 {
			t.Fatalf("lane %v exited 0:\n%s", args, res.Output())
		}
		assertContains(t, res.Stderr, "the main Worktree of \"remove-refused\" can't be Removed")
		assertContains(t, res.Stderr, "lane unlink")
	}

	res := d.RunCLI(repo.Dir, "worktree", "remove", "feature", "--force")
	if res.ExitCode == 0 {
		t.Fatalf("removing a locked Worktree exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `Worktree "feature" is locked: an agent is working here`)
	if _, err := os.Stat(wt.Dir); err != nil {
		t.Fatalf("the locked Worktree went: %v", err)
	}
	if _, err := os.Stat(repo.Dir); err != nil {
		t.Fatalf("the main Worktree went: %v", err)
	}
	waitForWorktree(t, d, "remove-refused", "feature")
}

// removeRequest sends POST /v1/worktree/remove to the Dashboard at dash from
// its own pages, and decodes the response or the error.
func removeRequest(t *testing.T, d *testkit.Daemon, dash string, req api.RemoveRequest) (int, api.RemoveResponse, api.Error) {
	t.Helper()
	status, body := dashboardRequest(t, d, http.MethodPost, dash+"/api/v1/worktree/remove", dash, req)
	var res api.RemoveResponse
	var e api.ErrorResponse
	if status == http.StatusOK {
		if err := json.Unmarshal([]byte(body), &res); err != nil {
			t.Fatalf("%v:\n%s", err, body)
		}
	} else if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("status %d, %v:\n%s", status, err, body)
	}
	return status, res, e.Error
}

// confirming is the request that removes the Worktree, sending back what
// the preview p showed.
func confirming(dir string, p api.RemoveResponse) api.RemoveRequest {
	req := api.RemoveRequest{Dir: dir, Worktree: p.Worktree.Name, Branch: p.Branch}
	for _, c := range p.UnmergedCommits {
		req.UnmergedCommits = append(req.UnmergedCommits, c.Hash)
	}
	for _, f := range p.ChangedFiles {
		req.ChangedFiles = append(req.ChangedFiles, f.Path)
	}
	return req
}

func TestTheDashboardPreviewsWhatRemovingAWorktreeLoses(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-remove-preview")
	repo := postgresProject(t, "remove-preview", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "api")
	wt.WriteFile("a.txt", "a\n").Commit("Add a")
	wt.WriteFile("notes.txt", "draft\n")
	before := time.Now().Add(-time.Minute)

	status, p, e := removeRequest(t, d, dash, api.RemoveRequest{Dir: repo.Dir, Worktree: "feature", Preview: true})

	if status != http.StatusOK {
		t.Fatalf("preview: status %d: %+v", status, e)
	}
	if p.Project != "remove-preview" || p.Worktree.Name != "feature" || p.Worktree.Path != wt.Dir {
		t.Errorf("preview of %q Worktree %q at %s, want remove-preview's feature at %s", p.Project, p.Worktree.Name, p.Worktree.Path, wt.Dir)
	}
	if p.Branch != "feature" || p.DefaultBranch != "main" || p.Merged || p.Clean || p.Locked || p.Removed {
		t.Errorf("preview = %+v, want on feature, Default Branch main, unmerged, unclean, not locked, not removed", p)
	}
	if len(p.UnmergedCommits) != 1 || p.UnmergedCommits[0].Hash != wt.Git("rev-parse", "HEAD") ||
		p.UnmergedCommits[0].Subject != "Add a" || p.UnmergedCommits[0].Time.Before(before) {
		t.Errorf("unmerged commits = %+v, want Add a, just committed", p.UnmergedCommits)
	}
	if want := []api.ChangedFile{{Path: "notes.txt", Status: "??"}}; !slices.Equal(p.ChangedFiles, want) {
		t.Errorf("changed files = %+v, want %+v", p.ChangedFiles, want)
	}
	if want := []string{"api.remove-preview.localhost", "feature.remove-preview.localhost"}; !slices.Equal(p.Domains, want) {
		t.Errorf("Domains = %v, want %v (the Primary Domain first)", p.Domains, want)
	}
	if len(p.Orphans) != 1 || p.Orphans[0].Name != "remove_preview__feature" || p.Orphans[0].Kind != api.OrphanDatabase ||
		p.Orphans[0].Expires.Sub(p.Orphans[0].Since) != 24*time.Hour {
		t.Errorf("Orphans = %+v, want the database remove_preview__feature, kept 24 hours", p.Orphans)
	}
	if _, ok := findWorktree(t, d, "remove-preview", "feature"); !ok {
		t.Fatal("the preview removed the Worktree")
	}
}

func TestTheRemovePreviewListsTheTestDatabaseAmongTheOrphans(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-remove-testdb")
	repo := postgresProject(t, "remove-testdb", `test = "on"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	status, p, e := removeRequest(t, d, dash, api.RemoveRequest{Dir: repo.Dir, Worktree: "feature", Preview: true})

	if status != http.StatusOK {
		t.Fatalf("preview: status %d: %+v", status, e)
	}
	kinds := map[string]api.OrphanKind{}
	for _, o := range p.Orphans {
		kinds[o.Name] = o.Kind
	}
	want := map[string]api.OrphanKind{"remove_testdb__feature": api.OrphanDatabase, "remove_testdb__feature_test": api.OrphanTestDatabase}
	if !maps.Equal(kinds, want) {
		t.Errorf("Orphans = %v, want the database and the Test Database %v", kinds, want)
	}
}

func TestARemovalIsRefusedWhenTheWorktreeChangedSinceThePreviewOrIsLocked(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-remove-changed")
	repo, wt := mergeProject(t, d, "remove-changed-since", "feature")
	wt.WriteFile("a.txt", "a\n").Commit("Add a")
	_, preview, _ := removeRequest(t, d, dash, api.RemoveRequest{Dir: repo.Dir, Worktree: "feature", Preview: true})

	for what, change := range map[string]func(){
		"a new commit":   func() { wt.WriteFile("b.txt", "b\n").Commit("Add b") },
		"a changed file": func() { wt.WriteFile("b.txt", "changed\n") },
		"a new branch":   func() { wt.Git("checkout", "--quiet", "-b", "other") },
	} {
		change()
		status, _, e := removeRequest(t, d, dash, confirming(repo.Dir, preview))
		if status != http.StatusConflict || e.Code != api.CodeWorktreeChanged {
			t.Fatalf("after %s: status %d, %+v; want 409 %s", what, status, e, api.CodeWorktreeChanged)
		}
		assertContains(t, e.Message, "changed since the preview")
		if _, err := os.Stat(wt.Dir); err != nil {
			t.Fatalf("refused after %s, but the Worktree went: %v", what, err)
		}
		_, preview, _ = removeRequest(t, d, dash, api.RemoveRequest{Dir: repo.Dir, Worktree: "feature", Preview: true})
	}

	repo.Git("worktree", "lock", "--reason", "in use by an agent", wt.Dir)
	_, preview, _ = removeRequest(t, d, dash, api.RemoveRequest{Dir: repo.Dir, Worktree: "feature", Preview: true})
	if !preview.Locked || preview.LockReason != "in use by an agent" {
		t.Fatalf("preview of a locked Worktree = %+v, want locked with its reason", preview)
	}
	status, _, e := removeRequest(t, d, dash, confirming(repo.Dir, preview))
	if status != http.StatusConflict || e.Code != api.CodeWorktreeLocked {
		t.Fatalf("removing a locked Worktree: status %d, %+v; want 409 %s", status, e, api.CodeWorktreeLocked)
	}
	assertContains(t, e.Message, "in use by an agent")
	waitForWorktree(t, d, "remove-changed-since", "feature")
}

func TestOnlyTheDashboardsOwnPagesCanRemoveAWorktree(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-remove-origin")
	repo, wt := mergeProject(t, d, "dash-remove-origin-shop", "feature")
	req := api.RemoveRequest{Dir: repo.Dir, Worktree: "feature", Branch: "feature"}

	for _, origin := range []string{"", "https://dash-remove-origin-shop.localhost", "https://evil.example", "null"} {
		status, body := dashboardRequest(t, d, http.MethodPost, dash+"/api/v1/worktree/remove", origin, req)
		if status != http.StatusForbidden {
			t.Errorf("Origin %q: status %d, want 403:\n%s", origin, status, body)
		}
	}
	if _, err := os.Stat(wt.Dir); err != nil {
		t.Fatalf("a refused request removed the Worktree: %v", err)
	}

	status, res, e := removeRequest(t, d, dash, req)

	if status != http.StatusOK || !res.Removed || !res.BranchDeleted {
		t.Fatalf("from the Dashboard's own origin: status %d, %+v, %+v; want removed with its branch", status, res, e)
	}
	assertGone(t, wt.Dir)
	if _, ok := findWorktree(t, d, "dash-remove-origin-shop", "feature"); ok {
		t.Fatal("GET /v1/projects still lists the removed Worktree")
	}
}

func TestDeleteBranchAlsoDeletesABranchWithUnmergedCommits(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, wt := mergeProject(t, d, "remove-delete-branch", "feature")
	wt.WriteFile("a.txt", "a\n").Commit("Add a")

	// Without a terminal it would lose the commit, so it needs --force.
	res := d.RunCLI(repo.Dir, "worktree", "remove", "feature", "--delete-branch")
	if res.ExitCode == 0 {
		t.Fatalf("remove --delete-branch without a terminal or --force exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stdout, "become unreachable")
	assertContains(t, res.Stderr, "--force")
	if _, ok := findWorktree(t, d, "remove-delete-branch", "feature"); !ok || !hasBranch(t, repo.Dir, "feature") {
		t.Fatal("refused, but the Worktree or its branch went")
	}

	d.MustRunCLI(repo.Dir, "worktree", "remove", "feature", "--delete-branch", "--force")

	assertGone(t, wt.Dir)
	if hasBranch(t, repo.Dir, "feature") {
		t.Fatal("--delete-branch kept the branch feature")
	}
}

// The Remove dialog names the Worktree by its path, sends back everything
// its preview listed and, when "Also delete branch" is ticked, deleteBranch.
func TestTheDashboardRemovesAWorktreeAsItsDialogConfirmsIt(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-remove-dialog")
	repo := postgresProject(t, "remove-dialog", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	wt.WriteFile("a.txt", "a\n").Commit("Add a")
	wt.WriteFile("notes.txt", "draft\n")
	events := projectEvents(t, d, dash+"/api/v1/events")
	waitForEvent(t, events, "the Worktree feature", func(l api.ProjectList) bool {
		_, ok := worktreeIn(l, "feature.remove-dialog.localhost")
		return ok
	})

	status, preview, e := removeRequest(t, d, dash, api.RemoveRequest{Dir: repo.Dir, Worktree: wt.Dir, Preview: true})
	if status != http.StatusOK || preview.Merged || preview.Clean {
		t.Fatalf("preview: status %d, %+v, %+v; want unmerged with changes", status, preview, e)
	}
	req := confirming(repo.Dir, preview)
	req.Worktree = wt.Dir
	req.DeleteBranch = true

	status, res, e := removeRequest(t, d, dash, req)

	if status != http.StatusOK || !res.Removed || !res.BranchDeleted {
		t.Fatalf("confirm: status %d, %+v, %+v; want removed with its branch", status, res, e)
	}
	assertGone(t, wt.Dir)
	if hasBranch(t, repo.Dir, "feature") {
		t.Error("deleteBranch kept the branch feature")
	}
	waitForEvent(t, events, "no Worktree feature", func(l api.ProjectList) bool {
		_, ok := worktreeIn(l, "feature.remove-dialog.localhost")
		return !ok
	})
	status, body := dashboardRequest(t, d, http.MethodGet, dash+"/api/v1/orphans", "", nil)
	var orphans api.OrphanList
	if err := json.Unmarshal([]byte(body), &orphans); status != http.StatusOK || err != nil {
		t.Fatalf("GET /api/v1/orphans: status %d, %v:\n%s", status, err, body)
	}
	if len(orphans.Orphans) != 1 || orphans.Orphans[0].Name != "remove_dialog__feature" {
		t.Errorf("Orphans = %+v, want the database remove_dialog__feature", orphans.Orphans)
	}
}
