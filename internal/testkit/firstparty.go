package testkit

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lanewayhq/laneway/internal/engine"
)

// The first-party Plugins (Laravel, Rails, Vite, the Engines, the Harness
// Generators) live in lanewayhq/plugins, not in Laneway. Tests get them from
// that repository's Go module, at the version go.mod pins: testkit builds
// each Plugin's module from its source for wasip1, as a release of it holds
// it, and test daemons fetch them from a test Catalog listing them all (see
// FirstPartyCatalog), as they would from lanewayhq's. Bumping the pin (`go
// get github.com/lanewayhq/plugins@<commit or tag>`) is how Laneway's tests
// pick up a change to a first-party Plugin.

// firstPartyModule is the Go module the first-party Plugins are in.
const firstPartyModule = "github.com/lanewayhq/plugins"

// firstPartySource is where the pinned first-party module is: its directory
// in Go's module cache, read-only, and its version. `go mod download -json`
// downloads it first if it isn't there yet, as on a fresh machine or CI
// runner, where nothing else may have needed it.
var firstPartySource = sync.OnceValues(func() (struct{ dir, version string }, error) {
	var src struct{ dir, version string }
	gomod, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return src, fmt.Errorf("finding Laneway's module: %w", err)
	}
	cmd := exec.Command("go", "mod", "download", "-json", firstPartyModule)
	cmd.Dir = filepath.Dir(strings.TrimSpace(string(gomod)))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return src, fmt.Errorf("downloading %s, which go.mod pins: %v\n%s%s", firstPartyModule, err, out, stderr.String())
	}
	var mod struct {
		Dir, Version, Error string
	}
	if err := json.Unmarshal(out, &mod); err != nil {
		return src, fmt.Errorf("reading `go mod download -json %s`: %w", firstPartyModule, err)
	}
	if mod.Error != "" || mod.Dir == "" {
		return src, fmt.Errorf("downloading %s, which go.mod pins: %s", firstPartyModule, mod.Error)
	}
	src.dir, src.version = mod.Dir, mod.Version
	return src, nil
})

// FirstPartyPlugins are the names of the first-party Plugins, sorted.
func FirstPartyPlugins(t testing.TB) []string {
	t.Helper()
	src, err := firstPartySource()
	if err != nil {
		t.Fatalf("testkit: %v", err)
	}
	names, err := firstPartyNames(src.dir)
	if err != nil {
		t.Fatalf("testkit: %v", err)
	}
	return names
}

func firstPartyNames(dir string) ([]string, error) {
	manifests, err := filepath.Glob(filepath.Join(dir, "plugins", "*", "plugin.json"))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, m := range manifests {
		names = append(names, filepath.Base(filepath.Dir(m)))
	}
	if len(names) == 0 {
		// An empty test Catalog would make every test needing a first-party
		// Plugin fail far from here, with a confusing config error.
		return nil, fmt.Errorf("no first-party Plugins (plugins/*/plugin.json) in %s", dir)
	}
	slices.Sort(names)
	return names, nil
}

// FirstPartyPlugin returns a directory holding the first-party Plugin name
// as its release does: its manifest, its module (plugin.wasm) if it has
// source, and its Engine manifests. The directory is the test's own, so it
// may change the manifest.
func FirstPartyPlugin(t testing.TB, name string) string {
	t.Helper()
	files, err := firstPartyFiles(name)
	if err != nil {
		t.Fatalf("testkit: first-party Plugin %s: %v", name, err)
	}
	dir := filepath.Join(t.TempDir(), name)
	if err := writeFiles(dir, files); err != nil {
		t.Fatalf("testkit: %v", err)
	}
	return dir
}

