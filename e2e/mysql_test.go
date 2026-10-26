package e2e

import (
	"context"
	"net"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/mysqlwire"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// mysqlProject is a Project whose database is a MySQL database, with the Env
// Patch setting Laravel's DB_* keys, plus the extra lines given (such as
// `worktree = "fresh"`, which land in the [database] table).
func mysqlProject(t *testing.T, name, database string) *gitfixture.Repo {
	t.Helper()
	return gitfixture.New(t, name).
		WriteFile(".gitignore", ".env\n").
		WriteFile("portby.toml", "[database]\nengine = \"mysql\"\n"+database+"\n"+
			"[env]\nDB_CONNECTION = \"mysql\"\nDB_HOST = \"{db_host}\"\nDB_PORT = \"{db_port}\"\n"+
			"DB_DATABASE = \"{db}\"\nDB_USERNAME = \"{db_username}\"\nDB_PASSWORD = \"{db_password}\"\n").
		Commit("declare a MySQL database")
}

// dialMySQL connects to the database db of the MySQL Service listening on
// port on the daemon's Service host, as Portby's shared user.
func dialMySQL(t *testing.T, d *testkit.Daemon, port int, db string) *mysqlwire.Conn {
	t.Helper()
	conn, err := connectMySQL(d, port, db)
	if err != nil {
		t.Fatalf("connecting to the database %s on port %d: %v", db, port, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func connectMySQL(d *testkit.Daemon, port int, db string) (*mysqlwire.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return mysqlwire.Connect(ctx, mysqlwire.Config{
		Addr:     net.JoinHostPort(d.ServiceHost(), strconv.Itoa(port)),
		User:     "portby",
		Password: "portby",
		Database: db,
	})
}

// myExec runs sql, failing the test on an error.
func myExec(t *testing.T, conn *mysqlwire.Conn, sql string) {
	t.Helper()
	if err := conn.Exec(context.Background(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// myColumn runs sql and returns the first column of every row.
func myColumn(t *testing.T, conn *mysqlwire.Conn, sql string) []string {
	t.Helper()
	rows, err := conn.Query(context.Background(), sql)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	col := []string{}
	for _, row := range rows {
		col = append(col, row[0])
	}
	return col
}

func TestLinkingCreatesTheProjectsEmptyMySQLDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := mysqlProject(t, "my-link", "")

	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	env := readFile(t, filepath.Join(repo.Dir, ".env"))
	for _, line := range []string{"DB_CONNECTION=mysql", "DB_HOST=" + d.ServiceHost(), "DB_PORT=3306",
		"DB_DATABASE=my_link", "DB_USERNAME=portby", "DB_PASSWORD=portby"} {
		assertContains(t, env, line+"\n")
	}
	conn := dialMySQL(t, d, 3306, "my_link")
	if tables := myColumn(t, conn, "SHOW TABLES"); len(tables) != 0 {
		t.Fatalf("the new database has tables %v, want none", tables)
	}
}

func TestANewWorktreeGetsACopyOfTheMainWorktreesMySQLDatabaseByDefault(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := mysqlProject(t, "my-clone", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	main := dialMySQL(t, d, 3306, "my_clone")
	myExec(t, main, "CREATE TABLE notes (body text)")
	myExec(t, main, "INSERT INTO notes VALUES ('main-data')")

	wt := repo.AddWorktree("Feature.C")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "DB_DATABASE=my_clone__feature_c\n")
	copied := dialMySQL(t, d, 3306, "my_clone__feature_c")
	if got := myColumn(t, copied, "SELECT body FROM notes"); !slices.Equal(got, []string{"main-data"}) {
		t.Fatalf("the Worktree's database has notes %q, want the main Worktree's", got)
	}
	if got := myColumn(t, copied, "SHOW TABLES"); !slices.Equal(got, []string{"notes"}) {
		t.Fatalf("the Worktree's database has tables %q, want only the main Worktree's", got)
	}
	myExec(t, copied, "INSERT INTO notes VALUES ('worktree-data')")
	// The main Worktree's connection was left alone (unlike Postgres's
	// TEMPLATE, a copy doesn't need the database to itself).
	if got := myColumn(t, main, "SELECT body FROM notes"); !slices.Equal(got, []string{"main-data"}) {
		t.Fatalf("the main Worktree's database has notes %q after the copy was changed", got)
	}
}

func TestInFreshModeANewWorktreeGetsAnEmptyMySQLDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := mysqlProject(t, "my-fresh", `worktree = "fresh"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	myExec(t, dialMySQL(t, d, 3306, "my_fresh"), "CREATE TABLE notes (body text)")

	wt := repo.AddWorktree("feature-f")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "DB_DATABASE=my_fresh__feature_f\n")
	if tables := myColumn(t, dialMySQL(t, d, 3306, "my_fresh__feature_f"), "SHOW TABLES"); len(tables) != 0 {
		t.Fatalf("the fresh database has tables %v, want none", tables)
	}
}

func TestInSharedModeANewWorktreeUsesTheMainWorktreesMySQLDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := mysqlProject(t, "my-shared", `worktree = "shared"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	wt := repo.AddWorktree("feature-s")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "DB_DATABASE=my_shared\n")
	if _, err := connectMySQL(d, 3306, "my_shared__feature_s"); !mysqlwire.HasCode(err, mysqlwire.BadDatabase) {
		t.Fatalf("connecting to my_shared__feature_s: %v, want it not to exist (error 1049)", err)
	}
}

func TestALongWorktreeNameGivesADatabaseNameCutToMySQLsLimitWithAHash(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := mysqlProject(t, "my-long", `worktree = "fresh"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	long := "Add-the-very-long-feature-that-needs-a-long-branch-name-to-describe-it-"

	var names []string
	for _, suffix := range []string{"one", "two"} {
		wt := repo.AddWorktree(long + suffix)
		d.MustRunCLI(wt.Dir, "worktree", "ready")
		env := readFile(t, filepath.Join(wt.Dir, ".env"))
		_, rest, _ := strings.Cut(env, "DB_DATABASE=")
		name, _, _ := strings.Cut(rest, "\n")
		// 64 characters: one more than Postgres allows.
		if !regexp.MustCompile(`^my_long__add_the_very_long_feature_that_needs_a_long_br_[0-9a-f]{8}$`).MatchString(name) {
			t.Fatalf("DB_DATABASE = %q (%d characters), want the name cut to 64 characters with a hash", name, len(name))
		}
		dialMySQL(t, d, 3306, name)
		names = append(names, name)
	}
	if names[0] == names[1] {
		t.Fatalf("two Worktrees whose names differ after the cut share the database %s", names[0])
	}
}

func TestAPinnedMySQLVersionGetsItsOwnPortAndGoesIdleOnceSetupIsDone(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	repo := mysqlProject(t, "my-pinned", "[services]\nmysql = \"8.0\"\n")

	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	assertContains(t, readFile(t, filepath.Join(repo.Dir, ".env")), "DB_PORT=33080\n")
	if got := serviceRow(t, d, "mysql", "8.0"); !slices.Equal(got, []string{"mysql", "8.0", d.ServiceHost(), "33080", "running"}) {
		t.Fatalf("`portby services` shows %v, want mysql:8.0 running on port 33080", got)
	}
	if !slices.Contains(d.Engine.Calls(), "pull docker.io/library/mysql:8.0") {
		t.Fatalf("the engine was asked %q, want the mysql:8.0 image pulled", d.Engine.Calls())
	}
	if delay := waitForTimer(t, clk); delay != 15*time.Minute {
		t.Fatalf("Idle scheduled after %s, want the default 15m", delay)
	}
	clk.Advance(15 * time.Minute)
	waitForServiceState(t, d, "mysql", "8.0", "idle")
}

func TestACopyCutShortIsCopiedAgainRatherThanKept(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := mysqlProject(t, "my-cut", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	main := dialMySQL(t, d, 3306, "my_cut")
	myExec(t, main, "CREATE TABLE notes (body text)")
	myExec(t, main, "INSERT INTO notes VALUES ('main-data')")
	// What a daemon killed half way through copying leaves: the database,
	// some of its tables, and the table that marks the copy unfinished.
	myExec(t, main, "CREATE DATABASE `my_cut__feature`")
	myExec(t, main, "USE `my_cut__feature`")
	myExec(t, main, "CREATE TABLE portby_copy_in_progress (id int)")
	myExec(t, main, "CREATE TABLE notes (body text)")

	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	copied := dialMySQL(t, d, 3306, "my_cut__feature")
	if got := myColumn(t, copied, "SHOW TABLES"); !slices.Equal(got, []string{"notes"}) {
		t.Fatalf("the Worktree's database has tables %q, want only the main Worktree's", got)
	}
	if got := myColumn(t, copied, "SELECT body FROM notes"); !slices.Equal(got, []string{"main-data"}) {
		t.Fatalf("the Worktree's database has notes %q, want the main Worktree's", got)
	}
}

func TestWithTestDatabasesOnEachWorktreeGetsAnEmptyMySQLTestDatabaseInEnvTesting(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := mysqlProject(t, "my-tdb", `test = "on"`)
	repo.WriteFile(".env.testing", "APP_ENV=testing\nDB_CONNECTION=mysql\nDB_DATABASE=laravel_test\n").
		Commit("add the test env")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	main := dialMySQL(t, d, 3306, "my_tdb")
	myExec(t, main, "CREATE TABLE notes (body text)")
	myExec(t, main, "INSERT INTO notes VALUES ('main-data')")

	wt := repo.AddWorktree("Feature.T")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	testing := filepath.Join(wt.Dir, ".env.testing")
	for key, want := range map[string]string{"APP_ENV": "testing", "DB_DATABASE": "my_tdb__feature_t_test",
		"DB_HOST": d.ServiceHost(), "DB_PORT": "3306", "DB_USERNAME": "portby", "DB_PASSWORD": "portby"} {
		if got := envValue(t, testing, key); got != want {
			t.Errorf(".env.testing has %s=%s, want %s", key, got, want)
		}
	}
	if tables := myColumn(t, dialMySQL(t, d, 3306, "my_tdb__feature_t_test"), "SHOW TABLES"); len(tables) != 0 {
		t.Fatalf("the Test Database has tables %v, want none (never copied)", tables)
	}
	// The Worktree's own database is still the copy.
	if got := envValue(t, filepath.Join(wt.Dir, ".env"), "DB_DATABASE"); got != "my_tdb__feature_t" {
		t.Fatalf(".env has DB_DATABASE=%s, want the Worktree's own database", got)
	}
}
