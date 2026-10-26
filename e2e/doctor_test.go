package e2e

import (
	"os"
	"strings"
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/platform"
	"github.com/mrsafalpiya/portby.dev/internal/platform/platformtest"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

func TestDoctorReportsAHealthySetup(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "install")
	// The in-process daemon is this test process.
	d.Platform.SetPortHolders(443, platform.Holder{Program: "portby", PID: os.Getpid()})
	d.Platform.SetContainerEngine(platform.Engine{Name: "podman", Address: "unix:///run/user/1000/podman/podman.sock"})
	d.Platform.SetMise("2026.10.4 linux-x64")

	res := d.MustRunCLI(d.Home, "doctor")

	assertCheck(t, res.Stdout, "ok", "daemon", "running")
	assertCheck(t, res.Stdout, "ok", "service", "")
	assertCheck(t, res.Stdout, "ok", "port 443", "bound by Portby")
	assertCheck(t, res.Stdout, "ok", "port 80", "free")
	assertCheck(t, res.Stdout, "ok", "port permission", "")
	assertCheck(t, res.Stdout, "ok", "CA in "+platformtest.LinuxStore, "trusted")
	assertCheck(t, res.Stdout, "ok", "container engine", "podman")
	assertCheck(t, res.Stdout, "ok", "mise", "2026.10.4")
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
	assertCheck(t, res.Stdout, "FAIL", "port permission", "")
	assertCheck(t, res.Stdout, "FAIL", "CA in "+platformtest.LinuxStore, "not trusted")
	assertCheck(t, res.Stdout, "FAIL", "container engine", "")
	assertCheck(t, res.Stdout, "warn", "mise", "not found")
	assertContains(t, res.Stdout, "portby install")
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
