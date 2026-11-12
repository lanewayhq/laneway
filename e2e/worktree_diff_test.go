package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// diffOf asks the Dashboard for the diff of one changed file (path) or one
// commit (commit) of the Worktree at dir, and returns the status and, when
// it is 200, the response.
func diffOf(t *testing.T, d *testkit.Daemon, dash, dir string, q url.Values) (int, api.DiffResponse, string) {
	t.Helper()
	q.Set("dir", dir)
	status, body := dashboardRequest(t, d, http.MethodGet, dash+"/api/v1/worktree/diff?"+q.Encode(), "", nil)
	var res api.DiffResponse
	if status == http.StatusOK {
		if err := json.Unmarshal([]byte(body), &res); err != nil {
			t.Fatalf("GET /v1/worktree/diff: %v\n%s", err, body)
		}
	}
	return status, res, body
}

func TestTheDashboardShowsTheDiffOfEachChangedFile(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-diff-files")
	_, wt := mergeProject(t, d, "diff-files-shop", "feature")
	wt.WriteFile("tracked.txt", "one\ntwo\n").Commit("Add tracked")
	wt.WriteFile("gone.txt", "bye\n").Commit("Add gone")
	wt.WriteFile(".gitignore", ".env\n").Commit("Ignore .env")

	wt.WriteFile("tracked.txt", "one\n2\n") // modified
	wt.WriteFile("staged.txt", "staged line\n")
	wt.Git("add", "staged.txt") // added to the index
	wt.Git("rm", "--quiet", "gone.txt")
	wt.WriteFile("untracked.txt", "untracked line\n")
	wt.WriteFile("notes/a.md", "note a\n") // an untracked directory
	wt.WriteFile("notes/b.md", "note b\n")
	wt.WriteFile(".env", "SECRET=1\n") // ignored: not a change

	for path, want := range map[string][]string{
		"tracked.txt":   {"-two", "+2"},
		"staged.txt":    {"+staged line"},
		"gone.txt":      {"-bye"},
		"untracked.txt": {"+untracked line"},
		"notes/":        {"notes/a.md", "+note a", "notes/b.md", "+note b"},
	} {
		status, res, body := diffOf(t, d, dash, wt.Dir, url.Values{"path": {path}})
		if status != http.StatusOK {
			t.Errorf("diff of %s: status %d:\n%s", path, status, body)
			continue
		}
		for _, w := range want {
			if !strings.Contains(res.Diff, w) {
				t.Errorf("diff of %s doesn't contain %q:\n%s", path, w, res.Diff)
			}
		}
		if res.Path != path || res.Truncated {
			t.Errorf("diff of %s: path %q, truncated %v", path, res.Path, res.Truncated)
		}
	}

	// A diff past api.DiffLimit stops at the end of a line, and says so.
	wt.WriteFile("big.log", strings.Repeat("a line of a generated file that is far too long to read\n", 30000))
	status, res, body := diffOf(t, d, dash, wt.Dir, url.Values{"path": {"big.log"}})
	if status != http.StatusOK || !res.Truncated || len(res.Diff) > api.DiffLimit || !strings.HasSuffix(res.Diff, "far too long to read\n") {
		t.Errorf("diff of big.log: status %d, truncated %v, %d bytes, want 200, truncated at a line's end within %d bytes:\n%.300s",
			status, res.Truncated, len(res.Diff), api.DiffLimit, body)
	}

	// Only the Worktree's changed files: not an ignored file, a file outside
	// it, or one with no changes.
	for _, path := range []string{".env", "../../../../etc/passwd", "/etc/passwd", "README.md"} {
		status, _, body := diffOf(t, d, dash, wt.Dir, url.Values{"path": {path}})
		if status != http.StatusBadRequest || strings.Contains(body, "SECRET") || strings.Contains(body, "root:") {
			t.Errorf("diff of %s: status %d, want 400 without its content:\n%s", path, status, body)
		}
	}
}

func TestTheDashboardShowsACommitsPatch(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-diff-commit")
	_, wt := mergeProject(t, d, "diff-commit-shop", "feature")
	wt.WriteFile("a.txt", "first line of a\n").Commit("Add a with its first line")
	hash := wt.Git("rev-parse", "HEAD")

	status, res, body := diffOf(t, d, dash, wt.Dir, url.Values{"commit": {hash}})
	if status != http.StatusOK {
		t.Fatalf("patch of %s: status %d:\n%s", hash, status, body)
	}
	for _, w := range []string{hash, "Add a with its first line", "a.txt", "+first line of a"} {
		if !strings.Contains(res.Diff, w) {
			t.Errorf("patch doesn't contain %q:\n%s", w, res.Diff)
		}
	}
	if res.Commit != hash {
		t.Errorf("commit %q, want %s", res.Commit, hash)
	}

	for _, bad := range []url.Values{
		{"commit": {"--output=/tmp/x"}},
		{"commit": {"0123456789abcdef0123456789abcdef01234567"}}, // no such commit
		{"commit": {hash}, "path": {"a.txt"}},                    // not both
		{},                                                       // neither
	} {
		if status, _, body := diffOf(t, d, dash, wt.Dir, bad); status != http.StatusBadRequest {
			t.Errorf("diff with %v: status %d, want 400:\n%s", bad, status, body)
		}
	}
}
