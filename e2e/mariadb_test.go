package e2e

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// The mariadb Plugin's Engine through Seam 1. The fake container engine
// answers the mariadb image as it does the mysql one (same protocol, same
// statements), through the image's own client names and env, so a broken
// MariaDB manifest fails here on every pull request; the slow tier runs the
// real images.

// mariadbContainer is the container of the mariadb Engine's default version.
const mariadbContainer = "laneway-mariadb-11.8"

func TestLinkingCreatesTheProjectsEmptyMariaDBDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := dbProject(t, "mariadb", "maria-link", "")

	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	env := readFile(t, filepath.Join(repo.Dir, ".env"))
	for _, line := range []string{"DB_HOST=" + d.ServiceHost(), "DB_PORT=3306",
		"DB_DATABASE=maria_link", "DB_USERNAME=laneway", "DB_PASSWORD=laneway"} {
		assertContains(t, env, line+"\n")
	}
	conn := dialMySQL(t, d, 3306, "maria_link")
	if tables := myColumn(t, conn, "SHOW TABLES"); len(tables) != 0 {
		t.Fatalf("the new database has tables %v, want none", tables)
	}
	if !d.Engine.IsRunning(mariadbContainer) {
		t.Fatalf("the database is served by %v, want %s", d.Engine.Containers(), mariadbContainer)
	}
}

func TestANewWorktreeGetsACopyOfTheMainWorktreesMariaDBDatabaseByDefault(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := dbProject(t, "mariadb", "maria-clone", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	main := dialMySQL(t, d, 3306, "maria_clone")
	myExec(t, main, "CREATE TABLE notes (body text)")
	myExec(t, main, "INSERT INTO notes VALUES ('main-data')")

	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "DB_DATABASE=maria_clone__feature\n")
	copied := dialMySQL(t, d, 3306, "maria_clone__feature")
	if got := myColumn(t, copied, "SELECT body FROM notes"); !slices.Equal(got, []string{"main-data"}) {
		t.Fatalf("the Worktree's database has notes %q, want the main Worktree's", got)
	}
	if got := myColumn(t, copied, "SHOW TABLES"); !slices.Equal(got, []string{"notes"}) {
		t.Fatalf("the Worktree's database has tables %q, want only the main Worktree's", got)
	}
	myExec(t, copied, "INSERT INTO notes VALUES ('worktree-data')")
	if got := myColumn(t, main, "SELECT body FROM notes"); !slices.Equal(got, []string{"main-data"}) {
		t.Fatalf("the main Worktree's database has notes %q after the copy was changed", got)
	}
}

func TestAMariaDBCopyCutShortIsCopiedAgainRatherThanKept(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := dbProject(t, "mariadb", "maria-cut", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	main := dialMySQL(t, d, 3306, "maria_cut")
	myExec(t, main, "CREATE TABLE notes (body text)")
	myExec(t, main, "INSERT INTO notes VALUES ('main-data')")
	// What a daemon killed half way through copying leaves: the database,
	// some of its tables, and the table that marks the copy unfinished.
	myExec(t, main, "CREATE DATABASE `maria_cut__feature`")
	myExec(t, main, "USE `maria_cut__feature`")
	myExec(t, main, "CREATE TABLE laneway_copy_in_progress (id int)")
	myExec(t, main, "CREATE TABLE notes (body text)")

	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	copied := dialMySQL(t, d, 3306, "maria_cut__feature")
	if got := myColumn(t, copied, "SHOW TABLES"); !slices.Equal(got, []string{"notes"}) {
		t.Fatalf("the Worktree's database has tables %q, want only the main Worktree's", got)
	}
	if got := myColumn(t, copied, "SELECT body FROM notes"); !slices.Equal(got, []string{"main-data"}) {
		t.Fatalf("the Worktree's database has notes %q, want the main Worktree's", got)
	}
}

// MySQL and MariaDB both use 3306 (ADR-0005): whichever default version is
// registered first keeps it, and the other gets a port from its version
// block, 33000–33099, which the Registry keeps from then on.
func TestProjectsOnTheMySQLAndMariaDBDefaultVersionsBothLinkOnOneDaemon(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		first, second string // Engines, in the order their Projects Link
	}{
		{"mysql", "mariadb"},
		{"mariadb", "mysql"},
	} {
		t.Run(c.first+" first", func(t *testing.T) {
			t.Parallel()
			d := testkit.StartDaemon(t)
			version := map[string]string{"mysql": "8.4", "mariadb": "11.8"}
			first := dbProject(t, c.first, "port-"+c.first, "")
			second := dbProject(t, c.second, "port-"+c.second, "")

			d.MustRunCLI(first.Dir, "link", "--yes")
			d.MustRunCLI(first.Dir, "worktree", "ready")
			d.MustRunCLI(second.Dir, "link", "--yes")
			d.MustRunCLI(second.Dir, "worktree", "ready")
			firstWt := first.AddWorktree("feature")
			d.MustRunCLI(firstWt.Dir, "worktree", "ready")
			secondWt := second.AddWorktree("feature")
			d.MustRunCLI(secondWt.Dir, "worktree", "ready")

			if got := servicePort(t, d, c.first, version[c.first]); got != "3306" {
				t.Fatalf("%s:%s has port %s, want the standard 3306, as it was registered first", c.first, version[c.first], got)
			}
			fallback := servicePort(t, d, c.second, version[c.second])
			if port, err := strconv.Atoi(fallback); err != nil || port < 33000 || port > 33099 {
				t.Fatalf("%s:%s has port %s, want one from its version block, 33000–33099", c.second, version[c.second], fallback)
			}
			check := func(when string) {
				t.Helper()
				for _, s := range []struct {
					engine string
					repo   *gitfixture.Repo
					wt     *gitfixture.Worktree
					port   string
				}{
					{c.first, first, firstWt, "3306"},
					{c.second, second, secondWt, fallback},
				} {
					if got := servicePort(t, d, s.engine, version[s.engine]); got != s.port {
						t.Errorf("%s: %s:%s has port %s, want %s", when, s.engine, version[s.engine], got, s.port)
					}
					for _, dir := range []string{s.repo.Dir, s.wt.Dir} {
						if got := envValue(t, filepath.Join(dir, ".env"), "DB_PORT"); got != s.port {
							t.Errorf("%s: %s has DB_PORT=%s, want its %s Service's port %s", when, dir, got, s.engine, s.port)
						}
					}
				}
			}
			check("after Linking")
			// Each port is its own Engine's Service, holding its own
			// Project's databases.
			port, _ := strconv.Atoi(fallback)
			dialMySQL(t, d, 3306, strings.ReplaceAll("port-"+c.first+"__feature", "-", "_"))
			dialMySQL(t, d, port, strings.ReplaceAll("port-"+c.second+"__feature", "-", "_"))
			if _, err := connectMySQL(d, 3306, strings.ReplaceAll("port-"+c.second, "-", "_")); err == nil {
				t.Errorf("the %s Project's database is on port 3306, want only on %s's port %d", c.second, c.second, port)
			}

			d.Restart()
			check("after the daemon restarted")
		})
	}
}

