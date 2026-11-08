package e2e

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// discoveryTimeout is how long a Worktree may take to appear or disappear:
// the ticket asks for "within a few seconds".
const discoveryTimeout = 5 * time.Second

// worktreeProject is a repo whose Primary Process reports the basename of its
// working directory as its name, so a response shows which Worktree served it.
func worktreeProject(t *testing.T, name string) *gitfixture.Repo {
	t.Helper()
	server := testkit.TestServer(t)
	return gitfixture.New(t, name).
		WriteFile("laneway.toml", processConfig(server+` --name "$(basename "$(pwd)")"`)).
		Commit("declare web Process")
}

func TestWorktreeAddedAfterLinkIsServedAtItsDomain(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := worktreeProject(t, "wt-added")
	d.MustRunCLI(repo.Dir, "link")

	wt := repo.AddWorktree("feature-x")

	got := waitForWorktree(t, d, "wt-added", "feature-x")
	if got.Path != wt.Dir || got.Domain != "feature-x.wt-added.localhost" || got.Main {
		t.Fatalf("Worktree = %+v, want feature-x at %s served at feature-x.wt-added.localhost", got, wt.Dir)
	}
	res := d.GetServer("https://feature-x.wt-added.localhost/")
	if res.Name != "feature-x" {
		t.Fatalf("feature-x.wt-added.localhost was served by the Process in %q, want the feature-x Worktree's", res.Name)
	}
	// The main Worktree keeps its Domain and its own Process on its own port.
	main := d.GetServer("https://wt-added.localhost/")
	if main.Name != "wt-added" || main.ListenPort == res.ListenPort {
		t.Fatalf("main Worktree served by %q on port %s; feature-x on port %s", main.Name, main.ListenPort, res.ListenPort)
	}
}

func TestWorktreeAddedWithRelativePathsIsFound(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := worktreeProject(t, "wt-relative")
	d.MustRunCLI(repo.Dir, "link")

	// git records this worktree's location relative to the repository.
	dir := filepath.Join(gitfixture.TempDir(t), "rel")
	repo.Git("worktree", "add", "--quiet", "--relative-paths", "-b", "rel", dir)

	if got := waitForWorktree(t, d, "wt-relative", "rel"); got.Path != dir {
		t.Fatalf("Worktree rel at %q, want %q", got.Path, dir)
	}
	if res := d.GetServer("https://rel.wt-relative.localhost/"); res.Name != "rel" {
		t.Fatalf("rel.wt-relative.localhost was served by the Process in %q", res.Name)
	}
}

func TestExistingWorktreesAreFoundWhenLinking(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := worktreeProject(t, "wt-existing")
	wt := repo.AddWorktree("bugfix")

	// Linking from inside the Worktree registers the whole repository and
	// already knows the Worktree's Domain: no waiting for the Watcher.
	res := d.MustRunCLI(wt.Dir, "link")
	assertContains(t, res.Stdout, `Linked Project "wt-existing"`)

	got, ok := findWorktree(t, d, "wt-existing", "bugfix")
	if !ok || got.Domain != "bugfix.wt-existing.localhost" {
		t.Fatalf("right after link: Worktree bugfix = %+v (found: %v)", got, ok)
	}
	if res := d.GetServer("https://bugfix.wt-existing.localhost/"); res.Name != "bugfix" {
		t.Fatalf("bugfix.wt-existing.localhost was served by the Process in %q", res.Name)
	}
}

func TestWorktreesChangedWhileDaemonWasDownAreFoundWhenItStarts(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := worktreeProject(t, "wt-restart")
	repo.AddWorktree("kept")
	gone := repo.AddWorktree("gone")
	d.MustRunCLI(repo.Dir, "link")
	d.GetServer("https://kept.wt-restart.localhost/")

	d.Stop()
	repo.Git("worktree", "remove", gone.Dir)
	repo.AddWorktree("added")
	d.Start()

	// Found during start, so no waiting.
	for _, name := range []string{"kept", "added"} {
		if _, ok := findWorktree(t, d, "wt-restart", name); !ok {
			t.Fatalf("Worktree %s not known right after the daemon started", name)
		}
		if res := d.GetServer("https://" + name + ".wt-restart.localhost/"); res.Name != name {
			t.Fatalf("%s.wt-restart.localhost was served by the Process in %q", name, res.Name)
		}
	}
	if _, ok := findWorktree(t, d, "wt-restart", "gone"); ok {
		t.Fatal("Worktree removed while the daemon was down is still known")
	}
	if res := d.Get("https://gone.wt-restart.localhost/"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("removed Worktree's Domain: status %d, want 404", res.StatusCode)
	}
}

