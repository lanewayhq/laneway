package e2e

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// mergeProject Links a new repository named name and adds a linked Worktree
// called worktree to it, which the daemon has found when it returns.
func mergeProject(t *testing.T, d *testkit.Daemon, name, worktree string) (*gitfixture.Repo, *gitfixture.Worktree) {
	t.Helper()
	repo := gitfixture.New(t, name)
	d.MustRunCLI(repo.Dir, "link")
	wt := repo.AddWorktree(worktree)
	waitForWorktree(t, d, name, worktree)
	return repo, wt
}

// unreachableRemote makes origin a remote Laneway couldn't reach if it tried,
// whose Default Branch, as last fetched, is branch at the commit at.
func unreachableRemote(repo *gitfixture.Repo, branch, at string) {
	repo.Git("remote", "add", "origin", "https://laneway.invalid/unreachable.git")
	repo.Git("update-ref", "refs/remotes/origin/"+branch, at)
	repo.Git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+branch)
}

// projectNamed is the Project called name, as GET /v1/projects reports it.
func projectNamed(t *testing.T, d *testkit.Daemon, name string) api.Project {
	t.Helper()
	projects, err := d.Client().Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no Project %q in %+v", name, projects)
	return api.Project{}
}

// mergeOf is the merge status GET /v1/projects reports for the Project's
// linked Worktree called name.
func mergeOf(t *testing.T, d *testkit.Daemon, project, name string) api.MergeStatus {
	t.Helper()
	w, ok := findWorktree(t, d, project, name)
	if !ok {
		t.Fatalf("no Worktree %s in Project %s", name, project)
	}
	if w.Merge == nil {
		t.Fatalf("Worktree %s has no merge status: %+v", name, w)
	}
	return *w.Merge
}

func TestAFreshWorktreeIsMergedAndCleanOnItsBranch(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	mergeProject(t, d, "merge-fresh", "feature")

	p := projectNamed(t, d, "merge-fresh")
	if p.DefaultBranch != "main" {
		t.Errorf("Default Branch = %q, want main", p.DefaultBranch)
	}
	if p.Worktrees[0].Merge != nil {
		t.Errorf("the main Worktree has a merge status: %+v", p.Worktrees[0].Merge)
	}
	m := mergeOf(t, d, "merge-fresh", "feature")
	if m.Branch != "feature" || !m.Merged || !m.Clean || m.UnmergedCount != 0 || m.ChangedCount != 0 {
		t.Fatalf("merge status = %+v, want on branch feature, merged and clean", m)
	}
}

func TestALockedWorktreesMergeStatusSaysSoWithItsReason(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, wt := mergeProject(t, d, "merge-locked", "feature")
	if m := mergeOf(t, d, "merge-locked", "feature"); m.Locked || m.LockReason != "" {
		t.Fatalf("merge status of an unlocked Worktree = %+v, want not locked", m)
	}

	repo.Git("worktree", "lock", "--reason", "an agent is working here", wt.Dir)

	m := mergeOf(t, d, "merge-locked", "feature")
	if !m.Locked || m.LockReason != "an agent is working here" || !m.Merged || !m.Clean {
		t.Fatalf("merge status = %+v, want merged and clean, but locked with its reason", m)
	}

	repo.Git("worktree", "unlock", wt.Dir)
	repo.Git("worktree", "lock", wt.Dir)
	if m := mergeOf(t, d, "merge-locked", "feature"); !m.Locked || m.LockReason != "" {
		t.Fatalf("merge status after locking without a reason = %+v, want locked, no reason", m)
	}
	repo.Git("worktree", "unlock", wt.Dir)
	if m := mergeOf(t, d, "merge-locked", "feature"); m.Locked {
		t.Fatalf("merge status after unlocking = %+v, want not locked", m)
	}
}

func TestAWorktreeListsItsCommitsThatAreNotOnTheDefaultBranch(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, wt := mergeProject(t, d, "merge-ahead", "feature")
	before := time.Now().Add(-time.Minute)
	wt.WriteFile("a.txt", "a\n").Commit("Add a")
	wt.WriteFile("b.txt", "b\n").Commit("Add b")
	// Commits on the Default Branch since aren't the Worktree's.
	repo.WriteFile("main.txt", "main\n").Commit("Work on main")

	m := mergeOf(t, d, "merge-ahead", "feature")
	if m.Merged || !m.Clean || m.UnmergedCount != 2 || len(m.UnmergedCommits) != 2 {
		t.Fatalf("merge status = %+v, want 2 unmerged commits, clean", m)
	}
	for i, subject := range []string{"Add b", "Add a"} { // newest first
		c := m.UnmergedCommits[i]
		hash := wt.Git("rev-parse", "HEAD~"+strconv.Itoa(i))
		if c.Subject != subject || c.Hash != hash || c.Time.Before(before) || c.Time.After(time.Now()) {
			t.Errorf("unmerged commit %d = %+v, want %q %s committed just now", i, c, subject, hash)
		}
	}
}

