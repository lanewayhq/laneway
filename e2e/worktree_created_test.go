package e2e

import (
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/testkit"
)

func TestEachLinkedWorktreeSaysWhenItWasCreated(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	// File times can be a little coarse: allow a second either side.
	before := time.Now().Add(-time.Second)
	_, wt := mergeProject(t, d, "created-shop", "feature")
	after := time.Now().Add(time.Second)

	w, _ := findWorktree(t, d, "created-shop", "feature")
	if w.Created.Before(before) || w.Created.After(after) {
		t.Fatalf("created = %v, want between %v and %v", w.Created, before, after)
	}
	created := w.Created

	// Working in it later doesn't move it.
	time.Sleep(1100 * time.Millisecond)
	wt.WriteFile("a.txt", "a\n").Commit("Add a")
	wt.Git("checkout", "--quiet", "-b", "other")
	if w, _ := findWorktree(t, d, "created-shop", "feature"); !w.Created.Equal(created) {
		t.Errorf("after committing and switching branch, created = %v, want %v still", w.Created, created)
	}

	// The main Worktree has none: it is the clone itself.
	if p := projectNamed(t, d, "created-shop"); !p.Worktrees[0].Main || !p.Worktrees[0].Created.IsZero() {
		t.Errorf("main Worktree = %+v, want no created time", p.Worktrees[0])
	}
}