func TestRemovedWorktreeDisappearsAndItsProcessesStop(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		how    string
		remove func(repo *gitfixture.Repo, wt *gitfixture.Worktree)
	}{
		{"git worktree remove", func(repo *gitfixture.Repo, wt *gitfixture.Worktree) {
			repo.Git("worktree", "remove", wt.Dir)
		}},
		{"deleting its directory", func(repo *gitfixture.Repo, wt *gitfixture.Worktree) {
			if err := os.RemoveAll(wt.Dir); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.how, func(t *testing.T) {
			t.Parallel()
			d := testkit.StartDaemon(t)
			project := "wt-remove-" + strings.ReplaceAll(tc.how, " ", "-")
			repo := worktreeProject(t, project)
			d.MustRunCLI(repo.Dir, "link")
			wt := repo.AddWorktree("doomed")
			waitForWorktree(t, d, project, "doomed")
			domain := "doomed." + project + ".localhost"
			port := d.GetServer("https://" + domain + "/").ListenPort

			tc.remove(repo, wt)

			waitForNoWorktree(t, d, project, "doomed")
			eventually(t, "the removed Worktree's Process to stop", func() bool {
				conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", port))
				if err == nil {
					conn.Close()
				}
				return err != nil
			})
			if res := d.Get("https://" + domain + "/"); res.StatusCode != http.StatusNotFound {
				t.Fatalf("removed Worktree's Domain: status %d, want 404", res.StatusCode)
			}
			// The main Worktree is unaffected.
			d.GetServer("https://" + project + ".localhost/")
		})
	}
}

func TestWorktreeAndProjectNamesAreMadeHostnameSafe(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := worktreeProject(t, "Wt_Naming")
	d.MustRunCLI(repo.Dir, "link")
	repo.AddWorktree("Feature_X.y")
	long := strings.Repeat("a", 70)
	repo.AddWorktree(long)

	if res := d.GetServer("https://wt-naming.localhost/"); res.Name != "Wt_Naming" {
		t.Fatalf("wt-naming.localhost was served by the Process in %q", res.Name)
	}
	if got := waitForWorktree(t, d, "Wt_Naming", "Feature_X.y"); got.Domain != "feature-x-y.wt-naming.localhost" {
		t.Fatalf("Domain of Worktree Feature_X.y = %q, want feature-x-y.wt-naming.localhost", got.Domain)
	}
	if res := d.GetServer("https://feature-x-y.wt-naming.localhost/"); res.Name != "Feature_X.y" {
		t.Fatalf("feature-x-y.wt-naming.localhost was served by the Process in %q", res.Name)
	}

	got := waitForWorktree(t, d, "Wt_Naming", long)
	label := strings.TrimSuffix(got.Domain, ".wt-naming.localhost")
	if !regexp.MustCompile(`^a{54}-[0-9a-f]{8}$`).MatchString(label) {
		t.Fatalf("Domain of a 70-character Worktree name = %q, want 54 a's, '-' and an 8-digit hash (63 characters)", got.Domain)
	}
	if res := d.GetServer("https://" + got.Domain + "/"); res.Name != long {
		t.Fatalf("%s was served by the Process in %q", got.Domain, res.Name)
	}
}

