package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// unlockRequest sends POST /v1/worktree/unlock to the Dashboard at dash
// with the given Origin, and decodes the response or the error.
func unlockRequest(t *testing.T, d *testkit.Daemon, dash, origin string, req api.UnlockRequest) (int, api.UnlockResponse, api.Error) {
	t.Helper()
	status, body := dashboardRequest(t, d, http.MethodPost, dash+"/api/v1/worktree/unlock", origin, req)
	var res api.UnlockResponse
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

func TestTheDashboardUnlocksALockedWorktreeWithoutRemovingIt(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-unlock")
	repo, wt := mergeProject(t, d, "unlock-shop", "feature")
	repo.Git("worktree", "lock", "--reason", "in use by an agent", wt.Dir)
	if m := mergeOf(t, d, "unlock-shop", "feature"); !m.Locked {
		t.Fatalf("merge status = %+v, want locked", m)
	}
	req := api.UnlockRequest{Dir: repo.Dir, Worktree: "feature"}

	for _, origin := range []string{"", "https://unlock-shop.localhost", "https://evil.example"} {
		if status, _, _ := unlockRequest(t, d, dash, origin, req); status != http.StatusForbidden {
			t.Errorf("unlock from Origin %q: status %d, want 403", origin, status)
		}
	}
	if m := mergeOf(t, d, "unlock-shop", "feature"); !m.Locked {
		t.Fatalf("a refused unlock unlocked it: %+v", m)
	}

	status, res, e := unlockRequest(t, d, dash, dash, req)
	if status != http.StatusOK || !res.Unlocked || res.Worktree != wt.Dir {
		t.Fatalf("unlock: status %d, %+v, %+v; want 200, unlocked %s", status, res, e, wt.Dir)
	}
	if m := mergeOf(t, d, "unlock-shop", "feature"); m.Locked {
		t.Errorf("after unlocking, merge status = %+v, want not locked", m)
	}
	if _, err := os.Stat(wt.Dir); err != nil {
		t.Fatalf("unlocking removed the Worktree: %v", err)
	}

	// Unlocking a Worktree that isn't locked changes nothing.
	if status, res, _ := unlockRequest(t, d, dash, dash, req); status != http.StatusOK || res.Unlocked {
		t.Errorf("unlocking again: status %d, %+v; want 200, not unlocked", status, res)
	}
	if status, _, e := unlockRequest(t, d, dash, dash, api.UnlockRequest{Dir: repo.Dir}); status != http.StatusUnprocessableEntity || e.Code != api.CodeMainWorktree {
		t.Errorf("unlocking the main Worktree: status %d, %+v; want 422 %s", status, e, api.CodeMainWorktree)
	}
}

func TestALockedWorktreeIsUnlockedAndRemovedOnlyWhenTheConfirmationSaysSo(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-unlock-remove")
	repo, wt := mergeProject(t, d, "unlock-remove-shop", "feature")
	wt.WriteFile("notes.txt", "draft\n") // a change, so the confirmation is checked
	repo.Git("worktree", "lock", "--reason", "in use by an agent", wt.Dir)
	var preview api.RemoveResponse
	eventually(t, "the preview to list notes.txt", func() bool {
		_, preview, _ = removeRequest(t, d, dash, api.RemoveRequest{Dir: repo.Dir, Worktree: "feature", Preview: true})
		return len(preview.ChangedFiles) == 1
	})
	if !preview.Locked {
		t.Fatalf("preview = %+v, want locked", preview)
	}

	if status, _, e := removeRequest(t, d, dash, confirming(repo.Dir, preview)); status != http.StatusConflict || e.Code != api.CodeWorktreeLocked {
		t.Fatalf("removing without unlock: status %d, %+v; want 409 %s", status, e, api.CodeWorktreeLocked)
	}
	// What the preview showed is checked first: nothing is unlocked when
	// the Worktree changed since.
	wt.WriteFile("more.txt", "more\n")
	stale := confirming(repo.Dir, preview)
	stale.Unlock = true
	if status, _, e := removeRequest(t, d, dash, stale); status != http.StatusConflict || e.Code != api.CodeWorktreeChanged {
		t.Fatalf("unlock and remove after a change: status %d, %+v; want 409 %s", status, e, api.CodeWorktreeChanged)
	}
	if m := mergeOf(t, d, "unlock-remove-shop", "feature"); !m.Locked {
		t.Fatalf("a refused removal unlocked it: %+v", m)
	}

	_, preview, _ = removeRequest(t, d, dash, api.RemoveRequest{Dir: repo.Dir, Worktree: "feature", Preview: true})
	req := confirming(repo.Dir, preview)
	req.Unlock = true
	status, res, e := removeRequest(t, d, dash, req)
	if status != http.StatusOK || !res.Removed {
		t.Fatalf("unlock and remove: status %d, %+v, %+v; want removed", status, res, e)
	}
	assertGone(t, wt.Dir)
	waitForNoWorktree(t, d, "unlock-remove-shop", "feature")
}

func TestLaneWorktreeRemoveUnlocksALockedWorktreeOnlyWithUnlock(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, wt := mergeProject(t, d, "unlock-cli", "feature")
	repo.Git("worktree", "lock", "--reason", "an agent is working here", wt.Dir)

	res := d.RunCLI(repo.Dir, "worktree", "remove", "feature", "--force")
	if res.ExitCode == 0 {
		t.Fatalf("removing a locked Worktree without --unlock exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `Worktree "feature" is locked: an agent is working here`)
	assertContains(t, res.Stderr, "lane worktree remove --unlock")
	if _, err := os.Stat(wt.Dir); err != nil {
		t.Fatalf("the locked Worktree went: %v", err)
	}

	res = d.RunCLI(repo.Dir, "worktree", "remove", "feature", "--unlock", "--force")
	if res.ExitCode != 0 {
		t.Fatalf("lane worktree remove --unlock exited %d:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stdout, "locked: an agent is working here")
	assertContains(t, res.Stdout, `Removed Worktree "feature"`)
	assertGone(t, wt.Dir)
	waitForNoWorktree(t, d, "unlock-cli", "feature")
}
