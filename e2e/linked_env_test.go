package e2e

import (
	"path/filepath"
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

func TestLinkedEnvKeysAreOnlyPatchedIntoWorktreesOtherThanTheMainOne(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := envProject(t, "env-linked", "APP_URL = \"{url}\"\n\n[linked_env]\nREDIS_PREFIX = \"{project}_{worktree}_\"\n").
		WriteFile(".env", "APP_URL=http://localhost\nREDIS_PREFIX=mine_\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	wt := repo.AddWorktree("feature-l")
	waitForWorktree(t, d, "env-linked", "feature-l")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertFile(t, filepath.Join(wt.Dir, ".env"),
		"APP_URL=https://feature-l.env-linked.localhost\nREDIS_PREFIX=env-linked_feature-l_\n")
	assertFile(t, filepath.Join(repo.Dir, ".env"), "APP_URL=https://env-linked.localhost\nREDIS_PREFIX=mine_\n")
}

// TestRunningProcessesRestartWhenPortbyChangesTheirEnvFile: a Process reads
// .env when it starts (php artisan serve --no-reload doesn't watch it), so
// after Portby changes .env its running Processes start again with it.
func TestRunningProcessesRestartWhenPortbyChangesTheirEnvFile(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	repo := envProject(t, "env-restart", "GREETING = \"one\"\n\n"+
		processConfig(`exec `+server+` --name "$(sed -n 's/^GREETING=//p' .env)"`))
	d.MustRunCLI(repo.Dir, "link", "--yes")
	if got := d.GetServer("https://env-restart.localhost/").Name; got != "one" {
		t.Fatalf("the Process started with GREETING %q, want one", got)
	}

	// `portby link` re-applies a changed Env Patch.
	repo.WriteFile("portby.local.toml", "[env]\nGREETING = \"two\"\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	eventually(t, "the Process to restart with GREETING=two", func() bool {
		return d.GetServer("https://env-restart.localhost/").Name == "two"
	})

	// So does starting a running Worktree again.
	repo.WriteFile("portby.local.toml", "[env]\nGREETING = \"three\"\n")
	d.MustRunCLI(repo.Dir, "up")
	if got := d.GetServer("https://env-restart.localhost/").Name; got != "three" {
		t.Errorf("after `portby up`, the Process has GREETING %q, want three", got)
	}
}

// TestPortbysEnvBackupsAreKeptOutOfGit: .env.portby.bak holds the
// developer's secrets, and Laravel's .gitignore doesn't know it, so Portby
// excludes its backups in the repository's own exclude file (not
// .gitignore, which would be a change to commit).
func TestPortbysEnvBackupsAreKeptOutOfGit(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "env-bak-git").
		WriteFile(".gitignore", ".env\n").
		WriteFile("portby.toml", "[env]\nAPP_URL = \"{url}\"\n").
		Commit("ignore .env only, as Laravel does")
	repo.WriteFile(".env", "APP_KEY=base64:secret\nAPP_URL=http://localhost\n")

	d.MustRunCLI(repo.Dir, "link", "--yes")

	assertFile(t, filepath.Join(repo.Dir, ".env.portby.bak"), "APP_KEY=base64:secret\nAPP_URL=http://localhost\n")
	if status := repo.Git("status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Errorf("git status after the first patch =\n%s\nwant a clean tree", status)
	}
	wt := repo.AddWorktree("feature-b")
	wt.WriteFile(".env.portby.bak", "copied by hand\n")
	if status := gitfixture.Git(t, wt.Dir, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Errorf("git status in another Worktree =\n%s\nwant a clean tree", status)
	}
}
