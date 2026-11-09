package recipe_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/detect"
	"github.com/lanewayhq/laneway/internal/recipe"
)

// fixtureIntegration is a test Integration that detects every Project
// resolveIntegrations makes (by its fixture-stack file) and adds the layer in
// its TOML.
type fixtureIntegration struct{ name, toml string }

func (i fixtureIntegration) Name() string           { return i.name }
func (i fixtureIntegration) Detection() detect.Rule { return detect.Rule{File: "fixture-stack"} }

func (i fixtureIntegration) Defaults(dir string) (config.Layer, error) {
	return config.ParseLayer("the "+i.name+" Integration", []byte(i.toml))
}

// resolveIntegrations resolves a Project whose laneway.toml is committed
// ("" for none) with the fixture-stack Recipe and the given Integrations.
func resolveIntegrations(t *testing.T, committed string, integrations ...recipe.Integration) (recipe.Resolved, error) {
	t.Helper()
	files := map[string]string{"fixture-stack": ""}
	if committed != "" {
		files[config.FileName] = committed
	}
	dir := project(t, files)
	layers, err := config.LoadLayers(dir)
	if err != nil {
		t.Fatal(err)
	}
	return recipe.Resolve(recipe.Input{Engines: firstPartyEngines(), Dir: dir, Layers: layers, Recipes: []recipe.Recipe{fixtureStack{}}, Integrations: integrations})
}

func TestTwoIntegrationsSettingTheSameKeyIsAnErrorNamingBoth(t *testing.T) {
	for _, tc := range []struct{ key, a, b string }{
		{"env.NODE_ENV", "[env]\nNODE_ENV = \"development\"\n", "[env]\nNODE_ENV = \"test\"\n"},
		{"linked_env.PREFIX", "[linked_env]\nPREFIX = \"a\"\n", "[linked_env]\nPREFIX = \"b\"\n"},
		{"services.redis", "[services]\nredis = \"7\"\n", "[services]\nredis = \"6\"\n"},
		{"processes.assets.command", "[processes.assets]\ncommand = \"a\"\n", "[processes.assets]\ncommand = \"b\"\n"},
		{"processes.assets.http", "[processes.assets]\ncommand = \"a\"\nhttp = true\n", "[processes.assets]\nhttp = false\n"},
		{"processes.queue.disabled", "[processes.queue]\ndisabled = true\n", "[processes.queue]\ndisabled = false\n"},
		{"processes.queue.start_mode", "[processes.queue]\nstart_mode = \"always\"\n", "[processes.queue]\nstart_mode = \"manual\"\n"},
		{"processes.web.static_dir", "[processes.web]\nstatic_dir = \"public\"\n", "[processes.web]\nstatic_dir = \"dist\"\n"},
		{"database.engine", "[database]\nengine = \"postgres\"\n", "[database]\nengine = \"mysql\"\n"},
		{"database.worktree", "[database]\nworktree = \"fresh\"\n", "[database]\nworktree = \"shared\"\n"},
		{"start_timeout", "start_timeout = \"10s\"\n", "start_timeout = \"20s\"\n"},
		{"idle_timeout", "idle_timeout = \"10m\"\n", "idle_timeout = \"20m\"\n"},
		{"worktree.root", "[worktree]\nroot = \"../a\"\n", "[worktree]\nroot = \"../b\"\n"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			_, err := resolveIntegrations(t, "", fixtureIntegration{"beta", tc.b}, fixtureIntegration{"alpha", tc.a})

			if err == nil {
				t.Fatalf("resolved, want an error: two Integrations set %s", tc.key)
			}
			for _, want := range []string{"the alpha Integration", "the beta Integration", tc.key, config.FileName} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q doesn't mention %q", err, want)
				}
			}
		})
	}
}

func TestLanewayTomlSettlesTwoIntegrationsSettingTheSameKey(t *testing.T) {
	res, err := resolveIntegrations(t, "[env]\nNODE_ENV = \"production\"\n",
		fixtureIntegration{"alpha", "[env]\nNODE_ENV = \"development\"\n"},
		fixtureIntegration{"beta", "[env]\nNODE_ENV = \"test\"\n"})

	if err != nil {
		t.Fatal(err)
	}
	if got := res.Env["NODE_ENV"]; got != "production" {
		t.Errorf("NODE_ENV = %q, want laneway.toml's production", got)
	}
}

