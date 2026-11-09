package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/platform"
	"github.com/lanewayhq/laneway/internal/platform/platformtest"
	"github.com/lanewayhq/laneway/internal/testkit"
)

func TestDoctorReportsAHealthySetup(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "install")
	// The in-process daemon is this test process.
	d.Platform.SetPortHolders(443, platform.Holder{Program: "lane", PID: os.Getpid()})
	d.Platform.SetPortHolders(80, platform.Holder{Program: "lane", PID: os.Getpid()})
	d.Platform.SetContainerEngine(platform.Engine{Name: "podman", Address: "unix:///run/user/1000/podman/podman.sock"})
	d.Platform.SetMise("2026.10.4 linux-x64")

	res := d.MustRunCLI(d.Home, "doctor")

	assertCheck(t, res.Stdout, "ok", "daemon", "running")
	assertCheck(t, res.Stdout, "ok", "service", "")
	assertCheck(t, res.Stdout, "ok", "port 443", "bound by Laneway")
	assertCheck(t, res.Stdout, "ok", "port 80", "bound by Laneway")
	assertCheck(t, res.Stdout, "ok", "port permission", "")
	assertCheck(t, res.Stdout, "ok", "CA in "+platformtest.LinuxStore, "trusted")
	assertCheck(t, res.Stdout, "ok", "container engine", "podman")
	assertCheck(t, res.Stdout, "ok", "mise", "2026.10.4")
	assertNotContains(t, res.Stdout, "database driver")
}

func TestDoctorFailsWhenTheDaemonIsNotRunning(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Stop()

	res := d.RunCLI(d.Home, "doctor")

	if res.ExitCode == 0 {
		t.Fatalf("doctor exited 0 with the daemon stopped:\n%s", res.Output())
	}
	assertCheck(t, res.Stdout, "FAIL", "daemon", "not running")
}

func TestDoctorReportsWhatIsNotSetUp(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)

	res := d.RunCLI(d.Home, "doctor")

	if res.ExitCode == 0 {
		t.Fatalf("doctor exited 0 before install:\n%s", res.Output())
	}
	assertCheck(t, res.Stdout, "FAIL", "service", "not installed")
	assertCheck(t, res.Stdout, "FAIL", "port 80", "not listening")
	assertCheck(t, res.Stdout, "FAIL", "port permission", "")
	assertCheck(t, res.Stdout, "FAIL", "CA in "+platformtest.LinuxStore, "not trusted")
	assertCheck(t, res.Stdout, "FAIL", "container engine", "")
	assertCheck(t, res.Stdout, "warn", "mise", "not found")
	assertContains(t, res.Stdout, "lane install")
}

func TestDoctorNamesTheProgramHoldingAPort(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Platform.SetPortHolders(443, platform.Holder{Program: "nginx", PID: 4242})
	d.Platform.SetPortHolders(80, platform.Holder{Program: "apache2", PID: 4343})

	res := d.RunCLI(d.Home, "doctor")

	assertCheck(t, res.Stdout, "FAIL", "port 443", "nginx (pid 4242)")
	assertCheck(t, res.Stdout, "FAIL", "port 80", "apache2 (pid 4343)")
}

func TestDoctorOnWSL2NamesWindowsProgramsHoldingAPort(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Platform.SetWSL2()
	d.Platform.SetPortHolders(443, platform.Holder{Program: "nginx.exe", PID: 9100, Windows: true})

	res := d.RunCLI(d.Home, "doctor")

	assertCheck(t, res.Stdout, "FAIL", "port 443", "Windows program nginx.exe (pid 9100)")
}

func TestDoctorOnWSL2ChecksTheCAOnBothSides(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Platform.SetWSL2()

	before := d.RunCLI(d.Home, "doctor")
	assertCheck(t, before.Stdout, "FAIL", "CA in "+platformtest.LinuxStore, "not trusted")
	assertCheck(t, before.Stdout, "FAIL", "CA in "+platformtest.WindowsStore, "not trusted")

	d.MustRunCLI(d.Home, "install")

	after := d.RunCLI(d.Home, "doctor")
	assertCheck(t, after.Stdout, "ok", "CA in "+platformtest.LinuxStore, "trusted")
	assertCheck(t, after.Stdout, "ok", "CA in "+platformtest.WindowsStore, "trusted")
}

// assertCheck finds doctor's line for check and asserts its status and that
// the line contains detail.
func assertCheck(t *testing.T, out, status, check, detail string) {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		got, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		if !strings.HasPrefix(strings.TrimSpace(rest), check+":") {
			continue
		}
		if got != status {
			t.Fatalf("check %q is %q, want %q:\n%s", check, got, status, out)
		}
		if !strings.Contains(line, detail) {
			t.Fatalf("check %q line does not contain %q:\n%s", check, detail, out)
		}
		return
	}
	t.Fatalf("doctor output has no %q check:\n%s", check, out)
}

