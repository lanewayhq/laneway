package e2e

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/clock"
	"github.com/mrsafalpiya/portby.dev/internal/daemon"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
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
