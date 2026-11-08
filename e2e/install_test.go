package e2e

import (
	"slices"
	"testing"

	"github.com/lanewayhq/laneway/internal/platform/platformtest"
	"github.com/lanewayhq/laneway/internal/testkit"
)

func TestInstallSetsUpPortsServiceEngineAndCATrust(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)

	res := d.MustRunCLI(d.Home, "install")

	// The port permission comes first: the service's daemon binds 443.
	want := []string{"allow ports", "install service", "enable engine", "trust " + platformtest.LinuxStore}
	if got := d.Platform.Actions(); !slices.Equal(got, want) {
		t.Fatalf("install did %q, want %q", got, want)
	}
	if d.Platform.InstalledExecutable() == "" {
		t.Fatal("the service was installed without the lane binary's path")
	}
	assertContains(t, res.Stdout, "sudo")
}

func TestInstallStartsTheDaemonThroughTheService(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Stop()
	d.Platform.OnInstallService(d.Start)

	d.MustRunCLI(d.Home, "install")

	if _, err := d.Client().Daemon(t.Context()); err != nil {
		t.Fatalf("daemon not reachable after install: %v", err)
	}
	if got := d.Platform.Actions(); !slices.Contains(got, "trust "+platformtest.LinuxStore) {
		t.Fatalf("install did %q; want the CA the new daemon created trusted", got)
	}
}

func TestInstallAgainChangesNothing(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "install")
	first := len(d.Platform.Actions())

	res := d.MustRunCLI(d.Home, "install")

	// Re-installing the service is itself safe to repeat; nothing else that
	// prompts (sudo, a Windows dialog) runs again.
	if got := d.Platform.Actions()[first:]; !slices.Equal(got, []string{"install service"}) {
		t.Fatalf("second install did %q, want only the idempotent service install", got)
	}
	assertContains(t, res.Stdout, "already")
}

func TestInstallOnWSL2TrustsTheCAInWindowsToo(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Platform.SetWSL2()

	res := d.MustRunCLI(d.Home, "install")

	if got := d.Platform.Actions(); !slices.Contains(got, "trust "+platformtest.WindowsStore) {
		t.Fatalf("install did %q, want the Windows store trusted too", got)
	}
	// The store's own prompt is shown before the dialog appears.
	assertContains(t, res.Stdout, "(fake) adding Laneway's CA to the "+platformtest.WindowsStore)
}