func TestALaravelAppOnTheMariaDBConnectionGetsAMariaDBDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t) // nothing installed; its Catalog lists the first-party Plugins
	laravelStubs(t, d)
	repo := laravelRepo(t, "lara-maria")
	repo.WriteFile(".env.example", strings.Replace(laravelEnvExample, "DB_CONNECTION=mysql", "DB_CONNECTION=mariadb", 1)).
		Commit("use Laravel's mariadb connection")

	out := d.MustRunCLI(repo.Dir, "link", "--yes").Stdout
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	assertContains(t, out, "Fetched Plugin mariadb ")
	if got := installedPlugins(t, d); slices.Contains(got, "mysql") || !slices.Contains(got, "mariadb") {
		t.Fatalf("installed Plugins = %v, want mariadb and not mysql", got)
	}
	env := filepath.Join(repo.Dir, ".env")
	for key, want := range map[string]string{
		"DB_CONNECTION": "mariadb",
		"DB_HOST":       d.ServiceHost(),
		"DB_PORT":       "3306",
		"DB_DATABASE":   "lara_maria",
		"DB_USERNAME":   "laneway",
		"DB_PASSWORD":   "laneway",
	} {
		if got := envValue(t, env, key); got != want {
			t.Errorf("%s=%q in .env, want %q", key, got, want)
		}
	}
	if tables := myColumn(t, dialMySQL(t, d, 3306, "lara_maria"), "SHOW TABLES"); len(tables) != 0 {
		t.Fatalf("the new database has tables %v, want none", tables)
	}
	if !d.Engine.IsRunning(mariadbContainer) {
		t.Fatalf("the database is served by %v, want %s", d.Engine.Containers(), mariadbContainer)
	}
}

func TestALaravelAppOnTheMySQLConnectionOptsIntoAPinnedMariaDBWithLanewayToml(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	laravelStubs(t, d)
	repo := laravelRepo(t, "lara-optin")
	repo.WriteFile("laneway.toml", "[database]\nengine = \"mariadb\"\n\n[services]\nmariadb = \"10.6\"\n").
		Commit("develop against MariaDB 10.6, as production runs")

	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	port := servicePort(t, d, "mariadb", "10.6")
	env := filepath.Join(repo.Dir, ".env")
	for key, want := range map[string]string{
		"DB_CONNECTION": "mysql", // the app's own connection, left alone
		"DB_HOST":       d.ServiceHost(),
		"DB_PORT":       port,
		"DB_DATABASE":   "lara_optin",
		"DB_USERNAME":   "laneway",
		"DB_PASSWORD":   "laneway",
	} {
		if got := envValue(t, env, key); got != want {
			t.Errorf("%s=%q in .env, want %q", key, got, want)
		}
	}
	n, _ := strconv.Atoi(port)
	dialMySQL(t, d, n, "lara_optin")
	if !d.Engine.IsRunning("laneway-mariadb-10.6") || d.Engine.IsRunning("laneway-mysql-8.4") {
		t.Fatalf("running containers %v, want MariaDB 10.6's and no MySQL", d.Engine.Containers())
	}
}
