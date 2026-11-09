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

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
	"github.com/lanewayhq/laneway/internal/testkit/pgwire"
)

// postgresProject is a Project whose database is a Postgres database, with
// the Env Patch pointing Laravel's DB_* keys at it, plus the extra lines
// given (such as `worktree = "fresh"`, which land in the [database] table).
func postgresProject(t *testing.T, name, database string) *gitfixture.Repo {
	t.Helper()
	return gitfixture.New(t, name).
		WriteFile(".gitignore", ".env\n").
		WriteFile("laneway.toml", "[database]\nengine = \"postgres\"\n"+database+"\n"+
			"[env]\nDB_HOST = \"{db_host}\"\nDB_PORT = \"{db_port}\"\nDB_DATABASE = \"{db}\"\n"+
			"DB_USERNAME = \"{db_username}\"\nDB_PASSWORD = \"{db_password}\"\n").
		Commit("declare a Postgres database")
}

// dialPostgres connects to the database db of the Postgres Service listening
// on port on the daemon's Service host, as Laneway's shared user.
func dialPostgres(t *testing.T, d *testkit.Daemon, port int, db string) *pgwire.Conn {
	t.Helper()
	conn, err := connectPostgres(d, port, db)
	if err != nil {
		t.Fatalf("connecting to the database %s on port %d: %v", db, port, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func connectPostgres(d *testkit.Daemon, port int, db string) (*pgwire.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return pgwire.Connect(ctx, pgwire.Config{
		Addr:     net.JoinHostPort(d.ServiceHost(), strconv.Itoa(port)),
		User:     "laneway",
		Password: "laneway",
		Database: db,
	})
}

// pgExec runs sql, failing the test on an error.
func pgExec(t *testing.T, conn *pgwire.Conn, sql string) {
	t.Helper()
	if err := conn.Exec(context.Background(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// pgColumn runs sql and returns the first column of every row.
func pgColumn(t *testing.T, conn *pgwire.Conn, sql string) []string {
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

func TestLinkingCreatesTheProjectsEmptyPostgresDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := postgresProject(t, "pg-link", "")

	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	env := readFile(t, filepath.Join(repo.Dir, ".env"))
	for _, line := range []string{"DB_HOST=" + d.ServiceHost(), "DB_PORT=5432", "DB_DATABASE=pg_link",
		"DB_USERNAME=laneway", "DB_PASSWORD=laneway"} {
		assertContains(t, env, line+"\n")
	}
	conn := dialPostgres(t, d, 5432, "pg_link")
	if tables := pgColumn(t, conn, "SELECT tablename FROM pg_tables WHERE schemaname = 'public'"); len(tables) != 0 {
		t.Fatalf("the new database has tables %v, want none", tables)
	}
}

func TestPostgresDatabasesAreMadeByPsqlInsideTheServiceContainer(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := postgresProject(t, "pg-psql", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	// Created with psql, then copied with it: no connection of Laneway's
	// own to the Service.
	if n := countCalls(d.Engine.Calls(), "exec laneway-postgres-18 psql "); n < 2 {
		t.Fatalf("psql ran %d times in the Postgres Service's container, want it to make both databases: %v", n, d.Engine.Calls())
	}
	conn := dialPostgres(t, d, 5432, "pg_psql__feature")
	if tables := pgColumn(t, conn, "SELECT tablename FROM pg_tables WHERE schemaname = 'public'"); len(tables) != 0 {
		t.Fatalf("the Worktree's database has tables %v, want none", tables)
	}
}

func TestANewWorktreeGetsACopyOfTheMainWorktreesPostgresDatabaseByDefault(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := postgresProject(t, "pg-clone", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	main := dialPostgres(t, d, 5432, "pg_clone")
	pgExec(t, main, "CREATE TABLE notes (body text)")
	pgExec(t, main, "INSERT INTO notes VALUES ('main-data')")
	main.Close()

	wt := repo.AddWorktree("Feature.C")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "DB_DATABASE=pg_clone__feature_c\n")
	copied := dialPostgres(t, d, 5432, "pg_clone__feature_c")
	if got := pgColumn(t, copied, "SELECT body FROM notes"); !slices.Equal(got, []string{"main-data"}) {
		t.Fatalf("the Worktree's database has notes %q, want the main Worktree's", got)
	}
	pgExec(t, copied, "INSERT INTO notes VALUES ('worktree-data')")
	main = dialPostgres(t, d, 5432, "pg_clone")
	if got := pgColumn(t, main, "SELECT body FROM notes"); !slices.Equal(got, []string{"main-data"}) {
		t.Fatalf("the main Worktree's database has notes %q after the copy was changed", got)
	}
}

func TestCloningClosesTheConnectionsThatKeepTheMainWorktreesDatabaseInUse(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := postgresProject(t, "pg-in-use", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	// Like the main Worktree's queue worker: connected all the time.
	worker := dialPostgres(t, d, 5432, "pg_in_use")
	pgExec(t, worker, "CREATE TABLE jobs (name text)")
	pgExec(t, worker, "INSERT INTO jobs VALUES ('send-mail')")

	wt := repo.AddWorktree("busy")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	copied := dialPostgres(t, d, 5432, "pg_in_use__busy")
	if got := pgColumn(t, copied, "SELECT name FROM jobs"); !slices.Equal(got, []string{"send-mail"}) {
		t.Fatalf("the Worktree's database has jobs %q, want the main Worktree's", got)
	}
	err := worker.Exec(context.Background(), "SELECT name FROM jobs")
	if !pgwire.HasCode(err, pgwire.AdminShutdown) {
		t.Fatalf("the worker's connection answered %v, want it closed by the copy (SQLSTATE 57P01)", err)
	}
	// The app reconnects as usual.
	reconnected := dialPostgres(t, d, 5432, "pg_in_use")
	if got := pgColumn(t, reconnected, "SELECT name FROM jobs"); !slices.Equal(got, []string{"send-mail"}) {
		t.Fatalf("the main Worktree's database has jobs %q after the copy", got)
	}
}

func TestInFreshModeANewWorktreeGetsAnEmptyPostgresDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := postgresProject(t, "pg-fresh", `worktree = "fresh"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	pgExec(t, dialPostgres(t, d, 5432, "pg_fresh"), "CREATE TABLE notes (body text)")

	wt := repo.AddWorktree("feature-f")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "DB_DATABASE=pg_fresh__feature_f\n")
	fresh := dialPostgres(t, d, 5432, "pg_fresh__feature_f")
	if tables := pgColumn(t, fresh, "SELECT tablename FROM pg_tables WHERE schemaname = 'public'"); len(tables) != 0 {
		t.Fatalf("the fresh database has tables %v, want none", tables)
	}
}

func TestInSharedModeANewWorktreeUsesTheMainWorktreesPostgresDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := postgresProject(t, "pg-shared", `worktree = "shared"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	wt := repo.AddWorktree("feature-s")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "DB_DATABASE=pg_shared\n")
	if _, err := connectPostgres(d, 5432, "pg_shared__feature_s"); !pgwire.HasCode(err, "3D000") {
		t.Fatalf("connecting to pg_shared__feature_s: %v, want it not to exist (SQLSTATE 3D000)", err)
	}
}

func TestALongWorktreeNameGivesADatabaseNameCutToPostgresLimitWithAHash(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := postgresProject(t, "pg-long", `worktree = "fresh"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	long := "Add-the-very-long-feature-that-needs-a-long-branch-name-to-describe-it-"

	var names []string
	for _, suffix := range []string{"one", "two"} {
		wt := repo.AddWorktree(long + suffix)
		d.MustRunCLI(wt.Dir, "worktree", "ready")
		env := readFile(t, filepath.Join(wt.Dir, ".env"))
		_, rest, _ := strings.Cut(env, "DB_DATABASE=")
		name, _, _ := strings.Cut(rest, "\n")
		if !regexp.MustCompile(`^pg_long__add_the_very_long_feature_that_needs_a_long_b_[0-9a-f]{8}$`).MatchString(name) {
			t.Fatalf("DB_DATABASE = %q (%d characters), want the name cut to 63 characters with a hash", name, len(name))
		}
		dialPostgres(t, d, 5432, name)
		names = append(names, name)
	}
	if names[0] == names[1] {
		t.Fatalf("two Worktrees whose names differ after the cut share the database %s", names[0])
	}
}

func TestWorktreesWhoseNamesNormaliseAlikeGetDatabasesOfTheirOwn(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := postgresProject(t, "pg-alike", `worktree = "fresh"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")

	first := repo.AddWorktree("feature-x")
	d.MustRunCLI(first.Dir, "worktree", "ready")
	second := repo.AddWorktree("feature_x")
	d.MustRunCLI(second.Dir, "worktree", "ready")

	if got := envValue(t, filepath.Join(first.Dir, ".env"), "DB_DATABASE"); got != "pg_alike__feature_x" {
		t.Fatalf("the first Worktree's DB_DATABASE = %q, want pg_alike__feature_x", got)
	}
	got := envValue(t, filepath.Join(second.Dir, ".env"), "DB_DATABASE")
	if !regexp.MustCompile(`^pg_alike__feature_x_[0-9a-f]{8}$`).MatchString(got) {
		t.Fatalf("the second Worktree's DB_DATABASE = %q, want pg_alike__feature_x with a hash", got)
	}
	dialPostgres(t, d, 5432, got)
}

func TestAPinnedPostgresVersionGetsItsOwnPortAndGoesIdleOnceSetupIsDone(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	repo := postgresProject(t, "pg-pinned", "[services]\npostgres = \"16\"\n")

	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	assertContains(t, readFile(t, filepath.Join(repo.Dir, ".env")), "DB_PORT=54316\n")
	if got := serviceRow(t, d, "postgres", "16"); !slices.Equal(got, []string{"postgres", "16", d.ServiceHost(), "54316", "running"}) {
		t.Fatalf("`lane services` shows %v, want postgres:16 running on port 54316", got)
	}
	if delay := waitForTimer(t, clk); delay != 15*time.Minute {
		t.Fatalf("Idle scheduled after %s, want the default 15m", delay)
	}
	clk.Advance(15 * time.Minute)
	waitForServiceState(t, d, "postgres", "16", "idle")
}