func TestDoctorReportsTheRuntimesResolvedForTheCurrentWorktree(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	miseBin := filepath.Join(d.Home, "mise-installs", "php", "bin")
	testkit.StubPHP(t, miseBin, "8.4.2", "Core", "mbstring")
	testkit.StubMise(t, d.Bin, map[string]string{"PATH": miseBin + ":" + d.Path})
	testkit.StubNode(t, d.Bin, "v22.11.0")
	repo := gitfixture.New(t, "doctor-runtimes").
		WriteFile("composer.json", `{"require": {"php": "^8.4", "ext-mbstring": "*"}}`).
		WriteFile("package.json", `{"engines": {"node": ">=20"}}`).
		WriteFile("app/readme.txt", "hi").
		Commit("a PHP and Node Project")

	res := d.RunCLI(filepath.Join(repo.Dir, "app"), "doctor")

	assertCheck(t, res.Stdout, "ok", "php", "8.4.2 at "+filepath.Join(miseBin, "php")+" (through mise)")
	assertCheck(t, res.Stdout, "ok", "node", "22.11.0 at "+filepath.Join(d.Bin, "node")+" (from PATH)")
}

func TestDoctorReportsRuntimeMismatchesAndMissingRuntimes(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	testkit.StubNode(t, d.Bin, "v18.20.0")
	repo := gitfixture.New(t, "doctor-mismatch").
		WriteFile("composer.json", `{"require": {"php": "^8.3"}}`).
		WriteFile("package.json", `{"engines": {"node": "^20 || ^22"}}`).
		Commit("a PHP and Node Project")

	res := d.RunCLI(repo.Dir, "doctor")

	assertCheck(t, res.Stdout, "warn", "php", "PHP was not found through mise or on PATH")
	assertCheck(t, res.Stdout, "warn", "node", `Node 18.20.0 doesn't satisfy engines.node "^20 || ^22" in package.json`)
	assertContains(t, res.Stdout, "fix: install a matching version with mise: `mise use php@8.3`")
	assertContains(t, res.Stdout, "fix: install a matching version with mise: `mise use node@20`")
}

func TestDoctorFallsBackToPathWhenMiseFails(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	testkit.WriteExecutable(t, d.Bin, "mise",
		"echo 'mise ERROR Config files in /work/shop/mise.toml are not trusted.' >&2; exit 1\n")
	testkit.StubPHP(t, d.Bin, "8.4.0")
	repo := gitfixture.New(t, "doctor-mise-fails").
		WriteFile("composer.json", `{"require": {"php": "^8.4"}}`).
		Commit("a PHP Project")

	res := d.RunCLI(repo.Dir, "doctor")

	assertCheck(t, res.Stdout, "warn", "mise env", "Runtimes come from PATH: mise ERROR Config files in /work/shop/mise.toml are not trusted.")
	assertCheck(t, res.Stdout, "ok", "php", "8.4.0 at "+filepath.Join(d.Bin, "php")+" (from PATH)")
}

func TestDoctorChecksEveryServicePortIsLaneways(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(serviceProject(t, "doctor-svc", "redis = \"7\"\nmailpit = \"v1\"", "").Dir, "link")
	d.Platform.SetPortHolders(6379, platform.Holder{Program: "redis-server", PID: 4545})
	// The in-process daemon is this test process.
	d.Platform.SetPortHolders(1025, platform.Holder{Program: "lane", PID: os.Getpid()})

	res := d.RunCLI(d.Home, "doctor")

	assertCheck(t, res.Stdout, "FAIL", "port 6379 (redis:7)", "in use by redis-server (pid 4545)")
	assertCheck(t, res.Stdout, "ok", "port 1025 (mailpit:v1)", "bound by Laneway")
}

func TestDoctorWarnsWhenPHPLacksTheDriverForTheProjectsDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	php := testkit.StubPHP(t, d.Bin, "8.5.0", "Core", "PDO")
	repo := gitfixture.New(t, "doctor-driver").
		WriteFile("composer.json", `{"require": {"php": "^8.2"}}`).
		WriteFile("laneway.toml", "[database]\nengine = \"sqlite\"\npath = \"database/database.sqlite\"\n").
		Commit("a PHP Project on SQLite")

	res := d.RunCLI(repo.Dir, "doctor")

	assertCheck(t, res.Stdout, "warn", "php", "the Project's database is sqlite, but PHP 8.5.0 ("+php+") doesn't load pdo_sqlite")
	assertContains(t, res.Stdout, "fix: install or enable the pdo_sqlite extension for that PHP")
	assertNotContains(t, res.Stdout, "database driver")
}

func TestDoctorSaysItSkippedTheDatabaseDriverCheckWhenTheConfigCannotBeResolved(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	testkit.StubPHP(t, d.Bin, "8.5.0", "Core", "PDO")
	repo := gitfixture.New(t, "doctor-driver-skipped").
		WriteFile("composer.json", `{"require": {"php": "^8.2"}}`).
		WriteFile("laneway.toml", "[database]\nengine = \"sqlite\"\npath = 42\n").
		Commit("a PHP Project whose config is broken")

	res := d.RunCLI(repo.Dir, "doctor")

	assertCheck(t, res.Stdout, "warn", "database driver", "skipped: can't resolve the Project's config")
	assertContains(t, res.Stdout, "laneway.toml")
}
