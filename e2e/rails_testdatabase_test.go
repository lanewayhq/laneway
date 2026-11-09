package e2e

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lanewayhq/laneway/internal/testkit"
)

// Test Databases of the Rails Recipe (database.test = auto) through Seam 1,
// with the Rails-shaped Project of rails_test.go.

// railsDotenvGemfile is a Rails app's Gemfile that loads .env files with
// dotenv in development and test.
const railsDotenvGemfile = "source \"https://rubygems.org\"\n\nruby file: \".ruby-version\"\n\n" +
	"gem \"rails\", \"~> 8.1.0\"\ngem \"puma\"\n\ngroup :development, :test do\n  gem \"dotenv-rails\"\nend\n"

func TestARailsAppWhoseTestsUsePostgresGetsATestDatabaseInEnvTest(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	railsStubs(t, d)
	repo := railsRepo(t, "rails-tdb", railsPostgresYml).
		WriteFile("Gemfile", railsDotenvGemfile).
		Commit("load .env files")

	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	if tables := pgColumn(t, dialPostgres(t, d, 5432, "rails_tdb_test"),
		"SELECT tablename FROM pg_tables WHERE schemaname = 'public'"); len(tables) != 0 {
		t.Fatalf("the Test Database has tables %v, want none", tables)
	}
	// dotenv-rails reads .env.test over .env when the tests run.
	url := fmt.Sprintf("postgresql://laneway:laneway@%s:5432/rails_tdb_test", d.ServiceHost())
	if got := envValue(t, filepath.Join(repo.Dir, ".env.test"), "DATABASE_URL"); got != url {
		t.Errorf("DATABASE_URL=%q in .env.test, want %q", got, url)
	}
	if got := envValue(t, filepath.Join(repo.Dir, ".env"), "DATABASE_URL"); got != fmt.Sprintf("postgresql://laneway:laneway@%s:5432/rails_tdb", d.ServiceHost()) {
		t.Errorf("DATABASE_URL=%q in .env, want the development database", got)
	}
}

func TestANewWorktreeOfARailsAppGetsATestDatabaseOfItsOwn(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	railsStubs(t, d)
	repo := railsRepo(t, "rails-tdb-wt", railsPostgresYml).
		WriteFile("Gemfile", railsDotenvGemfile).
		Commit("load .env files")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	dialPostgres(t, d, 5432, "rails_tdb_wt__feature_test")
	url := fmt.Sprintf("postgresql://laneway:laneway@%s:5432/rails_tdb_wt__feature_test", d.ServiceHost())
	if got := envValue(t, filepath.Join(wt.Dir, ".env.test"), "DATABASE_URL"); got != url {
		t.Errorf("DATABASE_URL=%q in the Worktree's .env.test, want %q", got, url)
	}
}

func TestARailsAppWhoseTestsUseSQLiteGetsNoTestDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	railsStubs(t, d)
	repo := railsRepo(t, "rails-tdb-lite", railsSQLiteYml+"\ntest:\n  <<: *default\n  database: storage/test.sqlite3\n")

	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	assertNoFile(t, filepath.Join(repo.Dir, ".env.test"))
}

func TestARailsAppThatLoadsNoEnvFilesIsWarnedItsTestsMissTheTestDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := railsRepo(t, "rails-tdb-nodotenv", railsPostgresYml)

	res := d.RunCLI(repo.Dir, "doctor")

	assertCheck(t, res.Stdout, "warn", "Test Database", "the Gemfile doesn't load .env files")
}
