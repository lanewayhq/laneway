package recipe_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/config"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
)

// resolveFixture loads testdata/<name>'s config layers and resolves them
// with the given Recipes.
func resolveFixture(t *testing.T, name string, recipes ...recipe.Recipe) (recipe.Resolved, error) {
	t.Helper()
	dir := filepath.Join("testdata", name)
	layers, err := config.LoadLayers(dir)
	if err != nil {
		return recipe.Resolved{}, err
	}
	return recipe.Resolve(recipe.Input{Dir: dir, Layers: layers, Recipes: recipes})
}

// fixtureStack is a test Recipe, detected by a `fixture-stack` file, that
// runs a web server, a queue worker and a scheduler.
type fixtureStack struct{}

func (fixtureStack) Name() string { return "fixture-stack" }

func (fixtureStack) Detect(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "fixture-stack"))
	return err == nil
}

func (fixtureStack) Defaults(dir string) (config.Layer, error) {
	return config.ParseLayer("the fixture-stack Recipe", []byte(`
[processes.web]
command = "serve --port={port}"
primary = true

[processes.queue]
command = "worker"

[processes.scheduler]
command = "scheduler"

[env]
APP_URL = "{url}"
APP_ENV = "local"
`))
}

func mustResolveFixture(t *testing.T, name string, recipes ...recipe.Recipe) recipe.Resolved {
	t.Helper()
	res, err := resolveFixture(t, name, recipes...)
	if err != nil {
		t.Fatalf("resolving %s: %v", name, err)
	}
	return res
}

func TestCommittedConfigAloneDeclaresProcesses(t *testing.T) {
	res := mustResolveFixture(t, "committed-only")

	if res.Name != "shop" {
		t.Errorf("Name = %q, want shop", res.Name)
	}
	want := []recipe.Process{
		{Name: "queue", Command: "php artisan queue:work"},
		{Name: "web", Command: "php -S 127.0.0.1:{port}", Primary: true},
	}
	if !reflect.DeepEqual(res.Processes, want) {
		t.Errorf("Processes =\n  %+v\nwant\n  %+v", res.Processes, want)
	}
}

func TestLocalConfigOverridesCommittedConfigByProcessName(t *testing.T) {
	res := mustResolveFixture(t, "local-overrides")

	if res.Name != "my-shop" {
		t.Errorf("Name = %q, want my-shop", res.Name)
	}
	want := []recipe.Process{
		{Name: "horizon", Command: "php artisan horizon"},
		{Name: "queue", Command: "php artisan queue:work"},
		{Name: "web", Command: "php -d xdebug.mode=debug -S 127.0.0.1:{port}", Primary: true},
	}
	if !reflect.DeepEqual(res.Processes, want) {
		t.Errorf("Processes =\n  %+v\nwant\n  %+v", res.Processes, want)
	}
}

func TestConfigLayersOverRecipeDefaultsAndCanDisableTheirProcesses(t *testing.T) {
	res := mustResolveFixture(t, "recipe-defaults", fixtureStack{})

	if res.Recipe != "fixture-stack" {
		t.Errorf("Recipe = %q, want fixture-stack", res.Recipe)
	}
	want := []recipe.Process{
		{Name: "queue", Command: "worker --tries=3"},
		{Name: "web", Command: "serve --port={port}", Primary: true},
	}
	if !reflect.DeepEqual(res.Processes, want) {
		t.Errorf("Processes =\n  %+v\nwant\n  %+v", res.Processes, want)
	}
	if want := []string{"scheduler"}; !reflect.DeepEqual(res.Disabled, want) {
		t.Errorf("Disabled = %q, want %q", res.Disabled, want)
	}
}

func TestLocalConfigCanReenableAProcessTheCommittedConfigDisabled(t *testing.T) {
	res := mustResolveFixture(t, "recipe-reenabled", fixtureStack{})

	want := []recipe.Process{
		{Name: "queue", Command: "worker"},
		{Name: "scheduler", Command: "scheduler"},
		{Name: "web", Command: "serve --port={port}", Primary: true},
	}
	if !reflect.DeepEqual(res.Processes, want) {
		t.Errorf("Processes =\n  %+v\nwant\n  %+v", res.Processes, want)
	}
	if len(res.Disabled) != 0 {
		t.Errorf("Disabled = %q, want none", res.Disabled)
	}
}

func TestARecipeThatDoesNotDetectTheProjectContributesNothing(t *testing.T) {
	res := mustResolveFixture(t, "no-recipe", fixtureStack{})

	if res.Recipe != "" {
		t.Errorf("Recipe = %q, want none", res.Recipe)
	}
	want := []recipe.Process{{Name: "web", Command: "serve", Primary: true}}
	if !reflect.DeepEqual(res.Processes, want) {
		t.Errorf("Processes =\n  %+v\nwant\n  %+v", res.Processes, want)
	}
}

func TestALaterLayerCanChooseADifferentPrimaryProcess(t *testing.T) {
	res := mustResolveFixture(t, "primary-moved", fixtureStack{})

	want := []recipe.Process{
		{Name: "octane", Command: "php artisan octane:start --port={port}", Primary: true},
		{Name: "queue", Command: "worker"},
		{Name: "scheduler", Command: "scheduler"},
		{Name: "web", Command: "serve --port={port}"},
	}
	if !reflect.DeepEqual(res.Processes, want) {
		t.Errorf("Processes =\n  %+v\nwant\n  %+v", res.Processes, want)
	}
}

