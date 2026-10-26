package e2e

import (
	"path/filepath"
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// Runtimes in these tests are stub executables (testkit.StubPHP and
// friends) in d.Bin, which is first on the PATH the daemon and the CLI see,
// or in a directory the stub mise puts on PATH. No real PHP, Node or mise
// runs.

func TestProcessesRunWithTheRuntimeOnPathWithoutMise(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	testkit.StubPHP(t, d.Bin, "8.3.4")
	repo := gitfixture.New(t, "runtime-path").
		WriteFile("portby.toml", processConfig("php --version; exec sleep 600")).
		Commit("declare web Process")

	d.MustRunCLI(repo.Dir, "link")

	waitForLogs(t, d, repo.Dir, []string{"logs", "web"}, "PHP 8.3.4 (stub)")
}

func TestProcessesRunWithTheRuntimesMiseResolvesForTheWorktree(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	testkit.StubPHP(t, d.Bin, "8.1.0") // the system PHP
	miseBin := filepath.Join(d.Home, "mise-installs", "php", "bin")
	testkit.StubPHP(t, miseBin, "8.4.2")
	testkit.StubMise(t, d.Bin, map[string]string{
		"PATH":           miseBin + ":" + d.Path,
		"FROM_MISE_TOML": "yes",
	})
	repo := gitfixture.New(t, "runtime-mise").
		WriteFile("portby.toml", processConfig("php --version; echo from-mise=$FROM_MISE_TOML; exec sleep 600")).
		Commit("declare web Process")

	d.MustRunCLI(repo.Dir, "link")

	out := waitForLogs(t, d, repo.Dir, []string{"logs", "web"}, "from-mise=")
	assertLines(t, out, "PHP 8.4.2 (stub) --version", "from-mise=yes")
}

func TestAWorktreeStartReportsRuntimeMismatchesInItsOutput(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	testkit.StubPHP(t, d.Bin, "8.2.10", "Core", "mbstring")
	testkit.StubNode(t, d.Bin, "v20.11.1")
	repo := gitfixture.New(t, "runtime-mismatch").
		WriteFile("composer.json", `{"require": {"php": "^8.3", "ext-intl": "*", "ext-mbstring": "*"}}`).
		WriteFile("package.json", `{"engines": {"node": ">=22"}}`).
		WriteFile("portby.toml", processConfig("echo booted; exec sleep 600")).
		Commit("a PHP and Node Project")

	d.MustRunCLI(repo.Dir, "link")

	out := waitForLogs(t, d, repo.Dir, []string{"logs", "web"}, "booted")
	assertContains(t, out, `portby: Runtime warning: PHP 8.2.10 doesn't satisfy require.php "^8.3" in composer.json`)
	assertContains(t, out, "mise use php@8.3")
	assertContains(t, out, "doesn't load intl")
	assertContains(t, out, `portby: Runtime warning: Node 20.11.1 doesn't satisfy engines.node ">=22" in package.json`)
}

func TestAWorktreeStartReportsAMissingRuntimeWithHowToInstallItWithMise(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	testkit.StubMise(t, d.Bin, map[string]string{"PATH": d.Path}, "php@8.4.1")
	repo := gitfixture.New(t, "runtime-missing").
		WriteFile("composer.json", `{"require": {"php": "^8.4"}}`).
		WriteFile("portby.toml", processConfig("echo booted; exec sleep 600")).
		Commit("a PHP Project")

	d.MustRunCLI(repo.Dir, "link")

	out := waitForLogs(t, d, repo.Dir, []string{"logs", "web"}, "booted")
	assertContains(t, out, "php@8.4.1, but it isn't installed; run `mise install`")
	assertContains(t, out, "PHP was not found through mise or on PATH")
	assertContains(t, out, "`mise use php@8.4`")
	assertContains(t, out, "Portby never installs Runtimes")
}