func TestABranchMergedIntoTheLocalDefaultBranchOnlyIsMerged(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, wt := mergeProject(t, d, "merge-local", "feature")
	unreachableRemote(repo, "main", repo.Git("rev-parse", "HEAD"))
	wt.WriteFile("a.txt", "a\n").Commit("Add a")
	if m := mergeOf(t, d, "merge-local", "feature"); m.Merged {
		t.Fatalf("before merging: %+v, want unmerged", m)
	}

	repo.Git("merge", "--quiet", "--no-ff", "--no-edit", "feature") // not pushed

	if m := mergeOf(t, d, "merge-local", "feature"); !m.Merged || m.UnmergedCount != 0 {
		t.Fatalf("merged into main but not pushed: %+v, want merged", m)
	}
}

func TestABranchMergedIntoTheRemoteDefaultBranchOnlyIsMerged(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, wt := mergeProject(t, d, "merge-remote", "feature")
	unreachableRemote(repo, "main", repo.Git("rev-parse", "HEAD"))
	wt.WriteFile("a.txt", "a\n").Commit("Add a")
	if m := mergeOf(t, d, "merge-remote", "feature"); m.Merged {
		t.Fatalf("before merging: %+v, want unmerged", m)
	}

	// The pull request was merged at the remote, and fetched; the local
	// main is behind.
	repo.Git("update-ref", "refs/remotes/origin/main", wt.Git("rev-parse", "HEAD"))

	if m := mergeOf(t, d, "merge-remote", "feature"); !m.Merged || m.UnmergedCount != 0 {
		t.Fatalf("merged at the remote only: %+v, want merged", m)
	}
}

func TestARebaseMergedBranchIsMerged(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, wt := mergeProject(t, d, "merge-rebase", "feature")
	wt.WriteFile("a.txt", "a\n").Commit("Add a")
	wt.WriteFile("b.txt", "b\n").Commit("Add b")

	// Rebased onto main as new commits with the same patches.
	repo.WriteFile("main.txt", "main\n").Commit("Work on main")
	repo.Git("cherry-pick", "main..feature")
	if repo.Git("rev-parse", "HEAD") == wt.Git("rev-parse", "HEAD") {
		t.Fatal("the rebase made the same commits")
	}

	if m := mergeOf(t, d, "merge-rebase", "feature"); !m.Merged || m.UnmergedCount != 0 {
		t.Fatalf("rebase-merged: %+v, want merged", m)
	}
}

func TestADefaultBranchOtherThanMainIsTheOneTheRemoteNames(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, wt := mergeProject(t, d, "merge-staging", "feature")
	wt.WriteFile("a.txt", "a\n").Commit("Add a")
	// staging has the Worktree's commit; main doesn't.
	unreachableRemote(repo, "staging", wt.Git("rev-parse", "HEAD"))

	if p := projectNamed(t, d, "merge-staging"); p.DefaultBranch != "staging" {
		t.Fatalf("Default Branch = %q, want staging, which origin/HEAD names", p.DefaultBranch)
	}
	if m := mergeOf(t, d, "merge-staging", "feature"); !m.Merged {
		t.Fatalf("merged into origin/staging: %+v, want merged", m)
	}
}

func TestWithoutARemoteTheDefaultBranchIsTheMainWorktreesBranch(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, wt := mergeProject(t, d, "merge-develop", "feature")
	repo.Git("checkout", "--quiet", "-b", "develop")
	wt.WriteFile("a.txt", "a\n").Commit("Add a")

	if p := projectNamed(t, d, "merge-develop"); p.DefaultBranch != "develop" {
		t.Fatalf("Default Branch = %q, want develop, checked out in the main Worktree", p.DefaultBranch)
	}
	if m := mergeOf(t, d, "merge-develop", "feature"); m.Merged || m.UnmergedCount != 1 {
		t.Fatalf("one commit not on develop: %+v", m)
	}
}

