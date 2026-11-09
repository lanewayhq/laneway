package testkit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

// Plugin returns a directory holding the test Plugin
// internal/testkit/testplugins/<name> as its author would hand it over: its
// manifest (plugin.json) and any other files beside it, such as Engine
// manifests, and, if it has Go source, its module (plugin.wasm), compiled
// for wasip1. The directory is the test's own, so it may change the
// manifest. The module is built once per test binary.
func Plugin(t testing.TB, name string) string {
	t.Helper()
	src := filepath.Join(moduleRoot(t), "internal", "testkit", "testplugins", name)
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("testkit: test Plugin %s: %v", name, err)
	}
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("testkit: %v", err)
	}
	hasCode := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".go") {
			hasCode = true
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatalf("testkit: test Plugin %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o644); err != nil {
			t.Fatalf("testkit: %v", err)
		}
	}
	if !hasCode {
		return dir
	}
	build, _ := pluginBuilds.LoadOrStore(name, sync.OnceValues(func() ([]byte, error) {
		return buildPlugin(moduleRoot(t), name)
	}))
	module, err := build.(func() ([]byte, error))()
	if err != nil {
		t.Fatalf("testkit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), module, 0o644); err != nil {
		t.Fatalf("testkit: %v", err)
	}
	return dir
}

// pluginBuilds are the test Plugins' modules built so far, by name.
var pluginBuilds sync.Map // name → func() ([]byte, error)

// cacheHome is the XDG_CACHE_HOME of every test daemon and CLI command: one
// directory shared by every test run, like Go's build cache, so that they
// share compiled Plugin modules (paths.PluginCache), since compiling a module
// takes about a second and it's keyed by the module's content.
var cacheHome = filepath.Join(os.TempDir(), "laneway-test-cache")

// pluginCacheDir is where test daemons keep compiled modules, as their CLI
// commands do.
var pluginCacheDir = filepath.Join(cacheHome, "laneway", "plugins")

// buildPlugin compiles the test Plugin name for wasip1 and returns its
// module.
func buildPlugin(root, name string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "laneway-test-plugin-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "plugin.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./internal/testkit/testplugins/"+name)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("building test Plugin %s: %v\n%s", name, err, output)
	}
	return os.ReadFile(out)
}
