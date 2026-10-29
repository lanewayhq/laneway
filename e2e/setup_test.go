package e2e

import (
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// tomlList is a TOML array of strings, for worktree.on_create and on_remove.
func tomlList(items ...string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func TestSetupAppliesTheEnvPatchThenRunsOnCreateStepsInTheWorktree(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	out := filepath.Join(gitfixture.TempDir(t), "ran")
	repo := gitfixture.New(t, "setup-steps").
		WriteFile(".gitignore", ".env\n").
		WriteFile("portby.toml", "[worktree]\non_create = "+tomlList(
			"pwd >> "+out,
			"grep APP_URL .env >> "+out,
		)+"\n[env]\nAPP_URL = \"{url}\"\n").
		WriteFile("portby.local.toml", "[worktree]\non_create = "+tomlList("echo local-step >> "+out)+"\n").
		Commit("declare setup steps")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	res := d.MustRunCLI(repo.Dir, "worktree", "ready")

	assertContains(t, res.Stdout, "https://setup-steps.localhost/")
	assertFile(t, out, repo.Dir+"\nAPP_URL=https://setup-steps.localhost\nlocal-step\n")
}

func TestAFailedStepIsRecordedWithItsOutputAndSetupCanBeRetried(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	// Fails until the marker exists, like a step that needs a fix.
	marker := filepath.Join(gitfixture.TempDir(t), "fixed")
	out := filepath.Join(gitfixture.TempDir(t), "ran")
	failing := "echo checking-deps; test -e " + marker + " || { echo missing-dep >&2; exit 3; }"
	repo := gitfixture.New(t, "setup-fail").
		WriteFile("portby.toml", "[worktree]\non_create = "+tomlList(failing, "echo after >> "+out)+"\n").
		Commit("declare setup steps")
	d.MustRunCLI(repo.Dir, "link")

	res := d.RunCLI(repo.Dir, "worktree", "ready")

	if res.ExitCode != 1 {
		t.Fatalf("worktree ready exited %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, "on_create: "+failing)
	assertContains(t, res.Stderr, "exit status 3")
	assertContains(t, res.Stderr, "checking-deps")
	assertContains(t, res.Stderr, "missing-dep")
	assertContains(t, res.Stderr, "--retry")
	assertNoFile(t, out)
	assertSetupState(t, d, repo.Dir, "failed")

	// Without --retry, the failure stands.
	if res := d.RunCLI(repo.Dir, "worktree", "ready"); res.ExitCode != 1 {
		t.Fatalf("worktree ready again exited %d, want 1:\n%s", res.ExitCode, res.Output())
	}

	writeTestFile(t, marker, "")
	d.MustRunCLI(repo.Dir, "worktree", "ready", "--retry")

	assertSetupState(t, d, repo.Dir, "ready")
	assertFile(t, out, "after\n")
}

// assertSetupState checks the setup state `portby status` shows for the
// Worktree at dir.
func assertSetupState(t *testing.T, d *testkit.Daemon, dir, want string) {
	t.Helper()
	status := d.MustRunCLI(d.Home, "status").Stdout
	for line := range strings.Lines(status) {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[2] == dir {
			if fields[3] != want {
				t.Fatalf("setup state of %s = %q, want %q:\n%s", dir, fields[3], want, status)
			}
			return
		}
	}
	t.Fatalf("no status row for %s:\n%s", dir, status)
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAWorktreeIsNotStartedUntilItIsReadyAndBrowsersAreToldItIsBeingSetUp(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	gate := filepath.Join(gitfixture.TempDir(t), "gate")
	command, log := startCounting(t, testkit.TestServer(t))
	repo := gitfixture.New(t, "setup-page").
		WriteFile("portby.toml", "[worktree]\non_create = "+tomlList("until [ -e "+gate+" ]; do sleep 0.05; done")+"\n"+processConfig(command)).
		Commit("declare a setup step that waits for the gate")
	d.MustRunCLI(repo.Dir, "link")

	page := d.Navigate("https://setup-page.localhost/")

	if page.StatusCode != http.StatusServiceUnavailable || page.Header.Get("Refresh") == "" {
		t.Fatalf("navigation while preparing: status %d (Refresh %q), want the 503 Preparing page:\n%s",
			page.StatusCode, page.Header.Get("Refresh"), page.Body)
	}
	assertContains(t, page.Body, "being prepared")
	assertSetupState(t, d, repo.Dir, "preparing")
	if n := starts(t, log); n != 0 {
		t.Fatalf("Process started %d times while the Worktree was Preparing, want 0", n)
	}

	writeTestFile(t, gate, "")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	if page := navigateUntilPrepared(t, d, "https://setup-page.localhost/"); page.StatusCode != http.StatusOK {
		t.Fatalf("once Ready: status %d, want the app's 200:\n%s", page.StatusCode, page.Body)
	}
}

func TestAFailedWorktreeShowsItsSetupErrorInsteadOfStarting(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	marker := filepath.Join(gitfixture.TempDir(t), "fixed")
	failing := "test -e " + marker + " || { echo broken-setup-output; exit 5; }"
	command, log := startCounting(t, testkit.TestServer(t))
	repo := gitfixture.New(t, "setup-error").
		WriteFile("portby.toml", "[worktree]\non_create = "+tomlList(failing)+"\n"+processConfig(command)).
		Commit("declare a failing setup step")
	d.MustRunCLI(repo.Dir, "link")

	page := navigateUntilPrepared(t, d, "https://setup-error.localhost/")

	if page.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d, want the 502 setup error page:\n%s", page.StatusCode, page.Body)
	}
	assertContains(t, page.Body, "Preparation failed")
	assertContains(t, page.Body, html.EscapeString("on_create: "+failing))
	assertContains(t, page.Body, "broken-setup-output")
	assertContains(t, page.Body, "portby worktree ready --retry")
	// Requests that aren't navigations get it too, without being held.
	if res := d.Get("https://setup-error.localhost/api"); res.StatusCode != http.StatusBadGateway {
		t.Fatalf("held request: status %d, want 502:\n%s", res.StatusCode, res.Body)
	}
	if n := starts(t, log); n != 0 {
		t.Fatalf("Process started %d times in a Failed Worktree, want 0", n)
	}

	writeTestFile(t, marker, "")
	d.MustRunCLI(repo.Dir, "worktree", "ready", "--retry")

	d.GetServer("https://setup-error.localhost/")
}

func TestWorktreeReadyInANewWorktreeDiscoversItAndWaitsForItsSetup(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	out := filepath.Join(gitfixture.TempDir(t), "ran")
	repo := gitfixture.New(t, "setup-new").
		WriteFile("portby.toml", "[worktree]\non_create = "+tomlList(`echo "$(pwd) $PORTBY_WORKTREE $PORTBY_URL" >> `+out)+"\n").
		Commit("declare a setup step")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	// Straight after `git worktree add`, before the Watcher can report it.
	wt := repo.AddWorktree("feature-q")
	res := d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertContains(t, res.Stdout, `Worktree "feature-q"`)
	assertContains(t, res.Stdout, "https://feature-q.setup-new.localhost/")
	assertFile(t, out, repo.Dir+"  https://setup-new.localhost\n"+
		wt.Dir+" feature-q https://feature-q.setup-new.localhost\n")
	assertSetupState(t, d, wt.Dir, "ready")
}

func TestSetupRunsOnceAndAReadyWorktreeStaysReadyAcrossDaemonRestarts(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	out := filepath.Join(gitfixture.TempDir(t), "ran")
	repo := gitfixture.New(t, "setup-once").
		WriteFile("portby.toml", "[worktree]\non_create = "+tomlList(`echo "$(basename "$(pwd)")" >> `+out)+"\n").
		Commit("declare a setup step")
	wt := repo.AddWorktree("kept")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	d.Restart()

	assertSetupState(t, d, repo.Dir, "ready")
	assertSetupState(t, d, wt.Dir, "ready")
	d.MustRunCLI(repo.Dir, "worktree", "ready", "--retry") // nothing to retry
	// Once each; the two Worktrees are prepared at the same time.
	ran := strings.Fields(readFile(t, out))
	slices.Sort(ran)
	if want := []string{"kept", "setup-once"}; !slices.Equal(ran, want) {
		t.Fatalf("on_create ran in %q, want once in each of %q", ran, want)
	}
}

func TestOnRemoveStepsRunInTheMainWorktreeWhenAWorktreeIsRemoved(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	out := filepath.Join(gitfixture.TempDir(t), "removed")
	repo := gitfixture.New(t, "setup-remove").
		WriteFile("portby.toml", "[worktree]\non_remove = "+tomlList(
			`echo "$(pwd) $PORTBY_WORKTREE $PORTBY_WORKTREE_PATH" >> `+out,
		)+"\n").
		Commit("declare an on_remove step")
	d.MustRunCLI(repo.Dir, "link")
	wt := repo.AddWorktree("doomed")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	assertNoFile(t, out)

	repo.Git("worktree", "remove", wt.Dir)

	waitForFile(t, out, repo.Dir+" doomed "+wt.Dir+"\n")
}

func TestRemovingAWorktreeStopsItsSetup(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	pids := gitfixture.TempDir(t)
	repo := gitfixture.New(t, "setup-stop").
		WriteFile("portby.toml", "[worktree]\non_create = "+tomlList(`echo $$ > `+pids+`/"$(basename "$(pwd)")"; exec sleep 600`)+"\n").
		Commit("declare a setup step that never ends")
	d.MustRunCLI(repo.Dir, "link")
	wt := repo.AddWorktree("doomed")
	waitForWorktree(t, d, "setup-stop", "doomed")
	pid := waitForPID(t, filepath.Join(pids, "doomed"))

	repo.Git("worktree", "remove", "--force", wt.Dir)

	eventually(t, "the removed Worktree's setup step to stop", func() bool {
		return syscall.Kill(pid, 0) != nil
	})
}

func TestASetupInterruptedByTheDaemonStoppingRunsAgainWhenItStarts(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	dir := gitfixture.TempDir(t)
	gate, out := filepath.Join(dir, "gate"), filepath.Join(dir, "ran")
	repo := gitfixture.New(t, "setup-resume").
		WriteFile("portby.toml", "[worktree]\non_create = "+tomlList(
			"echo begun >> "+out+"; until [ -e "+gate+" ]; do sleep 0.05; done",
		)+"\n").
		Commit("declare a setup step that waits for the gate")
	d.MustRunCLI(repo.Dir, "link")
	waitForFile(t, out, "begun\n")

	d.Stop()
	d.Start()
	writeTestFile(t, gate, "")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	assertFile(t, out, "begun\nbegun\n")
}

func TestWorktreeReadyExitsWith3WhenThereIsNothingToWaitFor(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	unlinked := gitfixture.New(t, "setup-unlinked")
	notARepo := gitfixture.NotARepo(t, "setup-plain")

	for _, dir := range []string{unlinked.Dir, notARepo} {
		if res := d.RunCLI(dir, "worktree", "ready"); res.ExitCode != 3 {
			t.Errorf("worktree ready in %s exited %d, want 3:\n%s", dir, res.ExitCode, res.Output())
		}
	}
	d.Stop()
	if res := d.RunCLI(unlinked.Dir, "worktree", "ready"); res.ExitCode != 3 {
		t.Errorf("worktree ready without a daemon exited %d, want 3:\n%s", res.ExitCode, res.Output())
	}
}

func TestWorktreeReadyGivesUpWithExit4AfterItsTimeout(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "setup-timeout").
		WriteFile("portby.toml", "[worktree]\non_create = "+tomlList("exec sleep 600")+"\n").
		Commit("declare a setup step that never ends")
	d.MustRunCLI(repo.Dir, "link")

	res := d.RunCLI(repo.Dir, "worktree", "ready", "--timeout", "300ms")

	if res.ExitCode != 4 {
		t.Fatalf("worktree ready exited %d, want 4:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, "300ms")
	assertSetupState(t, d, repo.Dir, "preparing")
}

func TestWorktreeAddCreatesTheWorktreeNextToTheMainOneAndDiscoveryTakesOver(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "setup-add")
	d.MustRunCLI(repo.Dir, "link")

	res := d.MustRunCLI(repo.Dir, "worktree", "add", "feature-a")

	dir := filepath.Join(filepath.Dir(repo.Dir), "setup-add.worktrees", "feature-a")
	assertContains(t, res.Stdout, dir)
	if branch := strings.TrimSpace(gitfixture.Git(t, dir, "branch", "--show-current")); branch != "feature-a" {
		t.Fatalf("new worktree is on branch %q, want a new branch feature-a", branch)
	}
	if got := waitForWorktree(t, d, "setup-add", "feature-a"); got.Path != dir {
		t.Fatalf("Worktree feature-a at %q, want %q", got.Path, dir)
	}
	d.MustRunCLI(dir, "worktree", "ready")
}

func TestWorktreeAddUsesTheConfiguredRootAndTheGivenBranch(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "setup-add-root").
		WriteFile("portby.local.toml", "[worktree]\nroot = \"../trees\"\n")
	repo.Git("branch", "existing")
	other := repo.AddWorktree("other")
	root := filepath.Join(filepath.Dir(repo.Dir), "trees")

	// From another Worktree, the root is still relative to the main one.
	d.MustRunCLI(other.Dir, "worktree", "add", "bugfix", "--branch", "existing")
	d.MustRunCLI(repo.Dir, "worktree", "add", "--branch", "brand-new", "spike")

	for dir, want := range map[string]string{"bugfix": "existing", "spike": "brand-new"} {
		branch := strings.TrimSpace(gitfixture.Git(t, filepath.Join(root, dir), "branch", "--show-current"))
		if branch != want {
			t.Errorf("worktree %s is on branch %q, want %q", dir, branch, want)
		}
	}
	res := d.RunCLI(repo.Dir, "worktree", "add", "bugfix")
	if res.ExitCode != 1 {
		t.Fatalf("adding bugfix again exited %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, filepath.Join(root, "bugfix"))
	if res := d.RunCLI(repo.Dir, "worktree", "add", "../escape"); res.ExitCode != 2 {
		t.Fatalf("a name with a slash: exit %d, want the usage error 2:\n%s", res.ExitCode, res.Output())
	}
}

func TestSetupStepsRunWithTheRuntimesMiseResolvesForTheWorktree(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	testkit.StubPHP(t, d.Bin, "8.1.0") // the system PHP
	miseBin := filepath.Join(d.Home, "mise-installs", "php", "bin")
	testkit.StubPHP(t, miseBin, "8.4.2")
	testkit.StubMise(t, d.Bin, map[string]string{"PATH": miseBin + ":" + d.Path})
	out := filepath.Join(gitfixture.TempDir(t), "ran")
	repo := gitfixture.New(t, "setup-runtime").
		WriteFile("portby.toml", "[worktree]\non_create = "+tomlList("php --version >> "+out)+"\n").
		Commit("declare a setup step using PHP")
	d.MustRunCLI(repo.Dir, "link")

	d.MustRunCLI(repo.Dir, "worktree", "ready")

	assertFile(t, out, "PHP 8.4.2 (stub) --version\n")
}
