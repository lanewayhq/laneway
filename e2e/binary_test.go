package e2e

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/client"
	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// TestBinary drives the compiled `lane` binary end to end: the daemon as a
// subprocess, and the CLI as separate processes talking to it over its socket.
func TestBinary(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds the lane binary")
	}
	bin := testkit.BuildBinary(t)
	home := gitfixture.TempDir(t)
	runtimeDir, err := os.MkdirTemp("", "laneway-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runtimeDir) })
	env := append(os.Environ(), "HOME="+home, "XDG_RUNTIME_DIR="+runtimeDir, "XDG_STATE_HOME=")
	socket := filepath.Join(runtimeDir, "laneway", "laneway.sock")
	repo := gitfixture.New(t, "shop")

	startDaemon := func() *exec.Cmd {
		t.Helper()
		cmd := exec.Command(bin, "daemon", "--https-addr", "127.0.0.1:0", "--http-addr", "127.0.0.1:0")
		cmd.Env = env
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
		waitForDaemon(t, socket)
		return cmd
	}
	stopDaemon := func(cmd *exec.Cmd) {
		t.Helper()
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatalf("daemon exited with %v after SIGTERM", err)
		}
		if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("socket still exists after shutdown: %v", err)
		}
	}
	lane := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("lane %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	daemon := startDaemon()
	assertContains(t, lane(repo.Dir, "link"), `Linked Project "shop"`)
	stopDaemon(daemon)

	startDaemon()
	assertStatusRows(t, lane(home, "status"), [][2]string{{"shop", repo.Dir}})
}

func waitForDaemon(t *testing.T, socket string) {
	t.Helper()
	c := client.New(socket)
	testkit.WaitUntil(t, 10*time.Second, "the daemon to answer on "+socket, func() bool {
		_, err := c.Projects(context.Background())
		return err == nil
	})
}
