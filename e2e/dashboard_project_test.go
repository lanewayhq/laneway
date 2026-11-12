package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// What the Dashboard's Project page reads besides the projects event: the
// Project's Recipe (GET /v1/info, once per Project) and each Worktree's
// recent output (GET /v1/logs without follow, whose last lines an expanded
// row shows).

func TestTheDashboardReadsAProjectsRecipe(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-recipe")
	repo := laravelRepo(t, "dash-recipe-shop")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	res := d.Get(dash + "/api/v1/info?" + url.Values{"dir": {repo.Dir}}.Encode())

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/info: status %d:\n%s", res.StatusCode, res.Body)
	}
	var info struct {
		Recipe string `json:"recipe"`
	}
	if err := json.Unmarshal([]byte(res.Body), &info); err != nil {
		t.Fatalf("GET /api/v1/info: %v\n%s", err, res.Body)
	}
	if info.Recipe != "laravel" {
		t.Errorf("recipe %q, want laravel", info.Recipe)
	}
}

func TestTheDashboardReadsAWorktreesRecentOutputWithoutFollowingIt(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-recent")
	repo := logsProject(t, "dash-recent-shop")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "up")
	waitForLogLines(t, logStream(t, d, logsURL(dash, repo.Dir, "", true)), "web | web started", "worker | worker ready")

	// Without follow, the kept output of every Process comes back and the
	// response ends, though the Processes still run.
	lines := logStream(t, d, logsURL(dash, repo.Dir, "", false))

	var got []string
	timeout := time.After(discoveryTimeout)
	for done := false; !done; {
		select {
		case l, ok := <-lines:
			if !ok {
				done = true
				break
			}
			got = append(got, l.Process+" | "+l.Text)
		case <-timeout:
			t.Fatalf("the output didn't end within %s; got so far:\n%s", discoveryTimeout, strings.Join(got, "\n"))
		}
	}
	slices.Sort(got)
	if want := []string{"web | web started", "worker | worker ready"}; !slices.Equal(got, want) {
		t.Fatalf("recent output:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
