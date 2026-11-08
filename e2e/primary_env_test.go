package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// eventuallyContains waits until the file at path contains want: Laneway
// re-applies the Env Patch for a moved Primary Domain in the background.
func eventuallyContains(t *testing.T, path, want string) {
	t.Helper()
	eventually(t, path+" to contain "+want, func() bool {
		return strings.Contains(readFile(t, path), want)
	})
}

// appURLProject is a Project whose Primary Process names itself after the
// APP_URL in .env when it starts, so a request shows which APP_URL the
// running Process read. Its starts are counted into log.
func appURLProject(t *testing.T, name string) (repo *gitfixture.Repo, log string) {
	t.Helper()
	command, log := startCounting(t, testkit.TestServer(t)+` --name "$(sed -n 's/^APP_URL=//p' .env)"`)
	repo = gitfixture.New(t, name).
		WriteFile(".gitignore", ".env\n").
		WriteFile("laneway.toml", "[env]\nAPP_URL = \"{url}\"\n\n"+processConfig(command)).
		Commit("declare a Process that reads APP_URL")
	return repo, log
}

func stat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

func TestTheEnvPatchFollowsTheWorktreesPrimaryDomain(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := envProject(t, "primary-env", `APP_URL = "{url}"
SESSION_DOMAIN = "{domain}"
`).WriteFile(".env", "APP_URL=http://localhost\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	wt := repo.AddWorktree("t3-298e5f25")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	env := filepath.Join(wt.Dir, ".env")

	// Added second, basket is the newest Alias though it sorts first.
	d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")
	eventuallyContains(t, env, "APP_URL=https://checkout.primary-env.localhost\nSESSION_DOMAIN=checkout.primary-env.localhost\n")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "basket")
	eventuallyContains(t, env, "APP_URL=https://basket.primary-env.localhost\nSESSION_DOMAIN=basket.primary-env.localhost\n")

	// Removing the newest falls back to the previous Alias, then to the Domain.
	d.MustRunCLI(wt.Dir, "worktree", "alias", "--remove", "basket")
	eventuallyContains(t, env, "APP_URL=https://checkout.primary-env.localhost\nSESSION_DOMAIN=checkout.primary-env.localhost\n")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "--remove", "checkout")
	eventuallyContains(t, env, "APP_URL=https://t3-298e5f25.primary-env.localhost\nSESSION_DOMAIN=t3-298e5f25.primary-env.localhost\n")

	// The same for the main Worktree.
	mainEnv := filepath.Join(repo.Dir, ".env")
	d.MustRunCLI(repo.Dir, "worktree", "alias", "demo")
	eventuallyContains(t, mainEnv, "APP_URL=https://demo.primary-env.localhost\nSESSION_DOMAIN=demo.primary-env.localhost\n")
	d.MustRunCLI(repo.Dir, "worktree", "alias", "--remove", "demo")
	eventuallyContains(t, mainEnv, "APP_URL=https://primary-env.localhost\nSESSION_DOMAIN=primary-env.localhost\n")
}

// With Test Databases on, .env.testing follows the Primary Domain too, and
// neither file's database names change: they come from the Worktree's label.
func TestTheEnvTestingFollowsThePrimaryDomainAndDatabaseNamesStay(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "primary-tdb").
		WriteFile(".gitignore", ".env\n.env.testing\n").
		WriteFile("laneway.toml", "[database]\nengine = \"postgres\"\ntest = \"on\"\n\n"+
			"[env]\nAPP_URL = \"{url}\"\nDB_DATABASE = \"{db}\"\n").
		Commit("declare a Postgres database with Test Databases").
		WriteFile(".env.testing", "APP_ENV=testing\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("t3-61aa")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	for _, tt := range []struct{ dir, alias, db string }{
		{wt.Dir, "checkout", "primary_tdb__t3_61aa"},
		{repo.Dir, "demo", "primary_tdb"},
	} {
		d.MustRunCLI(tt.dir, "worktree", "alias", tt.alias)
		url := "APP_URL=https://" + tt.alias + ".primary-tdb.localhost\n"
		eventuallyContains(t, filepath.Join(tt.dir, ".env"), url+"DB_DATABASE="+tt.db+"\n")
		eventuallyContains(t, filepath.Join(tt.dir, ".env.testing"), url+"DB_DATABASE="+tt.db+"_test\n")

		d.MustRunCLI(tt.dir, "worktree", "alias", "--remove", tt.alias)
		domain := "primary-tdb.localhost"
		if tt.dir == wt.Dir {
			domain = "t3-61aa." + domain
		}
		url = "APP_URL=https://" + domain + "\n"
		eventuallyContains(t, filepath.Join(tt.dir, ".env"), url+"DB_DATABASE="+tt.db+"\n")
		eventuallyContains(t, filepath.Join(tt.dir, ".env.testing"), url+"DB_DATABASE="+tt.db+"_test\n")
	}
}

