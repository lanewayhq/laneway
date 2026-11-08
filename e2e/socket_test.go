package e2e

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lanewayhq/laneway/internal/client"
	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

func TestAPISocketIsOnlyAccessibleByTheUser(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)

	assertOwnerOnly(t, d.Paths.Socket, os.ModeSocket|0o600)
	assertOwnerOnly(t, filepath.Dir(d.Paths.Socket), os.ModeDir|0o700)
}

func TestDaemonTightensAnExistingSocketDirectory(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Stop()
	if err := os.Chmod(filepath.Dir(d.Paths.Socket), 0o755); err != nil {
		t.Fatal(err)
	}

	d.Start()

	assertOwnerOnly(t, filepath.Dir(d.Paths.Socket), os.ModeDir|0o700)
	assertOwnerOnly(t, d.Paths.Socket, os.ModeSocket|0o600)
}

func TestCLIFailsClearlyWhenDaemonIsNotRunning(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "shop")
	d.Stop()

	for _, args := range [][]string{{"link"}, {"status"}} {
		res := d.RunCLI(repo.Dir, args...)
		if res.ExitCode == 0 {
			t.Fatalf("lane %s succeeded without a daemon:\n%s", args[0], res.Output())
		}
		assertContains(t, res.Stderr, "the Laneway daemon is not running")
	}
	_, err := d.Client().Projects(context.Background())
	if !errors.Is(err, client.ErrDaemonNotRunning) {
		t.Fatalf("Projects error = %v, want ErrDaemonNotRunning", err)
	}
}

func TestSecondDaemonOnSameSocketIsRefused(t *testing.T) {
	t.Parallel()
	first := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "shop")
	first.MustRunCLI(repo.Dir, "link")

	res := first.RunCLI(first.Home, "daemon")
	if res.ExitCode == 0 {
		t.Fatalf("second daemon started:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "already running")

	// The first daemon is unaffected.
	status := first.MustRunCLI(first.Home, "status")
	assertContains(t, status.Stdout, "shop")
}

func assertOwnerOnly(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode() & (os.ModeType | os.ModePerm); got != want {
		t.Errorf("%s mode = %v, want %v", path, got, want)
	}
	if uid := info.Sys().(*syscall.Stat_t).Uid; int(uid) != os.Getuid() {
		t.Errorf("%s owned by uid %d, want %d", path, uid, os.Getuid())
	}
}