func TestIntegrationsSettingAKeyToTheSameValueDoNotConflict(t *testing.T) {
	res, err := resolveIntegrations(t, "",
		fixtureIntegration{"alpha", "[services]\nredis = \"7\"\n"},
		fixtureIntegration{"beta", "[services]\nredis = \"7\"\n"})

	if err != nil {
		t.Fatal(err)
	}
	if want := []recipe.Service{{Engine: "redis", Version: "7"}}; !reflect.DeepEqual(res.Services, want) {
		t.Errorf("Services = %+v, want %+v", res.Services, want)
	}
}

func TestIntegrationsLayerOverTheRecipeInNameOrderAndUnderTheProjectsConfig(t *testing.T) {
	res, err := resolveIntegrations(t, "[env]\nAPP_ENV = \"staging\"\n",
		fixtureIntegration{"zeta", "[env]\nAPP_URL = \"{url}/zeta\"\nAPP_ENV = \"zeta\"\n"},
		fixtureIntegration{"alpha", "[env]\nASSETS = \"alpha\"\n[processes.queue]\ncommand = \"alpha-worker\"\n"})

	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alpha", "zeta"}; !reflect.DeepEqual(res.Integrations, want) {
		t.Errorf("Integrations = %q, want %q", res.Integrations, want)
	}
	want := map[string]string{"APP_URL": "{url}/zeta", "APP_ENV": "staging", "ASSETS": "alpha"}
	if !reflect.DeepEqual(res.Env, want) {
		t.Errorf("Env = %v, want %v", res.Env, want)
	}
	if got := process(t, res, "queue").Command; got != "alpha-worker" {
		t.Errorf("queue's command = %q, want the alpha Integration's over the Recipe's", got)
	}
}

func TestAnIntegrationsSetupStepsRunAfterTheRecipesAndAreInheritedLikeThem(t *testing.T) {
	integration := fixtureIntegration{"alpha", "[worktree]\non_create = [\"alpha install\"]\non_remove = [\"alpha clean\"]\n"}

	res, err := resolveIntegrations(t, "[worktree]\non_create = [\"seed\"]\n", fixtureIntegration{"setup", "[worktree]\non_create = [\"setup install\"]\n"}, integration)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alpha install", "setup install", "seed"}; !reflect.DeepEqual(res.OnCreate, want) {
		t.Errorf("OnCreate = %q, want %q", res.OnCreate, want)
	}
	if want := []string{"alpha clean"}; !reflect.DeepEqual(res.OnRemove, want) {
		t.Errorf("OnRemove = %q, want %q", res.OnRemove, want)
	}

	res, err = resolveIntegrations(t, "[worktree]\ninherit_setup = false\non_create = [\"seed\"]\n", integration)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"seed"}; !reflect.DeepEqual(res.OnCreate, want) || res.OnRemove != nil {
		t.Errorf("with inherit_setup = false, OnCreate = %q and OnRemove = %q, want %q and none", res.OnCreate, res.OnRemove, want)
	}
}

func TestAnIntegrationSettingThePrimaryProcessIsAnError(t *testing.T) {
	for _, toml := range []string{
		"[processes.assets]\ncommand = \"serve-assets --port={port}\"\nprimary = true\n",
		"[processes.web]\nprimary = true\n",
		"[processes.web]\nprimary = false\n",
	} {
		t.Run(toml, func(t *testing.T) {
			_, err := resolveIntegrations(t, "", fixtureIntegration{"assets", toml})

			if err == nil {
				t.Fatal("resolved, want an error: only the Recipe chooses the Primary Process")
			}
			for _, want := range []string{"the assets Integration", "primary", "Primary Process"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q doesn't mention %q", err, want)
				}
			}
		})
	}
}

func TestTheProjectsConfigMayMakeAnIntegrationsProcessThePrimaryProcess(t *testing.T) {
	res, err := resolveIntegrations(t, "[processes.assets]\nprimary = true\n",
		fixtureIntegration{"assets", "[processes.assets]\ncommand = \"serve-assets --port={port}\"\nhttp = true\n"})

	if err != nil {
		t.Fatal(err)
	}
	if !process(t, res, "assets").Primary || process(t, res, "web").Primary {
		t.Errorf("Processes = %+v, want assets the Primary Process", res.Processes)
	}
}
