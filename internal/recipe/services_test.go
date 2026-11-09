package recipe_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/detect"
	"github.com/lanewayhq/laneway/internal/recipe"
)

// servicesStack is a test Recipe, detected by a `services-stack` file, that
// uses Redis at the default version.
type servicesStack struct{}

func (servicesStack) Name() string { return "services-stack" }

func (servicesStack) Detection() detect.Rule { return detect.Rule{File: "services-stack"} }

func (servicesStack) Defaults(dir string) (recipe.Result, error) {
	return layered(config.ParseLayer("the services-stack Recipe", []byte("[services]\nredis = \"7\"\n")))
}

func TestServicesMergeByEngineLaterLayersPinningTheVersion(t *testing.T) {
	recipeOnly, err := recipe.Resolve(recipe.Input{Engines: firstPartyEngines(), Dir: filepath.Join("testdata", "services-layers"), Recipes: []recipe.Recipe{servicesStack{}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []recipe.Service{{Engine: "redis", Version: "7"}}; !reflect.DeepEqual(recipeOnly.Services, want) {
		t.Errorf("the Recipe's Services = %+v, want %+v", recipeOnly.Services, want)
	}

	res := mustResolveFixture(t, "services-layers", servicesStack{})
	if want := []recipe.Service{{Engine: "redis", Version: "7.4"}}; !reflect.DeepEqual(res.Services, want) {
		t.Errorf("Services = %+v, want laneway.local.toml's %+v", res.Services, want)
	}
}

func TestAProjectWithoutServicesUsesNone(t *testing.T) {
	if res := mustResolveFixture(t, "committed-only"); res.Services != nil {
		t.Errorf("Services = %+v, want none", res.Services)
	}
}

func TestServicesConfigWrittenAsALayerResolvesToItself(t *testing.T) {
	res := mustResolveFixture(t, "services-layers", servicesStack{})
	var buf strings.Builder
	if err := config.Encode(&buf, res.Layer()); err != nil {
		t.Fatal(err)
	}
	written, err := config.ParseLayer("written", []byte(buf.String()))
	if err != nil {
		t.Fatalf("reading back:\n%s\n%v", buf.String(), err)
	}
	again, err := recipe.Resolve(recipe.Input{Engines: firstPartyEngines(), Dir: filepath.Join("testdata", "services-layers"),
		Layers: []config.Layer{written}, Recipes: []recipe.Recipe{servicesStack{}}})
	if err != nil {
		t.Fatal(err)
	}
	again.Warnings = res.Warnings
	if !reflect.DeepEqual(again, res) {
		t.Errorf("the written config\n%s\nresolves to\n  %+v\nwant\n  %+v", buf.String(), again, res)
	}
}

func TestInvalidServicesConfigNamesTheFileAndTheKey(t *testing.T) {
	for _, tc := range []struct{ toml, want string }{
		{"[services]\nmemcached = \"1.6\"", `services.memcached: Laneway has no Service "memcached"; the Services are mailpit, mysql, postgres, redis`},
		{"[services]\nredis = \"\"", `services.redis must be a version, such as "7" or "7.2", not ""`},
		{"[services]\nredis = \"7 latest\"", `services.redis must be a version`},
		{"[services]\nredis = 7", "services.redis"},
		{`services = "redis"`, "services must be a table"},
	} {
		// Whether a Service's Engine exists is checked once the Engines
		// are known, when the layer is resolved.
		l, err := config.ParseLayer("/code/shop/laneway.toml", []byte(tc.toml))
		if err == nil {
			_, err = recipe.Resolve(recipe.Input{Engines: firstPartyEngines(), Dir: t.TempDir(), Layers: []config.Layer{l}})
		}
		if err == nil || !strings.Contains(err.Error(), "/code/shop/laneway.toml") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one naming the file and %q", tc.toml, err, tc.want)
		}
	}
}