func TestAWorktreeReportsItsChangedFiles(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	_, wt := mergeProject(t, d, "merge-changed", "feature")
	wt.WriteFile("tracked.txt", "v1\n").Commit("Add tracked")
	wt.WriteFile(".gitignore", ".env\n").Commit("Ignore .env")
	if m := mergeOf(t, d, "merge-changed", "feature"); !m.Clean {
		t.Fatalf("before changing anything: %+v, want clean", m)
	}

	wt.WriteFile("tracked.txt", "v2\n")    // modified
	wt.WriteFile("staged.txt", "new\n")    // added to the index
	wt.Git("add", "staged.txt")            //
	wt.WriteFile("untracked.txt", "new\n") // untracked
	wt.WriteFile(".env", "SECRET=1\n")     // ignored: not a change

	want := []api.ChangedFile{{Path: "staged.txt", Status: "A "}, {Path: "tracked.txt", Status: " M"}, {Path: "untracked.txt", Status: "??"}}
	var m api.MergeStatus
	eventually(t, "the changed files to be reported", func() bool {
		m = mergeOf(t, d, "merge-changed", "feature")
		return m.ChangedCount == len(want)
	})
	if m.Clean || !slices.Equal(m.ChangedFiles, want) {
		t.Fatalf("merge status = %+v, want unclean with changed files %v", m, want)
	}
}

func TestAWorktreeNotOnABranchReportsItsUnmergedCommits(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	_, wt := mergeProject(t, d, "merge-detached", "feature")
	wt.Git("checkout", "--quiet", "--detach")
	wt.WriteFile("a.txt", "a\n").Commit("Add a")

	m := mergeOf(t, d, "merge-detached", "feature")
	if m.Branch != "" || m.Merged || m.UnmergedCount != 1 || m.UnmergedCommits[0].Subject != "Add a" {
		t.Fatalf("merge status = %+v, want no branch and the commit Add a unmerged", m)
	}
}

func TestCommittingOrChangingAFileUpdatesTheMergeStatusInTheProjectsEvent(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-merge")
	_, wt := mergeProject(t, d, "dash-merge-shop", "feature")
	events := projectEvents(t, d, dash+"/api/v1/events")
	waitForEvent(t, events, "feature merged and clean", featureMerge(func(m *api.MergeStatus) bool {
		return m.Merged && m.Clean
	}))

	wt.WriteFile("a.txt", "a\n").Commit("Add a")
	waitForEvent(t, events, "feature with one unmerged commit", featureMerge(func(m *api.MergeStatus) bool {
		return m.UnmergedCount == 1 && m.Clean
	}))

	wt.WriteFile("a.txt", "changed, not staged\n")
	waitForEvent(t, events, "feature with one changed file", featureMerge(func(m *api.MergeStatus) bool {
		return m.UnmergedCount == 1 && m.ChangedCount == 1 && !m.Clean
	}))
}

func TestStatusShowsEachWorktreesMergeStatus(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, done := mergeProject(t, d, "merge-status", "done")
	ahead := repo.AddWorktree("ahead")
	ahead.WriteFile("a.txt", "a\n").Commit("Add a")
	ahead.WriteFile("b.txt", "b\n").Commit("Add b")
	dirty := repo.AddWorktree("dirty")
	dirty.WriteFile("new.txt", "new\n")
	detached := repo.AddWorktree("detached")
	detached.Git("checkout", "--quiet", "--detach")
	detached.WriteFile("c.txt", "c\n").Commit("Add c")
	detached.WriteFile("d.txt", "d\n")
	for _, w := range []*gitfixture.Worktree{done, ahead, dirty, detached} {
		d.MustRunCLI(w.Dir, "worktree", "ready")
	}

	res := d.MustRunCLI(d.Home, "status")
	lines := strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n")
	if got := strings.Fields(lines[0]); len(got) < 7 || !slices.Equal(got[5:7], []string{"BRANCH", "MERGE"}) {
		t.Fatalf("status header = %q, want BRANCH and MERGE after STATE:\n%s", lines[0], res.Stdout)
	}
	want := map[string][2]string{
		"merge-status": {"main", "default-branch"},
		"done":         {"done", "merged"},
		"ahead":        {"ahead", "unmerged:2"},
		"dirty":        {"dirty", "merged,changed:1"},
		"detached":     {"(detached)", "unmerged:1,changed:1"},
	}
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		w, ok := want[f[0]]
		if !ok {
			t.Fatalf("unexpected status row %q:\n%s", line, res.Stdout)
		}
		if len(f) < 7 || f[5] != w[0] || f[6] != w[1] {
			t.Errorf("status row %q: want branch %s and merge status %s", line, w[0], w[1])
		}
		delete(want, f[0])
	}
	if len(want) > 0 {
		t.Fatalf("status has no rows for %v:\n%s", want, res.Stdout)
	}
}

// featureMerge matches a projects event whose Worktree feature, of the
// Project dash-merge-shop, has a merge status that satisfies cond.
func featureMerge(cond func(*api.MergeStatus) bool) func(api.ProjectList) bool {
	return func(list api.ProjectList) bool {
		w, ok := worktreeIn(list, "feature.dash-merge-shop.localhost")
		return ok && w.Merge != nil && cond(w.Merge)
	}
}
