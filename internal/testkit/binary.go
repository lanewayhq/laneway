package testkit

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// BuildBinary compiles cmd/lane into a temporary directory and returns the
// binary's path, for tests that need a real `lane` process (or one on PATH).
// Go's build cache keeps repeat builds cheap.
func BuildBinary(t testing.TB) string {
	t.Helper()
	return build(t, "./cmd/lane", "laneway")
}

// TestServer compiles the test HTTP-server Process (package testserver) and
// returns the binary's path, for use in a Process command in laneway.toml. The
// server listens on --port, or on $PORT without it, answers every request
// with a JSON testserver.Response and echoes bytes on a WebSocket at /ws.
func TestServer(t testing.TB) string {
	t.Helper()
	return build(t, "./internal/testkit/testserver", "testserver")
}

func build(t testing.TB, pkg, name string) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("testkit: finding module root: %v", err)
	}
	root := filepath.Dir(strings.TrimSpace(string(out)))
	bin := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", bin, pkg)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("testkit: building %s: %v\n%s", pkg, err, out)
	}
	return bin
}
