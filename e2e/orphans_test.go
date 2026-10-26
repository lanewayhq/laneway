package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/clock"
	"github.com/mrsafalpiya/portby.dev/internal/daemon"
	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// orphanEpoch is when the fake clock of Orphan tests starts.
var orphanEpoch = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

// orphanDaemon is a test daemon on a fake clock, for driving the Orphan
// grace period.
func orphanDaemon(t *testing.T) (*testkit.Daemon, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(orphanEpoch)
	return testkit.StartDaemon(t, testkit.WithClock(clk)), clk
}

// orphanNames lists the databases the API reports as Orphans, sorted.
func orphanNames(t *testing.T, d *testkit.Daemon) []string {
	t.Helper()
	orphans, err := d.Client().Orphans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, o := range orphans {
		names = append(names, o.Database)
	}
	slices.Sort(names)
	return names
}

// waitForOrphans waits until the API reports exactly the Orphans want.
func waitForOrphans(t *testing.T, d *testkit.Daemon, want ...string) {
	t.Helper()
	slices.Sort(want)
	if want == nil {
		want = []string{}
	}
	var got []string
	eventually(t, "the Orphans to be "+fmtList(want), func() bool {
		got = orphanNames(t, d)
		return slices.Equal(got, want)
	})
}

func fmtList(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return "[" + strings.Join(names, " ") + "]"
}

// orphan is the API's Orphan for the database db.
func orphan(t *testing.T, d *testkit.Daemon, db string) api.Orphan {
	t.Helper()
	orphans, err := d.Client().Orphans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range orphans {
		if o.Database == db {
			return o
		}
	}
	t.Fatalf("%s isn't an Orphan; the Orphans are %v", db, orphanNames(t, d))
	return api.Orphan{}
}

