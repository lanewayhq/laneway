package e2e

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/clock"
	"github.com/lanewayhq/laneway/internal/daemon"
	"github.com/lanewayhq/laneway/internal/testkit"
	"github.com/lanewayhq/laneway/internal/testkit/mysqlwire"
)

// TestMariaDBDatabasesOnARealContainerEngine is the slow tier for the
// mariadb Plugin's Engine: one manifest, run against the oldest version a
// Project is likely to pin (10.6) and the newest tag (12.x), on real servers.
// Each run Links a Project, copies its database for a new Worktree (keeping
// the database's character set and collation, a column's own collation,
// foreign keys, generated and default-expression columns, a view, a
// function, a procedure and a trigger, but no event), drops a copy that
// fails, makes fresh and shared Worktree databases, cuts a long Worktree's
// database name to 64 characters with a hash, gives each Worktree a Test
// Database, and drops a removed Worktree's databases while a client holds a
// transaction open on one of them.
func TestMariaDBDatabasesOnARealContainerEngine(t *testing.T) {
	for name, engine := range realEngines(t) {
		for _, version := range []string{"10.6", "12.3"} {
			t.Run(name+"/"+version, func(t *testing.T) {
				t.Parallel()
				clk := clock.NewFake(orphanEpoch)
				d, prefix := realEngineDaemon(t, engine, testkit.WithClock(clk))
				removeContainer(t, engine, prefix+"-mariadb-"+version)
				// Setup must not wait for a download.
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				if err := engine.Pull(ctx, "docker.io/library/mariadb:"+version); err != nil {
					t.Fatalf("pulling mariadb:%s: %v", version, err)
				}
				services := "[services]\nmariadb = \"" + version + "\"\n"
				repo := mariadbProject(t, "maria-real", "test = \"on\"\n"+services).
					WriteFile(".env.testing", "APP_ENV=testing\n")

				d.MustRunCLI(repo.Dir, "link", "--yes")
				if setup := waitForSetup(t, d, repo.Dir); setup.State != api.SetupReady {
					t.Fatalf("the main Worktree's setup is %+v, want Ready", setup)
				}
				// A pinned version gets a port from its version block, and
				// the Env Patch fills it.
				port, err := strconv.Atoi(servicePort(t, d, "mariadb", version))
				if err != nil || port < 33000 || port > 33099 {
					t.Fatalf("mariadb:%s has port %d, want one from its version block, 33000–33099", version, port)
				}
				assertContains(t, readFile(t, repo.Dir+"/.env"), "DB_PORT="+strconv.Itoa(port)+"\n")
				worker := dialMySQL(t, d, port, "maria_real")
				if got := myColumn(t, worker, "SELECT VERSION()"); len(got) != 1 || !strings.HasPrefix(got[0], version+".") || !strings.Contains(got[0], "MariaDB") {
					t.Fatalf("the server is %v, want MariaDB %s", got, version)
				}
				if tables := myColumn(t, worker, "SHOW TABLES"); len(tables) != 0 {
					t.Fatalf("the main Worktree's database has tables %v, want none", tables)
				}
				if tables := myColumn(t, dialMySQL(t, d, port, "maria_real_test"), "SHOW TABLES"); len(tables) != 0 {
					t.Fatalf("the main Worktree's Test Database has tables %v, want none", tables)
				}
				for _, sql := range []string{
					"ALTER DATABASE maria_real CHARACTER SET latin1 COLLATE latin1_swedish_ci",
					"CREATE TABLE authors (id int AUTO_INCREMENT PRIMARY KEY, name varchar(50) NOT NULL)",
					"CREATE TABLE notes (id int AUTO_INCREMENT PRIMARY KEY, author_id int NOT NULL, body text NOT NULL, " +
						"tag varchar(20) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin, " +
						"shout varchar(100) AS (UPPER(body)) STORED, created_at timestamp DEFAULT CURRENT_TIMESTAMP, " +
						"FOREIGN KEY (author_id) REFERENCES authors (id))",
					"INSERT INTO authors (name) VALUES ('ada')",
					"INSERT INTO notes (author_id, body, tag, created_at) VALUES (1, 'main-data', 'T', '2026-01-02 03:04:05')",
					"CREATE VIEW note_bodies AS SELECT body FROM notes",
					"CREATE FUNCTION exclaim(s text) RETURNS text DETERMINISTIC RETURN CONCAT(s, '!')",
					"CREATE PROCEDURE count_notes(OUT n int) SELECT COUNT(*) INTO n FROM notes",
					"CREATE TRIGGER notes_exclaim BEFORE INSERT ON notes FOR EACH ROW SET NEW.body = exclaim(NEW.body)",
					"CREATE EVENT tick ON SCHEDULE EVERY 1 HOUR DO SELECT 1",
				} {
					myExec(t, worker, sql)
				}

				wt := repo.AddWorktree("feature")
				d.MustRunCLI(wt.Dir, "worktree", "ready")

				copied := dialMySQL(t, d, port, "maria_real__feature")
				schema := "SELECT CONCAT(DEFAULT_CHARACTER_SET_NAME, '/', DEFAULT_COLLATION_NAME) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = DATABASE()"
				if got := myColumn(t, copied, schema); !slices.Equal(got, []string{"latin1/latin1_swedish_ci"}) {
					t.Fatalf("the Worktree's database has the character set and collation %v, want the main Worktree's latin1/latin1_swedish_ci", got)
				}
				tag := "SELECT COLLATION_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'notes' AND COLUMN_NAME = 'tag'"
				if got := myColumn(t, copied, tag); !slices.Equal(got, []string{"utf8mb4_bin"}) {
					t.Fatalf("the copied column tag has the collation %v, want its own utf8mb4_bin", got)
				}
				if got := myColumn(t, copied, "SELECT CONCAT_WS('|', author_id, body, tag, shout, created_at) FROM notes"); !slices.Equal(got, []string{"1|main-data|T|MAIN-DATA|2026-01-02 03:04:05"}) {
					t.Fatalf("the Worktree's database has notes %q, want the main Worktree's", got)
				}
				routines := "SELECT CONCAT(ROUTINE_TYPE, ' ', ROUTINE_NAME) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = DATABASE() ORDER BY ROUTINE_NAME"
				if got := myColumn(t, copied, routines); !slices.Equal(got, []string{"PROCEDURE count_notes", "FUNCTION exclaim"}) {
					t.Fatalf("the Worktree's database has the routines %v, want the main Worktree's", got)
				}
				// An event would run twice, once in each database.
				events := "SELECT EVENT_NAME FROM information_schema.EVENTS WHERE EVENT_SCHEMA = DATABASE()"
				if got := myColumn(t, worker, events); !slices.Equal(got, []string{"tick"}) {
					t.Fatalf("the main Worktree's database has the events %v, want tick", got)
				}
				if got := myColumn(t, copied, events); len(got) != 0 {
					t.Fatalf("the Worktree's database has the events %v, want none", got)
				}
				// The trigger and function came along, the view reads
				// the copy, and the foreign key holds.
				myExec(t, copied, "INSERT INTO notes (author_id, body) VALUES (1, 'worktree-data')")
				if got := myColumn(t, copied, "SELECT body FROM note_bodies ORDER BY body"); !slices.Equal(got, []string{"main-data", "worktree-data!"}) {
					t.Fatalf("the Worktree's view shows %q, want its own notes", got)
				}
				if err := copied.Exec(context.Background(), "INSERT INTO notes (author_id, body) VALUES (99, 'orphan')"); err == nil {
					t.Fatalf("a note by a missing author was accepted: the foreign key wasn't copied")
				}
				// The main Worktree's connection was left alone, and its
				// data too.
				if got := myColumn(t, worker, "SELECT body FROM note_bodies"); !slices.Equal(got, []string{"main-data"}) {
					t.Fatalf("the main Worktree's view shows %q after the copy was changed", got)
				}
				if tables := myColumn(t, dialMySQL(t, d, port, "maria_real__feature_test"), "SHOW TABLES"); len(tables) != 0 {
					t.Fatalf("the Worktree's Test Database has tables %v, want none", tables)
				}
				assertContains(t, readFile(t, wt.Dir+"/.env.testing"), "DB_DATABASE=maria_real__feature_test\n")

				// A copy that fails leaves nothing behind, and a retry
				// once the cause is gone copies it all.
				for _, sql := range []string{"CREATE TABLE scratch (x int)", "CREATE VIEW broken AS SELECT x FROM scratch", "DROP TABLE scratch"} {
					myExec(t, worker, sql)
				}
				second := repo.AddWorktree("second")
				if res := d.RunCLI(second.Dir, "worktree", "ready"); res.ExitCode != 1 {
					t.Fatalf("worktree ready exited %d, want the copy to fail (1):\n%s", res.ExitCode, res.Output())
				}
				if _, err := connectMySQL(d, port, "maria_real__second"); !mysqlwire.HasCode(err, mysqlwire.BadDatabase) {
					t.Fatalf("connecting to the failed copy: %v, want it dropped (error 1049)", err)
				}
				myExec(t, worker, "DROP VIEW broken")
				d.MustRunCLI(second.Dir, "worktree", "ready", "--retry")
				if got := myColumn(t, dialMySQL(t, d, port, "maria_real__second"), "SELECT body FROM note_bodies"); !slices.Equal(got, []string{"main-data"}) {
					t.Fatalf("the retried copy's view shows %q, want the main Worktree's notes", got)
				}

				// Fresh mode, with Worktree names longer than a database
				// name can be: each gets its own empty database, named
				// within MariaDB's 64 characters.
				fresh := mariadbProject(t, "maria-long", "worktree = \"fresh\"\n"+services)
				d.MustRunCLI(fresh.Dir, "link", "--yes")
				d.MustRunCLI(fresh.Dir, "worktree", "ready")
				myExec(t, dialMySQL(t, d, port, "maria_long"), "CREATE TABLE notes (body text)")
				long := "Add-the-very-long-feature-that-needs-a-long-branch-name-to-describe-it-"
				var names []string
				for _, suffix := range []string{"one", "two"} {
					wt := fresh.AddWorktree(long + suffix)
					d.MustRunCLI(wt.Dir, "worktree", "ready")
					name := envValue(t, wt.Dir+"/.env", "DB_DATABASE")
					if !regexp.MustCompile(`^maria_long__add_the_very_long_feature_that_needs_a_long_[0-9a-f]{8}$`).MatchString(name) {
						t.Fatalf("DB_DATABASE = %q (%d characters), want the name cut to 64 characters with a hash", name, len(name))
					}
					if tables := myColumn(t, dialMySQL(t, d, port, name), "SHOW TABLES"); len(tables) != 0 {
						t.Fatalf("the fresh database %s has tables %v, want none", name, tables)
					}
					names = append(names, name)
				}
				if names[0] == names[1] {
					t.Fatalf("two Worktrees whose names differ after the cut share the database %s", names[0])
				}

				// Shared mode: a new Worktree uses the main Worktree's
				// database and gets none of its own.
				shared := mariadbProject(t, "maria-shared", "worktree = \"shared\"\n"+services)
				d.MustRunCLI(shared.Dir, "link", "--yes")
				d.MustRunCLI(shared.Dir, "worktree", "ready")
				sharedWt := shared.AddWorktree("feature")
				d.MustRunCLI(sharedWt.Dir, "worktree", "ready")
				if got := envValue(t, sharedWt.Dir+"/.env", "DB_DATABASE"); got != "maria_shared" {
					t.Fatalf("the shared Worktree has DB_DATABASE=%s, want the main Worktree's maria_shared", got)
				}
				if _, err := connectMySQL(d, port, "maria_shared__feature"); !mysqlwire.HasCode(err, mysqlwire.BadDatabase) {
					t.Fatalf("connecting to maria_shared__feature: %v, want it not to exist (error 1049)", err)
				}

				// A removed Worktree's databases are dropped once their
				// grace period is over, even while the developer's GUI
				// client holds a transaction open on one of them.
				held := dialMySQL(t, d, port, "maria_real__feature")
				myExec(t, held, "START TRANSACTION")
				myExec(t, held, "INSERT INTO notes (author_id, body) VALUES (1, 'uncommitted')")
				repo.Git("worktree", "remove", "--force", wt.Dir)
				waitForOrphans(t, d, "maria_real__feature", "maria_real__feature_test")
				clk.Advance(daemon.OrphanGrace)
				waitForOrphans(t, d)
				got := mysqlDatabases(t, d, port)
				for _, gone := range []string{"maria_real__feature", "maria_real__feature_test"} {
					if slices.Contains(got, gone) {
						t.Errorf("%s still exists after its grace period", gone)
					}
				}
				if !containsAll(got, []string{"maria_real", "maria_real_test", "maria_real__second", "maria_long", "maria_shared"}) {
					t.Fatalf("after dropping the Orphans the databases are %v", got)
				}
				if err := held.Exec(context.Background(), "SELECT 1"); err == nil {
					t.Error("the connection to the dropped database still works")
				}

				// The data is in Laneway's volume: it survives the container
				// stopping.
				d.MustRunCLI(d.Home, "services", "down", "mariadb:"+version)
				if got := myColumn(t, dialMySQL(t, d, port, "maria_real"), "SELECT body FROM notes"); !slices.Equal(got, []string{"main-data"}) {
					t.Fatalf("after a restart the main Worktree's database has notes %q", got)
				}
			})
		}
	}
}
