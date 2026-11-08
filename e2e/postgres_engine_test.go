package e2e

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/pgwire"
)

// TestPostgresDatabasesOnARealContainerEngine is the slow tier for Postgres:
// the default version (18, whose image keeps its data elsewhere) and an
// older pinned one, against a real server (SCRAM logins, a real CREATE
// DATABASE … TEMPLATE refusing a template in use).
func TestPostgresDatabasesOnARealContainerEngine(t *testing.T) {
	for name, engine := range realEngines(t) {
		for _, version := range []struct{ version, services string }{{"18", ""}, {"16", "[services]\npostgres = \"16\"\n"}} {
			t.Run(name+"/"+version.version, func(t *testing.T) {
				t.Parallel()
				d, prefix := realEngineDaemon(t, engine)
				containerName := prefix + "-postgres-" + version.version
				removeContainer(t, engine, containerName)
				// `worktree ready` must not wait for a download.
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				if err := engine.Pull(ctx, "docker.io/library/postgres:"+version.version); err != nil {
					t.Fatalf("pulling postgres:%s: %v", version.version, err)
				}
				port := 5432
				if version.version != "18" {
					port = 54316
				}
				project := "pg-real-" + name + "-" + version.version
				db := "pg_real_" + name + "_" + version.version
				repo := postgresProject(t, project, version.services)

				d.MustRunCLI(repo.Dir, "link", "--yes")
				d.MustRunCLI(repo.Dir, "worktree", "ready")
				worker := dialPostgres(t, d, port, db)
				pgExec(t, worker, "CREATE TABLE notes (body text)")
				pgExec(t, worker, "INSERT INTO notes VALUES ('main-data')")

				wt := repo.AddWorktree("feature")
				d.MustRunCLI(wt.Dir, "worktree", "ready")

				copied := dialPostgres(t, d, port, db+"__feature")
				if got := pgColumn(t, copied, "SELECT body FROM notes"); !slices.Equal(got, []string{"main-data"}) {
					t.Fatalf("the Worktree's database has notes %q, want the main Worktree's", got)
				}
				if err := worker.Exec(context.Background(), "SELECT body FROM notes"); !pgwire.HasCode(err, pgwire.AdminShutdown) {
					t.Fatalf("the open connection answered %v, want it closed by the copy (SQLSTATE 57P01)", err)
				}
				copied.Close()

				// The data is in Laneway's volume: it survives the container
				// stopping.
				d.MustRunCLI(d.Home, "services", "down", "postgres:"+version.version)
				again := dialPostgres(t, d, port, db)
				if got := pgColumn(t, again, "SELECT body FROM notes"); !slices.Equal(got, []string{"main-data"}) {
					t.Fatalf("after a restart the main Worktree's database has notes %q", got)
				}
			})
		}
	}
}

// TestTestDatabasesOnARealContainerEngine is the slow tier for Test
// Databases: on a real Postgres server, each Worktree's Test Database is
// created empty (never cloned, even with the main Worktree's Test Database
// in use) and .env.testing points at it.
func TestTestDatabasesOnARealContainerEngine(t *testing.T) {
	for name, engine := range realEngines(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d, prefix := realEngineDaemon(t, engine)
			removeContainer(t, engine, prefix+"-postgres-18")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			if err := engine.Pull(ctx, "docker.io/library/postgres:18"); err != nil {
				t.Fatalf("pulling postgres:18: %v", err)
			}
			db := "tdb_real_" + name
			repo := postgresProject(t, "tdb-real-"+name, `test = "on"`).
				WriteFile(".env.testing", "APP_ENV=testing\n")

			d.MustRunCLI(repo.Dir, "link", "--yes")
			d.MustRunCLI(repo.Dir, "worktree", "ready")
			assertEmptyPostgresDatabase(t, d, 5432, db+"_test")
			// A test run in the main Worktree, still connected.
			tests := dialPostgres(t, d, 5432, db+"_test")
			pgExec(t, tests, "CREATE TABLE notes (body text)")

			wt := repo.AddWorktree("feature")
			d.MustRunCLI(wt.Dir, "worktree", "ready")

			assertEmptyPostgresDatabase(t, d, 5432, db+"__feature_test")
			assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env.testing")), "DB_DATABASE="+db+"__feature_test\n")
			if got := pgColumn(t, tests, "SELECT tablename FROM pg_tables WHERE schemaname = 'public'"); !slices.Equal(got, []string{"notes"}) {
				t.Fatalf("the main Worktree's Test Database has tables %v after a Worktree was added, want notes", got)
			}
		})
	}
}