func TestClashingWorktreeNamesGetAHashSuffix(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := worktreeProject(t, "wt-clash")
	d.MustRunCLI(repo.Dir, "link")
	repo.AddWorktree("feat_x")
	first := waitForWorktree(t, d, "wt-clash", "feat_x")
	repo.AddWorktree("feat-x")
	second := waitForWorktree(t, d, "wt-clash", "feat-x")

	// The first one found keeps the plain name.
	if first.Domain != "feat-x.wt-clash.localhost" {
		t.Fatalf("Domain of feat_x = %q, want feat-x.wt-clash.localhost", first.Domain)
	}
	if !regexp.MustCompile(`^feat-x-[0-9a-f]{8}\.wt-clash\.localhost$`).MatchString(second.Domain) {
		t.Fatalf("Domain of feat-x = %q, want feat-x-<hash>.wt-clash.localhost", second.Domain)
	}
	for name, domain := range map[string]string{"feat_x": first.Domain, "feat-x": second.Domain} {
		if res := d.GetServer("https://" + domain + "/"); res.Name != name {
			t.Fatalf("%s was served by the Process in %q, want %s's", domain, res.Name, name)
		}
	}

	// Domains don't change when the daemon restarts.
	d.Restart()
	for name, domain := range map[string]string{"feat_x": first.Domain, "feat-x": second.Domain} {
		if got, _ := findWorktree(t, d, "wt-clash", name); got.Domain != domain {
			t.Fatalf("after restart, Domain of %s = %q, want %q", name, got.Domain, domain)
		}
	}
}

func TestEachWorktreeHasItsOwnProcessStatesAndLogs(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "wt-logs").
		WriteFile("laneway.toml", processConfig(`echo "boot $(basename "$(pwd)")"; exec sleep 600`)).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")
	wt := repo.AddWorktree("feature-y")
	waitForWorktree(t, d, "wt-logs", "feature-y")
	startByBrowsing(t, d, "feature-y.wt-logs.localhost")
	startByBrowsing(t, d, "wt-logs.localhost")

	assertLines(t, waitForLogs(t, d, wt.Dir, []string{"logs", "web"}, "boot"), "boot feature-y")
	assertLines(t, waitForLogs(t, d, repo.Dir, []string{"logs", "web"}, "boot"), "boot wt-logs")
	waitForFile(t, filepath.Join(d.Paths.StateDir, "logs", "feature-y.wt-logs.localhost", "web.log"), "boot feature-y\n")
	waitForFile(t, filepath.Join(d.Paths.StateDir, "logs", "wt-logs.localhost", "web.log"), "boot wt-logs\n")

	res := d.MustRunCLI(d.Home, "status")
	lines := strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n")
	want := [][]string{
		{"PROJECT", "DOMAIN", "PATH", "READINESS", "STATE", "PROCESSES"},
		// web never accepts connections, so the Worktrees are still starting.
		{"wt-logs", "wt-logs.localhost", repo.Dir, "ready", "starting", "web:running"},
		{"feature-y", "feature-y.wt-logs.localhost", wt.Dir, "ready", "starting", "web:running"},
	}
	if len(lines) != len(want) {
		t.Fatalf("status has %d lines, want %d:\n%s", len(lines), len(want), res.Stdout)
	}
	for i, fields := range want {
		if got := strings.Fields(lines[i]); !slices.Equal(got, fields) {
			t.Fatalf("status line %d = %q, want %v\n%s", i, lines[i], fields, res.Stdout)
		}
	}
}

// findWorktree returns the Worktree named name of the Project named project,
// as the API reports it.
func findWorktree(t *testing.T, d *testkit.Daemon, project, name string) (api.Worktree, bool) {
	t.Helper()
	projects, err := d.Client().Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		if p.Name != project {
			continue
		}
		for _, w := range p.Worktrees {
			if !w.Main && w.Name == name {
				return w, true
			}
		}
	}
	return api.Worktree{}, false
}

// waitForWorktree waits until the API reports the Worktree, failing the test
// after discoveryTimeout.
func waitForWorktree(t *testing.T, d *testkit.Daemon, project, name string) api.Worktree {
	t.Helper()
	var w api.Worktree
	eventually(t, "Worktree "+name+" to appear", func() bool {
		var ok bool
		w, ok = findWorktree(t, d, project, name)
		return ok
	})
	return w
}

// waitForNoWorktree waits until the API no longer reports the Worktree.
func waitForNoWorktree(t *testing.T, d *testkit.Daemon, project, name string) {
	t.Helper()
	eventually(t, "Worktree "+name+" to disappear", func() bool {
		_, ok := findWorktree(t, d, project, name)
		return !ok
	})
}

// eventually polls cond until it holds, failing the test after
// discoveryTimeout.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	testkit.WaitUntil(t, discoveryTimeout, what, cond)
}
