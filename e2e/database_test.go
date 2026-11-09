package e2e

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// sqliteProject is a Project whose database is the SQLite file
// database/database.sqlite, with the Env Patch pointing DB_DATABASE at it,
// plus the extra config lines given (such as `worktree = "fresh"`, which
// lands in the [database] table).
func sqliteProject(t *testing.T, name, database string) *gitfixture.Repo {
	t.Helper()
	return gitfixture.New(t, name).
		WriteFile(".gitignore", ".env\n*.sqlite\n").
		WriteFile("database/.gitkeep", "").
		WriteFile("laneway.toml", "[database]\nengine = \"sqlite\"\npath = \"database/database.sqlite\"\n"+database+
			"\n[env]\nDB_DATABASE = \"{db}\"\n").
		Commit("declare a SQLite database")
}

func sqliteFile(dir string) string { return filepath.Join(dir, "database", "database.sqlite") }

func TestANewWorktreeGetsACopyOfTheMainWorktreesSQLiteDatabaseByDefault(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := sqliteProject(t, "db-clone", "")
	writeTestFile(t, sqliteFile(repo.Dir), "main-data")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	wt := repo.AddWorktree("feature-c")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertFile(t, sqliteFile(wt.Dir), "main-data")
	assertFile(t, sqliteFile(repo.Dir), "main-data")
	assertFile(t, filepath.Join(wt.Dir, ".env"), "DB_DATABASE="+sqliteFile(wt.Dir)+"\n")
	assertFile(t, filepath.Join(repo.Dir, ".env"), "DB_DATABASE="+sqliteFile(repo.Dir)+"\n")
}

func TestASQLiteProjectWorksWithNoContainerEngineAndNoPlugins(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t, testkit.NewCatalog(t).Option()) // an empty Catalog: nothing to fetch
	d.Engine.SetUnavailable(errors.New("neither Podman nor Docker is installed"))
	repo := sqliteProject(t, "db-no-engine", "")
	writeTestFile(t, sqliteFile(repo.Dir), "main-data")

	d.MustRunCLI(repo.Dir, "link", "--yes")
	wt := repo.AddWorktree("feature-e")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertFile(t, sqliteFile(wt.Dir), "main-data")
	assertFile(t, filepath.Join(wt.Dir, ".env"), "DB_DATABASE="+sqliteFile(wt.Dir)+"\n")
	if got := installedPlugins(t, d); len(got) != 0 {
		t.Errorf("installed Plugins = %v, want none", got)
	}
	if calls := d.Engine.Calls(); len(calls) != 0 {
		t.Errorf("Laneway called the container engine: %v", calls)
	}
}

func TestTheDatabaseIsCreatedBeforeTheEnvPatchIsApplied(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := sqliteProject(t, "db-before-env", "")
	// On this branch a directory stands where .env goes, so the Env Patch
	// fails.
	repo.Git("checkout", "-q", "-b", "env-dir")
	repo.WriteFile(".env/keep", "")
	repo.Git("add", "-f", ".env/keep")
	repo.Git("commit", "-q", "-m", "a directory called .env")
	repo.Git("checkout", "-q", "main")
	writeTestFile(t, sqliteFile(repo.Dir), "main-data")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	wtDir := filepath.Join(gitfixture.TempDir(t), "env-dir")
	repo.Git("worktree", "add", wtDir, "env-dir")

	res := d.RunCLI(wtDir, "worktree", "ready")

	if res.ExitCode != 1 {
		t.Fatalf("worktree ready exited %d, want 1 (setup failed at env):\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, `failed at step "env"`)
	assertFile(t, sqliteFile(wtDir), "main-data")
}

func TestInFreshModeANewWorktreeGetsAnEmptySQLiteDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := sqliteProject(t, "db-fresh", `worktree = "fresh"`)
	writeTestFile(t, sqliteFile(repo.Dir), "main-data")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	wt := repo.AddWorktree("feature-f")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertFile(t, sqliteFile(wt.Dir), "")
	assertFile(t, sqliteFile(repo.Dir), "main-data")
	assertFile(t, filepath.Join(wt.Dir, ".env"), "DB_DATABASE="+sqliteFile(wt.Dir)+"\n")
}

func TestInSharedModeANewWorktreeUsesTheMainWorktreesSQLiteFile(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	out := filepath.Join(gitfixture.TempDir(t), "ran")
	repo := sqliteProject(t, "db-shared", `worktree = "shared"`).
		WriteFile("laneway.local.toml", "[worktree]\non_create = "+tomlList(`echo "$LANEWAY_DB" >> `+out)+"\n").
		Commit("record the database setup sees")
	writeTestFile(t, sqliteFile(repo.Dir), "main-data")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	wt := repo.AddWorktree("feature-s")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertNoFile(t, sqliteFile(wt.Dir))
	assertFile(t, filepath.Join(wt.Dir, ".env"), "DB_DATABASE="+sqliteFile(repo.Dir)+"\n")
	assertFile(t, out, sqliteFile(repo.Dir)+"\n"+sqliteFile(repo.Dir)+"\n")
}

func TestCloningWhenTheMainWorktreeHasNoDatabaseCreatesAnEmptyOneAndWarns(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := sqliteProject(t, "db-no-main", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	if err := os.Remove(sqliteFile(repo.Dir)); err != nil {
		t.Fatalf("the main Worktree's setup should have created its database: %v", err)
	}

	wt := repo.AddWorktree("feature-m")
	res := d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertFile(t, sqliteFile(wt.Dir), "")
	assertContains(t, res.Stderr, "warning")
	assertContains(t, res.Stderr, sqliteFile(repo.Dir)+" doesn't exist")
	assertContains(t, res.Stdout, "is Ready")
	assertSetupState(t, d, wt.Dir, "ready")
}

func TestRetryingSetupKeepsTheWorktreesDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	marker := filepath.Join(gitfixture.TempDir(t), "fixed")
	repo := sqliteProject(t, "db-retry", "").
		WriteFile("laneway.local.toml", "[worktree]\non_create = "+tomlList("test -e "+marker)+"\n").
		Commit("a setup step that fails until fixed")
	writeTestFile(t, sqliteFile(repo.Dir), "main-data")
	writeTestFile(t, marker, "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	wt := repo.AddWorktree("feature-r")
	if res := d.RunCLI(wt.Dir, "worktree", "ready"); res.ExitCode != 1 {
		t.Fatalf("worktree ready exited %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	assertFile(t, sqliteFile(wt.Dir), "main-data")
	writeTestFile(t, sqliteFile(wt.Dir), "worktree-data")
	writeTestFile(t, sqliteFile(repo.Dir), "newer-main-data")

	writeTestFile(t, marker, "")
	d.MustRunCLI(wt.Dir, "worktree", "ready", "--retry")

	assertFile(t, sqliteFile(wt.Dir), "worktree-data")
	assertFile(t, sqliteFile(repo.Dir), "newer-main-data")
}

func TestCloningASQLiteDatabaseInWALModeCopiesItsLogToo(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := sqliteProject(t, "db-wal", "")
	writeTestFile(t, sqliteFile(repo.Dir), "main-data")
	writeTestFile(t, sqliteFile(repo.Dir)+"-wal", "recent-writes")
	writeTestFile(t, sqliteFile(repo.Dir)+"-shm", "index")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	wt := repo.AddWorktree("feature-w")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertFile(t, sqliteFile(wt.Dir), "main-data")
	assertFile(t, sqliteFile(wt.Dir)+"-wal", "recent-writes")
	assertNoFile(t, sqliteFile(wt.Dir)+"-shm")
}
