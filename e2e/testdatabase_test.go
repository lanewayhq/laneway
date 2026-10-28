package e2e

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/pgwire"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// assertEmptyPostgresDatabase fails the test unless the database db exists
// on the Postgres Service at port and has no tables.
func assertEmptyPostgresDatabase(t *testing.T, d *testkit.Daemon, port int, db string) {
	t.Helper()
	conn := dialPostgres(t, d, port, db)
	if tables := pgColumn(t, conn, "SELECT tablename FROM pg_tables WHERE schemaname = 'public'"); len(tables) != 0 {
		t.Fatalf("the Test Database %s has tables %v, want none", db, tables)
	}
	conn.Close()
}

func TestWithTestDatabasesOnEachWorktreeGetsAnEmptyTestDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := postgresProject(t, "tdb-on", `test = "on"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	assertEmptyPostgresDatabase(t, d, 5432, "tdb_on_test")
	// Tests ran in the main Worktree, and its database has data: neither
	// is copied into a new Worktree's Test Database.
	for _, db := range []string{"tdb_on", "tdb_on_test"} {
		conn := dialPostgres(t, d, 5432, db)
		pgExec(t, conn, "CREATE TABLE notes (body text)")
		pgExec(t, conn, "INSERT INTO notes VALUES ('main-data')")
		conn.Close()
	}

	wt := repo.AddWorktree("Feature.T")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertEmptyPostgresDatabase(t, d, 5432, "tdb_on__feature_t_test")
}

func TestWithoutTestDatabasesNoTestDatabaseIsMade(t *testing.T) {
	t.Parallel()
	for _, test := range []string{``, `test = "off"`, `test = "auto"`} {
		t.Run(test, func(t *testing.T) {
			t.Parallel()
			d := testkit.StartDaemon(t)
			repo := postgresProject(t, "tdb-off", test)
			d.MustRunCLI(repo.Dir, "link", "--yes")
			d.MustRunCLI(repo.Dir, "worktree", "ready")

			if _, err := connectPostgres(d, 5432, "tdb_off_test"); !pgwire.HasCode(err, "3D000") {
				t.Fatalf("connecting to tdb_off_test: %v, want it not to exist (SQLSTATE 3D000)", err)
			}
		})
	}
}

// envValue is the value of key in the env file at path, as written.
func envValue(t *testing.T, path, key string) string {
	t.Helper()
	_, rest, ok := strings.Cut("\n"+readFile(t, path), "\n"+key+"=")
	if !ok {
		t.Fatalf("%s has no %s", path, key)
	}
	value, _, _ := strings.Cut(rest, "\n")
	return value
}

func TestTheTestDbVariableNamesTheWorktreesTestDatabaseCutToPostgresLimit(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "tdb-long").
		WriteFile(".gitignore", ".env\n").
		WriteFile("portby.toml", "[database]\nengine = \"postgres\"\ntest = \"on\"\n\n"+
			"[env]\nDB_DATABASE = \"{db}\"\nDB_TEST_DATABASE = \"{test_db}\"\n").
		Commit("declare a Postgres database with Test Databases")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	if got := envValue(t, filepath.Join(repo.Dir, ".env"), "DB_TEST_DATABASE"); got != "tdb_long_test" {
		t.Fatalf("the main Worktree's DB_TEST_DATABASE = %q, want tdb_long_test", got)
	}
	long := "Add-the-very-long-feature-that-needs-a-long-branch-name-to-describe-it-"

	var names []string
	for _, suffix := range []string{"one", "two"} {
		wt := repo.AddWorktree(long + suffix)
		d.MustRunCLI(wt.Dir, "worktree", "ready")
		name := envValue(t, filepath.Join(wt.Dir, ".env"), "DB_TEST_DATABASE")
		if !regexp.MustCompile(`^tdb_long__add_the_very_long_feature_that_needs_a_[0-9a-f]{8}_test$`).MatchString(name) || len(name) > 63 {
			t.Fatalf("DB_TEST_DATABASE = %q (%d characters), want the name cut with a hash, then _test, within 63 characters", name, len(name))
		}
		if db := envValue(t, filepath.Join(wt.Dir, ".env"), "DB_DATABASE"); name == db {
			t.Fatalf("the Test Database is the Worktree's database %s", db)
		}
		assertEmptyPostgresDatabase(t, d, 5432, name)
		names = append(names, name)
	}
	if names[0] == names[1] {
		t.Fatalf("two Worktrees whose names differ after the cut share the Test Database %s", names[0])
	}
}

func TestWorktreesSharingTheMainWorktreesDatabaseStillGetTheirOwnTestDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := postgresProject(t, "tdb-shared", "worktree = \"shared\"\ntest = \"on\"")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "DB_DATABASE=tdb_shared\n")
	assertEmptyPostgresDatabase(t, d, 5432, "tdb_shared_test")
	assertEmptyPostgresDatabase(t, d, 5432, "tdb_shared__feature_test")
}

func TestEachWorktreeGetsTheMainWorktreesEnvTestingPointedAtItsTestDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "tdb-env").
		WriteFile(".gitignore", ".env\n.env.testing\n").
		WriteFile("portby.toml", "[database]\nengine = \"postgres\"\ntest = \"on\"\n\n"+
			"[env]\nDB_HOST = \"{db_host}\"\nDB_DATABASE = \"{db}\"\n").
		Commit("declare a Postgres database with Test Databases").
		WriteFile(".env.testing", "APP_ENV=testing\n# the tests' database\nDB_DATABASE=laravel_testing\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	for _, tt := range []struct{ dir, db, testDB string }{
		{repo.Dir, "tdb_env", "tdb_env_test"},
		{wt.Dir, "tdb_env__feature", "tdb_env__feature_test"},
	} {
		want := "APP_ENV=testing\n# the tests' database\nDB_DATABASE=" + tt.testDB + "\nDB_HOST=" + d.ServiceHost() + "\n"
		if got := readFile(t, filepath.Join(tt.dir, ".env.testing")); got != want {
			t.Errorf("%s/.env.testing is\n%s\nwant\n%s", tt.dir, got, want)
		}
		assertContains(t, readFile(t, filepath.Join(tt.dir, ".env")), "DB_DATABASE="+tt.db+"\n")
	}
}

func TestWithoutAnEnvTestingInTheMainWorktreeTestConfigIsLeftAloneWithAWarning(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := postgresProject(t, "tdb-no-env", `test = "on"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	wt := repo.AddWorktree("feature")
	res := d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertContains(t, res.Stderr, "warning: ")
	assertContains(t, res.Stderr, ".env.testing")
	assertContains(t, res.Stderr, "tdb_no_env__feature_test")
	assertNoFile(t, filepath.Join(wt.Dir, ".env.testing"))
	assertNoFile(t, filepath.Join(repo.Dir, ".env.testing"))
	assertEmptyPostgresDatabase(t, d, 5432, "tdb_no_env__feature_test")
}

