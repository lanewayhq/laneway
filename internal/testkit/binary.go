package testkit

import (
	"os"
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
	return build(t, "./cmd/lane", "lane")
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
	bin := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", bin, pkg)
	cmd.Dir = moduleRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("testkit: building %s: %v\n%s", pkg, err, out)
	}
	return bin
}

// moduleRoot is the directory of Laneway's go.mod.
func moduleRoot(t testing.TB) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("testkit: finding module root: %v", err)
	}
	return filepath.Dir(strings.TrimSpace(string(out)))
}

// Plugin builds the test Plugin internal/testkit/testplugins/<name> and
// returns a directory holding it as its author would hand it over: its
// manifest (plugin.json) and its module (plugin.wasm), compiled for wasip1.
func Plugin(t testing.TB, name string) string {
	t.Helper()
	src := filepath.Join(moduleRoot(t), "internal", "testkit", "testplugins", name)
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("testkit: %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(src, "plugin.json"))
	if err != nil {
		t.Fatalf("testkit: test Plugin %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), manifest, 0o644); err != nil {
		t.Fatalf("testkit: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "plugin.wasm"), "./internal/testkit/testplugins/"+name)
	cmd.Dir = moduleRoot(t)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("testkit: building test Plugin %s: %v\n%s", name, err, out)
	}
	return dir
}