func TestARunningWorktreesProcessesRestartWhenItsPrimaryDomainMoves(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, log := appURLProject(t, "primary-restart")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	wt := repo.AddWorktree("t3-5c1d")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	url := "https://t3-5c1d.primary-restart.localhost/"
	if got := d.GetServer(url).Name; got != "https://t3-5c1d.primary-restart.localhost" {
		t.Fatalf("the Process started with APP_URL %q", got)
	}

	d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")
	eventually(t, "the Process to restart with the checkout APP_URL", func() bool {
		return d.GetServer(url).Name == "https://checkout.primary-restart.localhost"
	})
	d.MustRunCLI(wt.Dir, "worktree", "alias", "basket")
	eventually(t, "the Process to restart with the basket APP_URL", func() bool {
		return d.GetServer(url).Name == "https://basket.primary-restart.localhost"
	})
	started := starts(t, log)
	env := filepath.Join(wt.Dir, ".env")
	before := stat(t, env)

	// checkout isn't the Primary Domain: removing it changes nothing else.
	d.MustRunCLI(wt.Dir, "worktree", "alias", "--remove", "checkout")
	if !os.SameFile(before, stat(t, env)) {
		t.Errorf("removing an Alias that isn't the Primary Domain rewrote %s", env)
	}
	// Removing basket moves the Primary Domain back to the Domain.
	d.MustRunCLI(wt.Dir, "worktree", "alias", "--remove", "basket")
	eventually(t, "the Process to restart with the Domain's APP_URL", func() bool {
		return d.GetServer(url).Name == "https://t3-5c1d.primary-restart.localhost"
	})
	if n := starts(t, log) - started; n != 1 {
		t.Errorf("the Process started %d more times after removing both Aliases, want 1 (for basket only)", n)
	}

	// The same for the main Worktree.
	mainURL := "https://primary-restart.localhost/"
	if got := d.GetServer(mainURL).Name; got != "https://primary-restart.localhost" {
		t.Fatalf("the main Worktree's Process started with APP_URL %q", got)
	}
	d.MustRunCLI(repo.Dir, "worktree", "alias", "demo")
	eventually(t, "the main Worktree's Process to restart with the demo APP_URL", func() bool {
		return d.GetServer(mainURL).Name == "https://demo.primary-restart.localhost"
	})
	d.MustRunCLI(repo.Dir, "worktree", "alias", "--remove", "demo")
	eventually(t, "the main Worktree's Process to restart with its Domain's APP_URL", func() bool {
		return d.GetServer(mainURL).Name == "https://primary-restart.localhost"
	})
}

func TestAnAliasAddedWhilePreparingIsInTheEnvOnceTheWorktreeIsReady(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	gate := filepath.Join(gitfixture.TempDir(t), "gate")
	repo := envProject(t, "primary-preparing", "APP_URL = \"{url}\"\n\n[worktree]\non_create = "+
		tomlList("until [ -e "+gate+" ]; do sleep 0.05; done")+"\n")
	writeTestFile(t, gate, "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	if err := os.Remove(gate); err != nil {
		t.Fatal(err)
	}
	wt := repo.AddWorktree("t3-0b9f")
	waitForWorktree(t, d, "primary-preparing", "t3-0b9f")
	assertSetupState(t, d, wt.Dir, "preparing")

	d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")
	writeTestFile(t, gate, "")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertSetupState(t, d, wt.Dir, "ready")
	eventuallyContains(t, filepath.Join(wt.Dir, ".env"), "APP_URL=https://checkout.primary-preparing.localhost\n")
}

func TestNamingAStoppedWorktreeDoesNotStartItAndItsNextStartUsesTheNewEnv(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, log := appURLProject(t, "primary-stopped")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	wt := repo.AddWorktree("t3-a7e2")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")
	eventuallyContains(t, filepath.Join(wt.Dir, ".env"), "APP_URL=https://checkout.primary-stopped.localhost\n")
	if n := starts(t, log); n != 0 {
		t.Fatalf("naming the stopped Worktree started its Process %d times, want 0", n)
	}

	if got := d.GetServer("https://checkout.primary-stopped.localhost/").Name; got != "https://checkout.primary-stopped.localhost" {
		t.Errorf("the Process started with APP_URL %q, want the checkout Alias's", got)
	}
}