func TestAnEnvTestingAddedLaterIsPatchedByLink(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := postgresProject(t, "tdb-later", `test = "on"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	if err := os.WriteFile(filepath.Join(repo.Dir, ".env.testing"), []byte("APP_ENV=testing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d.MustRunCLI(repo.Dir, "link", "--yes")

	assertContains(t, readFile(t, filepath.Join(repo.Dir, ".env.testing")), "DB_DATABASE=tdb_later_test\n")
	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env.testing")), "APP_ENV=testing\n")
	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env.testing")), "DB_DATABASE=tdb_later__feature_test\n")
}

func TestTheWorktreesDatabaseAndTestDatabaseAreInItsInfo(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := postgresProject(t, "tdb-info", `test = "on"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	info, err := d.Client().Worktree(context.Background(), wt.Dir, "")
	if err != nil {
		t.Fatal(err)
	}
	want := api.Database{Engine: "postgres", Name: "tdb_info__feature", Test: "tdb_info__feature_test"}
	if info.Database == nil || *info.Database != want {
		t.Fatalf("GET /v1/worktree database = %+v, want %+v", info.Database, want)
	}
}

func TestSetupStepsKnowTheWorktreesTestDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := postgresProject(t, "tdb-steps", `test = "on"`).
		WriteFile("portby.local.toml", "[worktree]\non_create = [\"echo \\\"$PORTBY_TEST_DB\\\" > test-db.txt\"]\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	d.MustRunCLI(repo.Dir, "worktree", "ready")

	if got := readFile(t, filepath.Join(repo.Dir, "test-db.txt")); got != "tdb_steps_test\n" {
		t.Fatalf("PORTBY_TEST_DB in a setup step = %q, want tdb_steps_test", got)
	}
}