// writeFiles writes files (path → content) into dir.
func writeFiles(dir string, files map[string][]byte) error {
	for name, data := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// firstPartyBuilds are the first-party Plugins' release files read so far,
// by name.
var firstPartyBuilds sync.Map // name → func() (map[string][]byte, error)

// firstPartyFiles are the files of the first-party Plugin name's release,
// by path, read once per test binary.
func firstPartyFiles(name string) (map[string][]byte, error) {
	read, _ := firstPartyBuilds.LoadOrStore(name, sync.OnceValues(func() (map[string][]byte, error) {
		src, err := firstPartySource()
		if err != nil {
			return nil, err
		}
		return readFirstParty(src.dir, src.version, name)
	}))
	return read.(func() (map[string][]byte, error))()
}

// readFirstParty reads the release files of the Plugin name from the
// first-party module in dir, at version, building its module if it has
// source.
func readFirstParty(dir, version, name string) (map[string][]byte, error) {
	src := filepath.Join(dir, "plugins", name)
	manifest, err := os.ReadFile(filepath.Join(src, "plugin.json"))
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{"plugin.json": manifest}
	engines, err := firstPartyEngineFiles(dir, name)
	if err != nil {
		return nil, err
	}
	for _, f := range engines {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(src, f)
		if err != nil {
			return nil, err
		}
		files[filepath.ToSlash(rel)] = data
	}
	if _, err := os.Stat(filepath.Join(src, "main.go")); err != nil {
		return files, nil // Engines only
	}
	module, err := buildFirstParty(dir, version, name)
	if err != nil {
		return nil, err
	}
	files["plugin.wasm"] = module
	return files, nil
}

// buildFirstParty compiles the first-party Plugin name for wasip1 in the
// module in dir, at version, with the module's own go.mod. A build is kept
// in the shared test cache (cacheHome), keyed by the module's version and
// the Go toolchain, since a pinned version's source never changes.
func buildFirstParty(dir, version, name string) ([]byte, error) {
	cached := filepath.Join(cacheHome, "first-party", version+"-"+runtime.Version(), name+".wasm")
	if module, err := os.ReadFile(cached); err == nil {
		return module, nil
	}
	if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cached), "."+name+"-*.wasm")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	cmd := exec.Command("go", "build", "-trimpath", "-o", tmp.Name(), "./plugins/"+name)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "GOFLAGS=-mod=readonly")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("building first-party Plugin %s from %s: %v\n%s", name, dir, err, out)
	}
	if err := os.Rename(tmp.Name(), cached); err != nil {
		return nil, err
	}
	return os.ReadFile(cached)
}

// PublishFirstParty adds every first-party Plugin to the Catalog, as
// lanewayhq's Catalog lists them.
func (c *Catalog) PublishFirstParty() {
	c.t.Helper()
	for _, name := range FirstPartyPlugins(c.t) {
		if err := c.publishFirstParty(name); err != nil {
			c.t.Fatalf("testkit: publishing first-party Plugin %s: %v", name, err)
		}
	}
}

// FirstPartyEngines are the Engines the first-party Plugins add (Redis,
// Mailpit, Postgres, MySQL), read from their manifests, for package tests
// that resolve config naming them without a daemon to fetch them.
func FirstPartyEngines() (engine.Set, error) {
	src, err := firstPartySource()
	if err != nil {
		return nil, err
	}
	names, err := firstPartyNames(src.dir)
	if err != nil {
		return nil, err
	}
	var set engine.Set
	for _, name := range names {
		files, err := firstPartyEngineFiles(src.dir, name)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			e, err := engine.Parse(f, data)
			if err != nil {
				return nil, err
			}
			set = append(set, e)
		}
	}
	return set, nil
}

// firstPartyEngineFiles are the paths of the Engine manifests of the
// first-party Plugin name, in the module in dir.
func firstPartyEngineFiles(dir, name string) ([]string, error) {
	src := filepath.Join(dir, "plugins", name)
	data, err := os.ReadFile(filepath.Join(src, "plugin.json"))
	if err != nil {
		return nil, err
	}
	var m struct {
		Engines []string `json:"engines"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(src, "plugin.json"), err)
	}
	var files []string
	for _, f := range m.Engines {
		files = append(files, filepath.Join(src, filepath.FromSlash(f)))
	}
	return files, nil
}
