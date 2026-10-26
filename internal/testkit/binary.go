package testkit

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// BuildBinary compiles cmd/portby into a temporary directory and returns the
// binary's path, for tests that need a real `portby` process (or one on PATH).
// Go's build cache keeps repeat builds cheap.
func BuildBinary(t testing.TB) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("testkit: finding module root: %v", err)
	}
	root := filepath.Dir(strings.TrimSpace(string(out)))
	bin := filepath.Join(t.TempDir(), "portby")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/portby")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("testkit: building portby: %v\n%s", err, out)
	}
	return bin
}
