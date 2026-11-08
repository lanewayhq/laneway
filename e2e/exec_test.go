package e2e

import (
	"path/filepath"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

func TestExecRunsACommandInTheWorktreeWithItsRuntimes(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	testkit.StubPHP(t, d.Bin, "8.1.0") // the system PHP
	miseBin := filepath.Join(d.Home, "mise-installs", "php", "bin")
	testkit.StubPHP(t, miseBin, "8.4.2")
	testkit.StubMise(t, d.Bin, map[string]string{"PATH": miseBin + ":" + d.Path})
	repo := gitfixture.New(t, "exec-runtimes").WriteFile("app/readme.txt", "hi").Commit("app dir")
	d.MustRunCLI(repo.Dir, "link")

	res := d.MustRunCLI(filepath.Join(repo.Dir, "app"), "exec", "sh", "-c", "pwd; php -v")

	assertLines(t, res.Stdout, repo.Dir, "PHP 8.4.2 (stub) -v")
}

func TestExecPassesInputAndOutputThroughAndReturnsTheExitCode(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "exec-io")
	d.MustRunCLI(repo.Dir, "link")

	res := d.RunCLIWithInput(repo.Dir, "hello\n", "exec", "--", "sh", "-c", "cat; echo oops >&2; exit 3")

	if res.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3\n%s", res.ExitCode, res.Output())
	}
	assertLines(t, res.Stdout, "hello")
	assertLines(t, res.Stderr, "oops")
}

func TestExecOfAMissingRuntimeSuggestsInstallingItWithMise(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "exec-missing").
		WriteFile("composer.json", `{"require": {"php": "^8.4"}}`).
		Commit("a PHP Project")
	d.MustRunCLI(repo.Dir, "link")

	res := d.RunCLI(repo.Dir, "exec", "php", "artisan", "migrate")

	if res.ExitCode != 127 {
		t.Errorf("exit code = %d, want 127\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, "php was not found through mise or on PATH")
	assertContains(t, res.Stderr, "`mise use php@8.4`")
}

func TestExecOutsideALinkedRepositoryFails(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "exec-unlinked")

	res := d.RunCLI(repo.Dir, "exec", "true")

	if res.ExitCode == 0 {
		t.Fatalf("exec in an unlinked repository succeeded:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "lane link")
}

func TestExecWithoutACommandIsAUsageError(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)

	res := d.RunCLI(d.Home, "exec")

	if res.ExitCode != 2 {
		t.Errorf("exit code = %d, want 2\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, "missing command")
}
