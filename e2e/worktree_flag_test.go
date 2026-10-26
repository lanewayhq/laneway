package e2e

import (
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// Every command that acts on a Worktree acts on the current directory's
// unless --worktree names another: by git's name (within the current
// Project), by Domain or Alias, or by path (from anywhere).
func TestCommandsActOnTheWorktreeNamedByTheWorktreeFlag(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "wt-flag").
		WriteFile("portby.toml", processConfig("echo serving $PWD; exec "+testkit.TestServer(t))).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")
	wt := repo.AddWorktree("feature-y")
	domain := "feature-y.wt-flag.localhost"

	res := d.MustRunCLI(repo.Dir, "worktree", "ready", "--worktree", "feature-y")
	assertContains(t, res.Stdout, `Worktree "feature-y" of "wt-flag" is Ready: https://`+domain+"/")

	res = d.MustRunCLI(repo.Dir, "info", "--worktree", "feature-y")
	assertContains(t, res.Stdout, "https://"+domain+"/")
	res = d.MustRunCLI(d.Home, "info", "--json", "--worktree", domain)
	assertContains(t, res.Stdout, wt.Dir)
	res = d.MustRunCLI(d.Home, "info", "--worktree", wt.Dir)
	assertContains(t, res.Stdout, "https://"+domain+"/")

	res = d.MustRunCLI(repo.Dir, "open", "--worktree", "feature-y")
	assertContains(t, res.Stdout, "Opening https://"+domain+"/")

	res = d.MustRunCLI(repo.Dir, "worktree", "alias", "yy", "--worktree", "feature-y")
	assertContains(t, res.Stdout, `Added the Alias yy.wt-flag.localhost to Worktree "feature-y"`)
	res = d.MustRunCLI(d.Home, "open", "--worktree", "yy.wt-flag.localhost")
	assertContains(t, res.Stdout, "Opening https://"+domain+"/")
	res = d.MustRunCLI(repo.Dir, "worktree", "alias", "--remove", "yy", "--worktree", "feature-y")
	assertContains(t, res.Stdout, `Removed the Alias yy.wt-flag.localhost from Worktree "feature-y"`)

	res = d.MustRunCLI(repo.Dir, "exec", "--worktree", "feature-y", "--", "sh", "-c", "pwd")
	assertContains(t, res.Stdout, wt.Dir+"\n")

	d.GetServer("https://" + domain + "/")
	res = d.MustRunCLI(repo.Dir, "logs", "--worktree", "feature-y", "web")
	assertContains(t, res.Stdout, "serving "+wt.Dir+"\n")

	res = d.RunCLI(repo.Dir, "info", "--worktree", "nope")
	if res.ExitCode == 0 {
		t.Fatalf("info --worktree nope succeeded:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `Project "wt-flag" has no Worktree "nope"; its Worktrees are wt-flag, feature-y`)

	res = d.MustRunCLI(d.Home, "unlink", "--worktree", repo.Dir)
	assertContains(t, res.Stdout, `Unlinked Project "wt-flag"`)
}