func TestRemovingAWorktreeTurnsItsDatabaseAndTestDatabaseIntoOrphans(t *testing.T) {
	t.Parallel()
	d, _ := orphanDaemon(t)
	repo := postgresProject(t, "orphan-remove", `test = "on"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	waitForOrphans(t, d)

	repo.Git("worktree", "remove", wt.Dir)

	waitForOrphans(t, d, "orphan_remove__feature", "orphan_remove__feature_test")
	o := orphan(t, d, "orphan_remove__feature")
	if !o.Since.Equal(orphanEpoch) || !o.Expires.Equal(orphanEpoch.Add(24*time.Hour)) {
		t.Errorf("Orphan since %v, expires %v; want since %v, expires 24 hours later", o.Since, o.Expires, orphanEpoch)
	}
	if o.Project != "orphan-remove" || o.Worktree != "feature" || o.Service != "postgres:18" || o.Kind != "database" {
		t.Errorf("Orphan = %+v, want the database of Worktree feature of orphan-remove in postgres:18", o)
	}
	if kind := orphan(t, d, "orphan_remove__feature_test").Kind; kind != "test database" {
		t.Errorf("the Test Database's Orphan kind = %q, want test database", kind)
	}
	// Not dropped yet.
	dialPostgres(t, d, 5432, "orphan_remove__feature").Close()
	dialPostgres(t, d, 5432, "orphan_remove").Close()
}

func TestAMovedWorktreeKeepsItsDatabases(t *testing.T) {
	t.Parallel()
	d, _ := orphanDaemon(t)
	repo := postgresProject(t, "orphan-move", `test = "on"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	moved := filepath.Join(filepath.Dir(wt.Dir), "feature-moved")
	repo.Git("worktree", "move", wt.Dir, moved)
	d.MustRunCLI(moved, "worktree", "ready")

	if got := orphanNames(t, d); len(got) != 0 {
		t.Fatalf("after moving the Worktree the Orphans are %v, want none", got)
	}
}

// createDatabases creates empty databases on the Postgres Service at port,
// as a parallel test run (or the developer) would.
func createDatabases(t *testing.T, d *testkit.Daemon, port int, names ...string) {
	t.Helper()
	conn := dialPostgres(t, d, port, "postgres")
	defer conn.Close()
	for _, name := range names {
		pgExec(t, conn, `CREATE DATABASE "`+name+`"`)
	}
}

func TestRemovingAWorktreeTurnsItsParallelTestCopiesIntoOrphans(t *testing.T) {
	t.Parallel()
	d, _ := orphanDaemon(t)
	repo := postgresProject(t, "orphan-copies", `test = "on"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	createDatabases(t, d, 5432,
		"orphan_copies__feature_test_test_1", "orphan_copies__feature_test_test_2",
		// Not copies: the developer's own, and the main Worktree's.
		"orphan_copies__feature_test_test_old", "orphan_copies_test_test_1")

	repo.Git("worktree", "remove", wt.Dir)

	waitForOrphans(t, d, "orphan_copies__feature", "orphan_copies__feature_test",
		"orphan_copies__feature_test_test_1", "orphan_copies__feature_test_test_2")
	o := orphan(t, d, "orphan_copies__feature_test_test_2")
	if o.Kind != "parallel test copy" || o.Worktree != "feature" || !o.Since.Equal(orphanEpoch) {
		t.Errorf("Orphan = %+v, want a parallel test copy of Worktree feature since %v", o, orphanEpoch)
	}
}

func TestAWorktreeRemovedBeforeItHadADatabaseLeavesNoOrphans(t *testing.T) {
	t.Parallel()
	d, _ := orphanDaemon(t)
	repo := postgresProject(t, "orphan-early", `test = "on"`).
		WriteFile("portby.toml", "[database]\nengine = \"postgres\"\ntest = \"on\"\n").
		Commit("no Env Patch")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	// Its setup never gets as far as creating its database.
	d.MustRunCLI(repo.Dir, "services", "down")
	release := d.Engine.HoldStarts()
	wt := repo.AddWorktree("feature")
	waitForWorktree(t, d, "orphan-early", "feature")

	repo.Git("worktree", "remove", "--force", wt.Dir)
	waitForNoWorktree(t, d, "orphan-early", "feature")
	release()

	waitForOrphans(t, d)
}

// postgresDatabases lists the databases on the Postgres Service at port.
func postgresDatabases(t *testing.T, d *testkit.Daemon, port int) []string {
	t.Helper()
	conn := dialPostgres(t, d, port, "postgres")
	defer conn.Close()
	return pgColumn(t, conn, "SELECT datname FROM pg_database WHERE NOT datistemplate")
}

func TestOrphansAreDropped24HoursAfterTheyWereMade(t *testing.T) {
	t.Parallel()
	d, clk := orphanDaemon(t)
	repo := postgresProject(t, "orphan-expire", `test = "on"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	createDatabases(t, d, 5432, "orphan_expire__feature_test_test_1")
	repo.Git("worktree", "remove", wt.Dir)
	waitForOrphans(t, d, "orphan_expire__feature", "orphan_expire__feature_test", "orphan_expire__feature_test_test_1")

	clk.Advance(daemon.OrphanGrace - time.Minute)
	if got := orphanNames(t, d); len(got) != 3 {
		t.Fatalf("a minute before the grace period ends the Orphans are %v, want all three", got)
	}
	clk.Advance(time.Minute)

	waitForOrphans(t, d)
	got := postgresDatabases(t, d, 5432)
	for _, db := range []string{"orphan_expire__feature", "orphan_expire__feature_test", "orphan_expire__feature_test_test_1"} {
		if slices.Contains(got, db) {
			t.Errorf("%s still exists after its grace period", db)
		}
	}
	for _, db := range []string{"orphan_expire", "orphan_expire_test"} {
		if !slices.Contains(got, db) {
			t.Errorf("the main Worktree's %s was dropped", db)
		}
	}
}

func TestOrphansOutliveADaemonRestartAndAreStillDroppedOnTime(t *testing.T) {
	t.Parallel()
	d, clk := orphanDaemon(t)
	repo := postgresProject(t, "orphan-restart", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	repo.Git("worktree", "remove", wt.Dir)
	waitForOrphans(t, d, "orphan_restart__feature")
	clk.Advance(time.Hour)

	d.Restart()

	if o := orphan(t, d, "orphan_restart__feature"); !o.Since.Equal(orphanEpoch) {
		t.Fatalf("after a restart the Orphan is since %v, want %v", o.Since, orphanEpoch)
	}
	clk.Advance(daemon.OrphanGrace - time.Hour)
	waitForOrphans(t, d)
	if slices.Contains(postgresDatabases(t, d, 5432), "orphan_restart__feature") {
		t.Fatal("orphan_restart__feature still exists after its grace period")
	}
}

func TestGcAsksOnATerminalAndOtherwiseDropsOrphansAndLeftoverParallelTestCopies(t *testing.T) {
	t.Parallel()
	d, _ := orphanDaemon(t)
	repo := postgresProject(t, "orphan-gc", `test = "on"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	kept := repo.AddWorktree("kept")
	d.MustRunCLI(kept.Dir, "worktree", "ready")
	gone := repo.AddWorktree("gone")
	d.MustRunCLI(gone.Dir, "worktree", "ready")
	repo.Git("worktree", "remove", gone.Dir)
	waitForOrphans(t, d, "orphan_gc__gone", "orphan_gc__gone_test")
	createDatabases(t, d, 5432,
		// Left by parallel test runs of Worktrees that no longer exist
		// (one removed while the daemon was down, say).
		"orphan_gc__old_test_test_1", "orphan_gc__old_test_test_2",
		// Copies of existing Worktrees' Test Databases, and databases
		// Portby didn't make.
		"orphan_gc_test_test_1", "orphan_gc__kept_test_test_1", "orphan_gc__old_test_test_x", "elsewhere_test_test_1")
	untouched := []string{"orphan_gc", "orphan_gc_test", "orphan_gc__kept", "orphan_gc__kept_test",
		"orphan_gc_test_test_1", "orphan_gc__kept_test_test_1", "orphan_gc__old_test_test_x", "elsewhere_test_test_1"}
	dropped := []string{"orphan_gc__gone", "orphan_gc__gone_test", "orphan_gc__old_test_test_1", "orphan_gc__old_test_test_2"}

	// On a terminal it lists them and asks; no answer is no.
	res := d.RunCLIInTerminal(repo.Dir, "", "gc")

	if res.ExitCode == 0 {
		t.Fatalf("gc without an answer exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stdout, "Drop these 4 databases? [y/N]")
	for _, db := range dropped {
		assertContains(t, res.Stdout, db)
	}
	for _, db := range untouched {
		assertNotContains(t, res.Stdout, db+" ")
	}
	assertContains(t, res.Stderr, "--yes")
	if got := postgresDatabases(t, d, 5432); !containsAll(got, dropped) {
		t.Fatalf("gc dropped databases without being told to; left %v", got)
	}

	// With no terminal (a script, an agent) it drops them at once and
	// says what it dropped.
	res = d.MustRunCLI(repo.Dir, "gc")

	assertNotContains(t, res.Stdout, "[y/N]")
	got := postgresDatabases(t, d, 5432)
	for _, db := range dropped {
		assertContains(t, res.Stdout, "Dropped "+db+" ")
		if slices.Contains(got, db) {
			t.Errorf("gc left %s", db)
		}
	}
	if !containsAll(got, untouched) {
		t.Errorf("gc dropped more than it should; left %v, want at least %v", got, untouched)
	}
	waitForOrphans(t, d)
	res = d.MustRunCLI(repo.Dir, "gc", "--yes")
	assertContains(t, res.Stdout, "Nothing to drop")
}

func containsAll(list, want []string) bool {
	for _, w := range want {
		if !slices.Contains(list, w) {
			return false
		}
	}
	return true
}

// servedPostgresProject is a Project with a web Process and a Postgres
// database with Test Databases, whose Env Patch points DB_DATABASE at it.
func servedPostgresProject(t *testing.T, name string) *gitfixture.Repo {
	t.Helper()
	server := testkit.TestServer(t)
	return gitfixture.New(t, name).
		WriteFile(".gitignore", ".env\n").
		WriteFile("portby.toml", processConfig(server+` --name "$(basename "$(pwd)")"`)+
			"\n[database]\nengine = \"postgres\"\ntest = \"on\"\n\n[env]\nDB_DATABASE = \"{db}\"\n").
		Commit("declare web and a Postgres database")
}

func TestUnlinkStopsTheProjectAndTurnsAllItsDatabasesIntoOrphansLeavingEnvFilesAlone(t *testing.T) {
	t.Parallel()
	d, _ := orphanDaemon(t)
	repo := servedPostgresProject(t, "orphan-unlink")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "alias", "old-name")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	mainPort := d.GetServer("https://orphan-unlink.localhost/").ListenPort
	wtPort := d.GetServer("https://feature.orphan-unlink.localhost/").ListenPort
	d.GetServer("https://old-name.orphan-unlink.localhost/")
	envs := map[string]string{repo.Dir: readFile(t, filepath.Join(repo.Dir, ".env")), wt.Dir: readFile(t, filepath.Join(wt.Dir, ".env"))}

	res := d.MustRunCLI(wt.Dir, "unlink")

	assertContains(t, res.Stdout, `"orphan-unlink"`)
	assertContains(t, res.Stdout, "24 hours")
	waitForOrphans(t, d, "orphan_unlink", "orphan_unlink_test", "orphan_unlink__feature", "orphan_unlink__feature_test")
	for _, port := range []string{mainPort, wtPort} {
		eventually(t, "the Project's Processes to stop", func() bool {
			conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", port))
			if err == nil {
				conn.Close()
			}
			return err != nil
		})
	}
	for _, domain := range []string{"orphan-unlink.localhost", "feature.orphan-unlink.localhost", "old-name.orphan-unlink.localhost"} {
		if r := d.Get("https://" + domain + "/"); r.StatusCode != http.StatusNotFound {
			t.Errorf("https://%s/ after unlink: status %d, want 404", domain, r.StatusCode)
		}
	}
	projects, err := d.Client().Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Errorf("after unlink the Projects are %v, want none", projects)
	}
	for dir, before := range envs {
		if got := readFile(t, filepath.Join(dir, ".env")); got != before {
			t.Errorf("unlink changed %s/.env:\n%s\nwas\n%s", dir, got, before)
		}
		assertNoFile(t, filepath.Join(dir, ".env.portby.bak"))
	}
	if res := d.RunCLI(repo.Dir, "unlink"); res.ExitCode == 0 {
		t.Errorf("unlinking again exited 0:\n%s", res.Output())
	}
}

func TestLinkingAgainTakesBackTheProjectsOrphanedDatabases(t *testing.T) {
	t.Parallel()
	d, clk := orphanDaemon(t)
	repo := servedPostgresProject(t, "orphan-relink")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	conn := dialPostgres(t, d, 5432, "orphan_relink")
	pgExec(t, conn, "CREATE TABLE notes (body text)")
	pgExec(t, conn, "INSERT INTO notes VALUES ('kept')")
	conn.Close()
	d.MustRunCLI(repo.Dir, "unlink")
	waitForOrphans(t, d, "orphan_relink", "orphan_relink_test", "orphan_relink__feature", "orphan_relink__feature_test")
	clk.Advance(time.Hour)

	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	waitForOrphans(t, d)
	clk.Advance(daemon.OrphanGrace)
	conn = dialPostgres(t, d, 5432, "orphan_relink")
	if got := pgColumn(t, conn, "SELECT body FROM notes"); !slices.Equal(got, []string{"kept"}) {
		t.Fatalf("after linking again the main database has notes %q, want the ones it had", got)
	}
	d.GetServer("https://feature.orphan-relink.localhost/")
	if got := postgresDatabases(t, d, 5432); !containsAll(got, []string{"orphan_relink__feature", "orphan_relink__feature_test", "orphan_relink_test"}) {
		t.Fatalf("after linking again the databases are %v, want the Worktrees' kept", got)
	}
}

func TestTheDashboardListsOrphansAndOnlyItsOwnPagesCanUnlinkOrDropThem(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-orphans")
	repo := postgresProject(t, "dash-orphans-shop", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	repo.Git("worktree", "remove", wt.Dir)
	waitForOrphans(t, d, "dash_orphans_shop__feature")

	status, body := dashboardRequest(t, d, http.MethodGet, dash+"/api/v1/orphans", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/orphans: status %d:\n%s", status, body)
	}
	var list api.OrphanList
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Orphans) != 1 || list.Orphans[0].Database != "dash_orphans_shop__feature" || list.Orphans[0].Expires.IsZero() {
		t.Fatalf("the Dashboard's Orphans: %+v", list.Orphans)
	}

	unlink := dash + "/api/v1/projects?dir=" + url.QueryEscape(repo.Dir)
	for _, origin := range []string{"", "https://dash-orphans-shop.localhost", "https://evil.example"} {
		if status, body := dashboardRequest(t, d, http.MethodPost, dash+"/api/v1/gc", origin, api.GCRequest{}); status != http.StatusForbidden {
			t.Errorf("gc from Origin %q: status %d, want 403:\n%s", origin, status, body)
		}
		if status, body := dashboardRequest(t, d, http.MethodDelete, unlink, origin, nil); status != http.StatusForbidden {
			t.Errorf("unlink from Origin %q: status %d, want 403:\n%s", origin, status, body)
		}
	}
	waitForOrphans(t, d, "dash_orphans_shop__feature")
	if projects, _ := d.Client().Projects(context.Background()); len(projects) != 1 {
		t.Fatalf("a rejected request unlinked the Project: %v", projects)
	}

	status, body = dashboardRequest(t, d, http.MethodPost, dash+"/api/v1/gc", dash, api.GCRequest{
		Databases: []api.OrphanRef{{Service: "postgres:18", Database: "dash_orphans_shop__feature"}},
	})
	if status != http.StatusOK {
		t.Fatalf("gc from the Dashboard: status %d:\n%s", status, body)
	}
	waitForOrphans(t, d)
	status, body = dashboardRequest(t, d, http.MethodDelete, unlink, dash, nil)
	if status != http.StatusOK {
		t.Fatalf("unlink from the Dashboard: status %d:\n%s", status, body)
	}
	waitForOrphans(t, d, "dash_orphans_shop")
}

func TestASharedDatabaseIsNeverAnOrphanOfARemovedWorktree(t *testing.T) {
	t.Parallel()
	d, _ := orphanDaemon(t)
	repo := postgresProject(t, "orphan-shared", "worktree = \"shared\"\ntest = \"on\"")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	repo.Git("worktree", "remove", wt.Dir)

	waitForOrphans(t, d, "orphan_shared__feature_test")
}

func TestSQLiteDatabasesAreNeverOrphans(t *testing.T) {
	t.Parallel()
	d, _ := orphanDaemon(t)
	repo := sqliteProject(t, "orphan-sqlite", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	repo.Git("worktree", "remove", "--force", wt.Dir)
	waitForNoWorktree(t, d, "orphan-sqlite", "feature")

	res := d.MustRunCLI(repo.Dir, "unlink")

	assertNotContains(t, res.Stdout, "24 hours")
	waitForOrphans(t, d)
	if _, err := os.Stat(sqliteFile(repo.Dir)); err != nil {
		t.Fatalf("the main Worktree's SQLite file after unlink: %v", err)
	}
}

func TestOrphansThatCannotBeDroppedAreTriedAgainLater(t *testing.T) {
	t.Parallel()
	d, clk := orphanDaemon(t)
	repo := postgresProject(t, "orphan-retry", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	repo.Git("worktree", "remove", wt.Dir)
	waitForOrphans(t, d, "orphan_retry__feature")
	d.MustRunCLI(repo.Dir, "services", "down")
	d.Engine.SetUnavailable(errors.New("the engine is down"))

	clk.Advance(daemon.OrphanGrace)

	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
	defer cancel()
	for {
		delay, err := clk.WaitForTimerWithin(ctx, daemon.OrphanRetry)
		if err != nil {
			t.Fatalf("waiting for the drop to be tried again in %s: %v", daemon.OrphanRetry, err)
		}
		if delay == daemon.OrphanRetry {
			break
		}
		clk.Advance(delay) // something else, such as Idle
	}
	waitForOrphans(t, d, "orphan_retry__feature")
	d.Engine.SetUnavailable(nil)
	clk.Advance(daemon.OrphanRetry)
	waitForOrphans(t, d)
}

// mysqlDatabases lists the databases on the MySQL Service at port.
func mysqlDatabases(t *testing.T, d *testkit.Daemon, port int) []string {
	t.Helper()
	conn := dialMySQL(t, d, port, "")
	defer conn.Close()
	return myColumn(t, conn, "SHOW DATABASES")
}

func TestARemovedWorktreesMySQLDatabasesAreOrphansDroppedAfter24HoursOrByGc(t *testing.T) {
	t.Parallel()
	d, clk := orphanDaemon(t)
	repo := mysqlProject(t, "orphan-mysql", `test = "on"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	conn := dialMySQL(t, d, 3306, "")
	for _, db := range []string{"orphan_mysql__feature_test_test_1", "orphan_mysql__old_test_test_1", "orphan_mysql_test_test_1"} {
		myExec(t, conn, "CREATE DATABASE `"+db+"`")
	}
	conn.Close()

	repo.Git("worktree", "remove", wt.Dir)

	waitForOrphans(t, d, "orphan_mysql__feature", "orphan_mysql__feature_test", "orphan_mysql__feature_test_test_1")
	if s := orphan(t, d, "orphan_mysql__feature").Service; s != "mysql:8.4" {
		t.Errorf("the Orphan's Service is %s, want mysql:8.4", s)
	}
	clk.Advance(daemon.OrphanGrace)
	waitForOrphans(t, d)
	got := mysqlDatabases(t, d, 3306)
	for _, db := range []string{"orphan_mysql__feature", "orphan_mysql__feature_test", "orphan_mysql__feature_test_test_1"} {
		if slices.Contains(got, db) {
			t.Errorf("%s still exists after its grace period", db)
		}
	}

	res := d.MustRunCLI(repo.Dir, "gc", "--yes")

	assertContains(t, res.Stdout, "orphan_mysql__old_test_test_1")
	got = mysqlDatabases(t, d, 3306)
	if slices.Contains(got, "orphan_mysql__old_test_test_1") {
		t.Error("gc --yes left the leftover parallel test copy")
	}
	if !containsAll(got, []string{"orphan_mysql", "orphan_mysql_test", "orphan_mysql_test_test_1"}) {
		t.Errorf("the main Worktree's databases are gone: %v", got)
	}
}

func TestOrphansThatCameDueWhileTheDaemonWasStoppedAreDroppedWhenItStarts(t *testing.T) {
	t.Parallel()
	d, clk := orphanDaemon(t)
	repo := postgresProject(t, "orphan-overdue", `test = "on"`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	for _, name := range []string{"one", "two"} {
		wt := repo.AddWorktree(name)
		d.MustRunCLI(wt.Dir, "worktree", "ready")
		repo.Git("worktree", "remove", wt.Dir)
	}
	waitForOrphans(t, d, "orphan_overdue__one", "orphan_overdue__one_test", "orphan_overdue__two", "orphan_overdue__two_test")
	d.Stop()

	clk.Advance(daemon.OrphanGrace + time.Hour)
	d.Start()

	waitForOrphans(t, d)
	got := postgresDatabases(t, d, 5432)
	for _, db := range []string{"orphan_overdue__one", "orphan_overdue__one_test", "orphan_overdue__two", "orphan_overdue__two_test"} {
		if slices.Contains(got, db) {
			t.Errorf("%s still exists", db)
		}
	}
}
