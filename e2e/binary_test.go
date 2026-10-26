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

	"github.com/mrsafalpiya/portby.dev/internal/client"
	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// TestBinary drives the compiled `portby` binary end to end: the daemon as a
// subprocess, and the CLI as separate processes talking to it over its socket.
func TestBinary(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds the portby binary")
	}
	bin := testkit.BuildBinary(t)
	home := gitfixture.TempDir(t)
	runtimeDir, err := os.MkdirTemp("", "portby-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runtimeDir) })
	env := append(os.Environ(), "HOME="+home, "XDG_RUNTIME_DIR="+runtimeDir, "XDG_STATE_HOME=")
	socket := filepath.Join(runtimeDir, "portby", "portby.sock")
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
	portby := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("portby %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	daemon := startDaemon()
	assertContains(t, portby(repo.Dir, "link"), `Linked Project "shop"`)
	stopDaemon(daemon)

	startDaemon()
	assertStatusRows(t, portby(home, "status"), [][2]string{{"shop", repo.Dir}})
}

func waitForDaemon(t *testing.T, socket string) {
	t.Helper()
	c := client.New(socket)
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := c.Projects(context.Background())
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not come up on %s: %v", socket, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
