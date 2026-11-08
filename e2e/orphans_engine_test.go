package e2e

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/clock"
	"github.com/lanewayhq/laneway/internal/daemon"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// TestOrphansOnARealContainerEngine is the slow tier for Orphans on
// Postgres: a removed Worktree's database, Test Database and parallel test
// copies are found on a real server and dropped once their grace period is
// over, even with a connection still open to one of them; `gc` drops
// leftover copies.
func TestOrphansOnARealContainerEngine(t *testing.T) {
	for name, engine := range realEngines(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewFake(orphanEpoch)
			d, prefix := realEngineDaemon(t, engine, testkit.WithClock(clk))
			removeContainer(t, engine, prefix+"-postgres-18")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			if err := engine.Pull(ctx, "docker.io/library/postgres:18"); err != nil {
				t.Fatalf("pulling postgres:18: %v", err)
			}
			db := "orphan_real_" + name
			repo := postgresProject(t, "orphan-real-"+name, `test = "on"`)
			d.MustRunCLI(repo.Dir, "link", "--yes")
			d.MustRunCLI(repo.Dir, "worktree", "ready")
			wt := repo.AddWorktree("feature")
			d.MustRunCLI(wt.Dir, "worktree", "ready")
			createDatabases(t, d, 5432, db+"__feature_test_test_1", db+"__old_test_test_1")
			// The developer's GUI client, left open.
			held := dialPostgres(t, d, 5432, db+"__feature")

			repo.Git("worktree", "remove", wt.Dir)
			waitForOrphans(t, d, db+"__feature", db+"__feature_test", db+"__feature_test_test_1")
			clk.Advance(daemon.OrphanGrace)
			waitForOrphans(t, d)

			got := postgresDatabases(t, d, 5432)
			for _, gone := range []string{db + "__feature", db + "__feature_test", db + "__feature_test_test_1"} {
				if slices.Contains(got, gone) {
					t.Errorf("%s still exists after its grace period", gone)
				}
			}
			if !containsAll(got, []string{db, db + "_test", db + "__old_test_test_1"}) {
				t.Fatalf("after dropping the Orphans the databases are %v", got)
			}
			if err := held.Exec(context.Background(), "SELECT 1"); err == nil {
				t.Error("the connection to the dropped database still works")
			}

			res := d.MustRunCLI(repo.Dir, "gc", "--yes")
			assertContains(t, res.Stdout, db+"__old_test_test_1")
			if slices.Contains(postgresDatabases(t, d, 5432), db+"__old_test_test_1") {
				t.Error("gc --yes left the leftover parallel test copy")
			}
		})
	}
}

// TestMySQLOrphansOnARealContainerEngine is the slow tier for Orphans on
// MySQL: a removed Worktree's databases are found on a real server and
// dropped once their grace period is over, even while a connection holds a
// transaction open on one of them (which would make DROP DATABASE wait for
// its metadata lock); `gc` drops leftover parallel test copies.
func TestMySQLOrphansOnARealContainerEngine(t *testing.T) {
	for name, engine := range realEngines(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewFake(orphanEpoch)
			d, prefix := realEngineDaemon(t, engine, testkit.WithClock(clk))
			removeContainer(t, engine, prefix+"-mysql-8.4")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			if err := engine.Pull(ctx, "docker.io/library/mysql:8.4"); err != nil {
				t.Fatalf("pulling mysql:8.4: %v", err)
			}
			db := "orphan_my_real_" + name
			repo := mysqlProject(t, strings.ReplaceAll(db, "_", "-"), `test = "on"`)
			d.MustRunCLI(repo.Dir, "link", "--yes")
			if setup := waitForSetup(t, d, repo.Dir); setup.State != api.SetupReady {
				t.Fatalf("the main Worktree's setup is %+v, want Ready", setup)
			}
			wt := repo.AddWorktree("feature")
			if setup := waitForSetup(t, d, wt.Dir); setup.State != api.SetupReady {
				t.Fatalf("the Worktree's setup is %+v, want Ready", setup)
			}
			admin := dialMySQL(t, d, 3306, "")
			for _, copy := range []string{db + "__feature_test_test_1", db + "__old_test_test_1"} {
				myExec(t, admin, "CREATE DATABASE `"+copy+"`")
			}
			admin.Close()
			// The developer's GUI client, in the middle of a transaction.
			held := dialMySQL(t, d, 3306, db+"__feature")
			myExec(t, held, "CREATE TABLE notes (body text)")
			myExec(t, held, "START TRANSACTION")
			myExec(t, held, "INSERT INTO notes VALUES ('uncommitted')")

			repo.Git("worktree", "remove", wt.Dir)
			waitForOrphans(t, d, db+"__feature", db+"__feature_test", db+"__feature_test_test_1")
			clk.Advance(daemon.OrphanGrace)
			waitForOrphans(t, d)

			got := mysqlDatabases(t, d, 3306)
			for _, gone := range []string{db + "__feature", db + "__feature_test", db + "__feature_test_test_1"} {
				if slices.Contains(got, gone) {
					t.Errorf("%s still exists after its grace period", gone)
				}
			}
			if !containsAll(got, []string{db, db + "_test", db + "__old_test_test_1"}) {
				t.Fatalf("after dropping the Orphans the databases are %v", got)
			}
			if err := held.Exec(context.Background(), "SELECT 1"); err == nil {
				t.Error("the connection to the dropped database still works")
			}

			res := d.MustRunCLI(repo.Dir, "gc", "--yes")
			assertContains(t, res.Stdout, db+"__old_test_test_1")
			if slices.Contains(mysqlDatabases(t, d, 3306), db+"__old_test_test_1") {
				t.Error("gc --yes left the leftover parallel test copy")
			}
		})
	}
}