func TestEnvPatchTemplatesMergeByKeyOverTheRecipesDefaults(t *testing.T) {
	res := mustResolveFixture(t, "env-layers", fixtureStack{})

	want := map[string]string{
		"APP_ENV":        "local",
		"APP_URL":        "{url}/app",
		"SESSION_DOMAIN": "{domain}",
		"LOG_CHANNEL":    "stderr",
		"CACHE_PREFIX":   "{{literal}} {project}_{worktree}_",
	}
	if !reflect.DeepEqual(res.Env, want) {
		t.Errorf("Env =\n  %q\nwant\n  %q", res.Env, want)
	}
}

func TestAProjectWithoutEnvTemplatesHasNoEnvPatch(t *testing.T) {
	if res := mustResolveFixture(t, "committed-only"); res.Env != nil {
		t.Errorf("Env = %q, want none", res.Env)
	}
}

func TestInvalidConfigNamesTheFileAndTheKey(t *testing.T) {
	tests := []struct {
		fixture string
		file    string
		want    []string // substrings of the error, besides the file
	}{
		{"invalid-syntax", config.LocalFileName, []string{"line 2"}},
		{"invalid-type", config.FileName, []string{"processes.web.command", "string"}},
		{"invalid-empty-command", config.FileName, []string{"processes.web.command", "empty"}},
		{"invalid-no-command", config.LocalFileName, []string{"processes.wbe", "no command"}},
		{"invalid-two-primaries", config.FileName, []string{"processes.api.primary", "processes.web.primary"}},
		{"invalid-processes-not-table", config.FileName, []string{"processes", "table"}},
		{"invalid-process-not-table", config.FileName, []string{"processes.web", "table"}},
		{"invalid-empty-name", config.FileName, []string{"name", "empty"}},
		{"invalid-env-variable", config.FileName, []string{"env.APP_URL", "{domian}", "{domain}"}},
		{"invalid-env-key", config.FileName, []string{`env."APP URL"`, "letters, digits"}},
		{"invalid-env-not-table", config.FileName, []string{"env", "table"}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			_, err := resolveFixture(t, tt.fixture, fixtureStack{})
			if err == nil {
				t.Fatal("resolved without an error")
			}
			msg := err.Error()
			for _, want := range append([]string{filepath.Join("testdata", tt.fixture, tt.file)}, tt.want...) {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q doesn't mention %q", msg, want)
				}
			}
		})
	}
}

func TestUnknownKeysAreWarningsNamingTheFileAndTheKey(t *testing.T) {
	res := mustResolveFixture(t, "unknown-keys")

	file := filepath.Join("testdata", "unknown-keys", config.FileName)
	want := []string{ // in file order
		file + `: unknown key "processes.web.start_mod"`,
		file + `: unknown key "future"`,
	}
	if !reflect.DeepEqual(res.Warnings, want) {
		t.Errorf("Warnings =\n  %q\nwant\n  %q", res.Warnings, want)
	}
	if want := []recipe.Process{{Name: "web", Command: "serve", Primary: true}}; !reflect.DeepEqual(res.Processes, want) {
		t.Errorf("Processes = %+v, want %+v", res.Processes, want)
	}
}

func TestResolveDirReadsTheProjectsConfigFiles(t *testing.T) {
	res, err := recipe.ResolveDir(filepath.Join("testdata", "local-overrides"))
	if err != nil {
		t.Fatal(err)
	}
	if want := mustResolveFixture(t, "local-overrides"); !reflect.DeepEqual(res, want) {
		t.Errorf("ResolveDir =\n  %+v\nwant\n  %+v", res, want)
	}
}

func TestAProjectWithNoConfigAndNoRecipeResolvesToNothing(t *testing.T) {
	res, err := recipe.ResolveDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res, recipe.Resolved{}) {
		t.Errorf("ResolveDir = %+v, want nothing", res)
	}
}

func TestResolveDirReportsInvalidConfig(t *testing.T) {
	_, err := recipe.ResolveDir(filepath.Join("testdata", "invalid-no-command"))
	if err == nil || !strings.Contains(err.Error(), "processes.wbe") {
		t.Errorf("err = %v, want one naming processes.wbe", err)
	}
}

// What `portby init` writes must mean the same thing as the config it was
// resolved from, whether or not the Recipe still detects the Project.
func TestTheResolvedConfigWrittenAsALayerResolvesToItself(t *testing.T) {
	for _, fixture := range []string{"committed-only", "local-overrides", "recipe-defaults", "recipe-reenabled", "primary-moved", "env-layers"} {
		t.Run(fixture, func(t *testing.T) {
			res := mustResolveFixture(t, fixture, fixtureStack{})

			var buf strings.Builder
			if err := config.Encode(&buf, res.Layer()); err != nil {
				t.Fatal(err)
			}
			written, err := config.ParseLayer("written", []byte(buf.String()))
			if err != nil {
				t.Fatalf("reading back:\n%s\n%v", buf.String(), err)
			}
			dir := filepath.Join("testdata", fixture)
			for _, recipes := range [][]recipe.Recipe{nil, {fixtureStack{}}} {
				again, err := recipe.Resolve(recipe.Input{Dir: dir, Layers: []config.Layer{written}, Recipes: recipes})
				if err != nil {
					t.Fatal(err)
				}
				again.Recipe, again.Warnings = res.Recipe, res.Warnings
				if !reflect.DeepEqual(again, res) {
					t.Errorf("with Recipes %v, the written config\n%s\nresolves to\n  %+v\nwant\n  %+v", recipes, buf.String(), again, res)
				}
			}
		})
	}
}
