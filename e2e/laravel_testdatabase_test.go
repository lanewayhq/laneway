package e2e

import (
	"path/filepath"
	"testing"

	"github.com/lanewayhq/laneway/internal/mysqlwire"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// Test Databases of the Laravel Recipe (database.test = auto) through Seam 1,
// with the Laravel-shaped Project of laravel_test.go.

// laravelEnvTesting is a Laravel app's .env.testing.
const laravelEnvTesting = "APP_ENV=testing\nDB_CONNECTION=mysql\nDB_DATABASE=laravel_testing\n"

// assertNoMySQLDatabase fails the test if the MySQL Service on port has the
// database db.
func assertNoMySQLDatabase(t *testing.T, d *testkit.Daemon, port int, db string) {
	t.Helper()
	conn, err := connectMySQL(d, port, db)
	if err == nil {
		conn.Close()
	}
	if !mysqlwire.HasCode(err, mysqlwire.BadDatabase) {
		t.Fatalf("connecting to %s: %v, want it not to exist (error %d)", db, err, mysqlwire.BadDatabase)
	}
}

func TestALaravelAppWithAnEnvTestingGetsATestDatabaseItsTestsUse(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	laravelStubs(t, d)
	repo := laravelRepo(t, "lara-tdb").
		WriteFile(".env.testing", laravelEnvTesting).
		Commit("add the test env")

	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	if tables := myColumn(t, dialMySQL(t, d, 3306, "lara_tdb_test"), "SHOW TABLES"); len(tables) != 0 {
		t.Fatalf("the Test Database has tables %v, want none", tables)
	}
	if got := envValue(t, filepath.Join(repo.Dir, ".env.testing"), "DB_DATABASE"); got != "lara_tdb_test" {
		t.Errorf(".env.testing has DB_DATABASE=%s, want the Test Database", got)
	}
}

func TestALaravelAppWhoseTestsUseNoDatabaseOfTheirOwnGetsNoTestDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	laravelStubs(t, d)
	repo := laravelRepo(t, "lara-no-tdb")

	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	dialMySQL(t, d, 3306, "lara_no_tdb")
	assertNoMySQLDatabase(t, d, 3306, "lara_no_tdb_test")
	assertNoFile(t, filepath.Join(repo.Dir, ".env.testing"))
}

func TestALaravelWorktreeGetsATestDatabaseFromTheMainWorktreesUncommittedEnvTesting(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	laravelStubs(t, d)
	repo := laravelRepo(t, "lara-tdb-wt").
		WriteFile(".gitignore", ".env\n.env.testing\n/vendor\n/node_modules\n/public/storage\n").
		Commit("keep the test env out of git").
		WriteFile(".env.testing", laravelEnvTesting)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	if tables := myColumn(t, dialMySQL(t, d, 3306, "lara_tdb_wt__feature_test"), "SHOW TABLES"); len(tables) != 0 {
		t.Fatalf("the Worktree's Test Database has tables %v, want none", tables)
	}
	testing := filepath.Join(wt.Dir, ".env.testing")
	for key, want := range map[string]string{"APP_ENV": "testing", "DB_DATABASE": "lara_tdb_wt__feature_test"} {
		if got := envValue(t, testing, key); got != want {
			t.Errorf("the Worktree's .env.testing has %s=%s, want %s", key, got, want)
		}
	}
}

// laravelPHPUnitHardCodingTheDatabase is a phpunit.xml that sends the tests
// to a database of its own choosing.
const laravelPHPUnitHardCodingTheDatabase = `<?xml version="1.0" encoding="UTF-8"?>
<phpunit bootstrap="vendor/autoload.php" colors="true">
    <php>
        <env name="APP_ENV" value="testing"/>
        <env name="DB_CONNECTION" value="mysql"/>
        <env name="DB_DATABASE" value="laravel_testing"/>
    </php>
</phpunit>
`

func TestDoctorWarnsWhenPHPUnitConfigBypassesTheTestDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := laravelRepo(t, "lara-doctor-phpunit").
		WriteFile("phpunit.xml", laravelPHPUnitHardCodingTheDatabase).
		Commit("test against a fixed database")

	res := d.RunCLI(repo.Dir, "doctor")

	assertCheck(t, res.Stdout, "warn", "Test Database", `phpunit.xml sets DB_DATABASE to "laravel_testing"`)
	assertFile(t, filepath.Join(repo.Dir, "phpunit.xml"), laravelPHPUnitHardCodingTheDatabase)
}

func TestAWorktreeStartWarnsWhenPHPUnitConfigBypassesTheTestDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	laravelStubs(t, d)
	repo := laravelRepo(t, "lara-start-phpunit").
		WriteFile("phpunit.xml", laravelPHPUnitHardCodingTheDatabase).
		Commit("test against a fixed database")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	startByBrowsing(t, d, "lara-start-phpunit.localhost")

	out := waitForLogs(t, d, repo.Dir, []string{"logs", "web"}, "laneway: Test Database warning: ")
	assertContains(t, out, `laneway: Test Database warning: phpunit.xml sets DB_DATABASE to "laravel_testing"`)
	assertFile(t, filepath.Join(repo.Dir, "phpunit.xml"), laravelPHPUnitHardCodingTheDatabase)
}

func TestSettingUpAWorktreeWarnsWhenPHPUnitConfigBypassesItsTestDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	laravelStubs(t, d)
	repo := laravelRepo(t, "lara-setup-phpunit").
		WriteFile("phpunit.xml", laravelPHPUnitHardCodingTheDatabase).
		Commit("test against a fixed database")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	wt := repo.AddWorktree("feature")
	res := d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertContains(t, res.Stderr, `warning: phpunit.xml sets DB_DATABASE to "laravel_testing"`)
	dialMySQL(t, d, 3306, "lara_setup_phpunit__feature_test")
}
