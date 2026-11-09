package e2e

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/testkit"
	"github.com/lanewayhq/laneway/internal/testkit/mysqlwire"
)

// waitForSetup waits for the Worktree at dir to be set up, for longer than
// `lane worktree ready` gets in tests: MySQL's first start initialises
// its data directory, which can take 20 s.
func waitForSetup(t *testing.T, d *testkit.Daemon, dir string) *api.Setup {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	info, err := d.Client().WorktreeReady(ctx, api.ReadyRequest{Dir: dir, Wait: true})
	if err != nil {
		t.Fatalf("waiting for %s to be Ready: %v", dir, err)
	}
	return info.Worktree.Setup
}

// TestMySQLDatabasesOnARealContainerEngine is the slow tier for MySQL: the
// default version and an older pinned one, against a real server
// (caching_sha2_password's RSA exchange, the image's first start creating
// the superuser, and a copy of a database with what mysqldump would carry:
// foreign keys, generated and default-expression columns, a view, a
// function and a trigger), plus Test Databases.
func TestMySQLDatabasesOnARealContainerEngine(t *testing.T) {
	for name, engine := range realEngines(t) {
		for _, version := range []struct {
			version, services string
			port              int
		}{{"8.4", "", 3306}, {"8.0", "[services]\nmysql = \"8.0\"\n", 33080}} {
			t.Run(name+"/"+version.version, func(t *testing.T) {
				t.Parallel()
				d, prefix := realEngineDaemon(t, engine)
				removeContainer(t, engine, prefix+"-mysql-"+version.version)
				// Setup must not wait for a download.
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				if err := engine.Pull(ctx, "docker.io/library/mysql:"+version.version); err != nil {
					t.Fatalf("pulling mysql:%s: %v", version.version, err)
				}
				port := version.port
				db := "my_real_" + name + "_" + strings.ReplaceAll(version.version, ".", "_")
				repo := mysqlProject(t, strings.ReplaceAll(db, "_", "-"), "test = \"on\"\n"+version.services).
					WriteFile(".env.testing", "APP_ENV=testing\n")

				d.MustRunCLI(repo.Dir, "link", "--yes")
				if setup := waitForSetup(t, d, repo.Dir); setup.State != api.SetupReady {
					t.Fatalf("the main Worktree's setup is %+v, want Ready", setup)
				}
				if tables := myColumn(t, dialMySQL(t, d, port, db+"_test"), "SHOW TABLES"); len(tables) != 0 {
					t.Fatalf("the main Worktree's Test Database has tables %v, want none", tables)
				}
				worker := dialMySQL(t, d, port, db)
				for _, sql := range []string{
					"CREATE TABLE authors (id int AUTO_INCREMENT PRIMARY KEY, name varchar(50) NOT NULL)",
					"CREATE TABLE notes (id int AUTO_INCREMENT PRIMARY KEY, author_id int NOT NULL, body text NOT NULL, " +
						"shout varchar(100) AS (UPPER(body)) STORED, created_at timestamp DEFAULT CURRENT_TIMESTAMP, " +
						"FOREIGN KEY (author_id) REFERENCES authors (id))",
					"INSERT INTO authors (name) VALUES ('ada')",
					"INSERT INTO notes (author_id, body, created_at) VALUES (1, 'main-data', '2026-01-02 03:04:05')",
					"CREATE VIEW note_bodies AS SELECT body FROM notes",
					"CREATE FUNCTION exclaim(s text) RETURNS text DETERMINISTIC RETURN CONCAT(s, '!')",
					"CREATE TRIGGER notes_exclaim BEFORE INSERT ON notes FOR EACH ROW SET NEW.body = exclaim(NEW.body)",
				} {
					myExec(t, worker, sql)
				}

				wt := repo.AddWorktree("feature")
				d.MustRunCLI(wt.Dir, "worktree", "ready")

				copied := dialMySQL(t, d, port, db+"__feature")
				if got := myColumn(t, copied, "SELECT CONCAT_WS('|', author_id, body, shout, created_at) FROM notes"); !slices.Equal(got, []string{"1|main-data|MAIN-DATA|2026-01-02 03:04:05"}) {
					t.Fatalf("the Worktree's database has notes %q, want the main Worktree's", got)
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
				if tables := myColumn(t, dialMySQL(t, d, port, db+"__feature_test"), "SHOW TABLES"); len(tables) != 0 {
					t.Fatalf("the Worktree's Test Database has tables %v, want none", tables)
				}
				assertContains(t, readFile(t, wt.Dir+"/.env.testing"), "DB_DATABASE="+db+"__feature_test\n")

				// A copy that fails leaves nothing behind, and a retry
				// once the cause is gone copies it all.
				for _, sql := range []string{"CREATE TABLE scratch (x int)", "CREATE VIEW broken AS SELECT x FROM scratch", "DROP TABLE scratch"} {
					myExec(t, worker, sql)
				}
				second := repo.AddWorktree("second")
				if res := d.RunCLI(second.Dir, "worktree", "ready"); res.ExitCode != 1 {
					t.Fatalf("worktree ready exited %d, want the copy to fail (1):\n%s", res.ExitCode, res.Output())
				}
				if _, err := connectMySQL(d, port, db+"__second"); !mysqlwire.HasCode(err, mysqlwire.BadDatabase) {
					t.Fatalf("connecting to the failed copy: %v, want it dropped (error 1049)", err)
				}
				myExec(t, worker, "DROP VIEW broken")
				d.MustRunCLI(second.Dir, "worktree", "ready", "--retry")
				if got := myColumn(t, dialMySQL(t, d, port, db+"__second"), "SELECT body FROM note_bodies"); !slices.Equal(got, []string{"main-data"}) {
					t.Fatalf("the retried copy's view shows %q, want the main Worktree's notes", got)
				}

				// The data is in Laneway's volume: it survives the container
				// stopping.
				d.MustRunCLI(d.Home, "services", "down", "mysql:"+version.version)
				again := dialMySQL(t, d, port, db)
				if got := myColumn(t, again, "SELECT body FROM notes"); !slices.Equal(got, []string{"main-data"}) {
					t.Fatalf("after a restart the main Worktree's database has notes %q", got)
				}
			})
		}
	}
}
