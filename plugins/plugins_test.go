package plugins_test

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lanewayhq/laneway/internal/detect"
	"github.com/lanewayhq/laneway/internal/plugin"
	"github.com/lanewayhq/laneway/internal/recipe"
	_ "github.com/lanewayhq/laneway/sdk" // the Plugins' source, see TestTheEmbeddedModulesAreWhatTheirSourceBuildsTo
)

// The built-in Plugins' Recipes and Integration, resolved the way the
// daemon resolves a Project: through a Plugin host, which runs their
// embedded modules. Each test case is a Project directory, from testdata or
// made by the test.

// host runs the built-in Plugins, with no Plugin installed.
var host *plugin.Host

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "laneway-plugins-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	// Shared with testkit's daemons (see testkit.cacheHome), since
	// compiling a module takes about a second.
	cache := filepath.Join(os.TempDir(), "laneway-test-cache", "laneway", "plugins")
	host, err = plugin.Open(plugin.Options{Dir: filepath.Join(dir, "plugins"), CacheDir: cache})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer host.Close()
	return m.Run()
}

// resolve resolves the Project in dir the way the daemon does.
func resolve(dir string) (recipe.Resolved, error) {
	return host.ResolveDir(dir)
}

// project is a temp Project with the given files (path → content).
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestTheBuiltInRecipesAndIntegrationsDetectByRulesThatAreJSONData(t *testing.T) {
	want := map[string]string{
		"laravel": `{"file":"artisan"}`,
		"rails":   `{"all":[{"file":"config/application.rb"},{"any":[{"gem":"rails"},{"gem":"railties"}]}]}`,
		"vite":    `{"any":[{"package":"vite"},{"package":"laravel-vite-plugin"}]}`,
	}
	rules := map[string]detect.Rule{}
	for _, r := range host.Recipes() {
		rules[r.Name()] = r.Detection()
	}
	for _, in := range host.Integrations() {
		rules[in.Name()] = in.Detection()
	}
	for name, rule := range rules {
		var read detect.Rule
		if err := json.Unmarshal([]byte(want[name]), &read); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(read, rule) {
			t.Errorf("%s's rule is %+v, want %s", name, rule, want[name])
		}
	}
	if len(rules) != len(want) {
		t.Errorf("built-in rules for %d Recipes and Integrations, want %d", len(rules), len(want))
	}
}

func TestARailsAppDeclaringOnlyRailtiesIsARailsProject(t *testing.T) {
	dir := project(t, map[string]string{
		"config/application.rb": "module Shop\n  class Application < Rails::Application\n  end\nend\n",
		"Gemfile":               "gem \"railties\"\ngem \"activerecord\"\n",
	})
	res, err := resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Recipe != "rails" || res.RecipeRule != "config/application.rb exists and Gemfile declares railties" {
		t.Errorf("Recipe %q matched by %q, want rails matched by its config/application.rb and railties", res.Recipe, res.RecipeRule)
	}
}

func TestTheEmbeddedModulesAreWhatTheirSourceBuildsTo(t *testing.T) {
	if testing.Short() {
		t.Skip("builds every built-in Plugin")
	}
	// go test reuses a passing result until a file the test opened changes:
	// open the Plugins' source (the SDK they build with is imported), so
	// that a changed Plugin runs the check again.
	if err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".go" {
			return err
		}
		_, err = os.ReadFile(path)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"../go.mod", "../go.sum"} {
		if _, err := os.ReadFile(file); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "run", "generate.go", "-check")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
