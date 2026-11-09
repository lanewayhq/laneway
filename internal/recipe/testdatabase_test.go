package recipe_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/detect"
	"github.com/lanewayhq/laneway/internal/recipe"
)

func TestATestDatabaseIsMadeOnlyWhenDatabaseTestIsOn(t *testing.T) {
	tests := []struct {
		fixture string
		want    bool
	}{
		{"database-test-on", true},
		{"database-test-off", false},
		// Without a Recipe that decides it (the Laravel Recipe does), auto is off.
		{"database-test-auto", false},
		{"database-postgres", false},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			if got := mustResolveFixture(t, tt.fixture).TestDatabase; got != tt.want {
				t.Errorf("TestDatabase = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestInvalidTestDatabaseConfigNamesTheFileAndTheKey(t *testing.T) {
	tests := []struct {
		fixture string
		want    []string // substrings of the error, besides the file
	}{
		{"invalid-database-test", []string{"database.test", "on, off or auto", `"yes"`}},
		{"invalid-database-test-sqlite", []string{"database.test", "only for databases in a Service", "sqlite"}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			_, err := resolveFixture(t, tt.fixture)
			if err == nil {
				t.Fatal("resolved without an error")
			}
			msg := err.Error()
			for _, want := range append([]string{filepath.Join("testdata", tt.fixture, config.FileName)}, tt.want...) {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q doesn't mention %q", msg, want)
				}
			}
		})
	}
}

func TestTheTestDatabaseSettingWrittenAsALayerResolvesToItself(t *testing.T) {
	for _, fixture := range []string{"database-test-on", "database-test-off", "database-test-auto"} {
		t.Run(fixture, func(t *testing.T) {
			res := mustResolveFixture(t, fixture)

			var buf strings.Builder
			if err := config.Encode(&buf, res.Layer()); err != nil {
				t.Fatal(err)
			}
			written, err := config.ParseLayer("written", []byte(buf.String()))
			if err != nil {
				t.Fatalf("reading back:\n%s\n%v", buf.String(), err)
			}
			again, err := recipe.Resolve(recipe.Input{Engines: firstPartyEngines(), Layers: []config.Layer{written}})
			if err != nil {
				t.Fatalf("resolving what was written:\n%s\n%v", buf.String(), err)
			}
			if again.Database != res.Database || again.TestDatabase != res.TestDatabase {
				t.Errorf("written as:\n%s\nresolves to %+v %t, want %+v %t", buf.String(),
					again.Database, again.TestDatabase, res.Database, res.TestDatabase)
			}
		})
	}
}

// testedStack is a test Recipe, detected by a `tested-stack` file, whose
// Projects keep a Postgres database and whose answer decides the Test
// Database as given.
type testedStack struct{ answer recipe.Result }

func (testedStack) Name() string { return "tested-stack" }

func (testedStack) Detection() detect.Rule { return detect.Rule{File: "tested-stack"} }

func (s testedStack) Defaults(string) (recipe.Result, error) {
	res := s.answer
	res.Layer = config.Layer{Source: "the tested-stack Recipe", Database: &config.DatabaseLayer{Engine: ptr("postgres")}}
	return res, nil
}

func ptr[T any](v T) *T { return &v }

func TestTheRecipesAnswerDecidesTheTestDatabaseItsEnvFileAndItsWarnings(t *testing.T) {
	dir := project(t, map[string]string{"tested-stack": ""})
	answer := recipe.Result{
		TestDatabase:         true,
		TestEnv:              recipe.TestEnv{File: ".env.test", Layered: true},
		TestDatabaseWarnings: []string{"spec/config.rb sets the database"},
	}

	res, err := recipe.Resolve(recipe.Input{Engines: firstPartyEngines(), Dir: dir, Recipes: []recipe.Recipe{testedStack{answer}}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TestDatabase || res.TestEnv != answer.TestEnv || !slices.Equal(res.TestDatabaseWarnings, answer.TestDatabaseWarnings) {
		t.Errorf("Test Database %t in %+v with warnings %q, want the Recipe's answer %+v", res.TestDatabase, res.TestEnv, res.TestDatabaseWarnings, answer)
	}
}

func TestARecipeNamingNoTestEnvFileGetsTheDefaultOne(t *testing.T) {
	dir := project(t, map[string]string{"tested-stack": ""})

	res, err := recipe.Resolve(recipe.Input{Engines: firstPartyEngines(), Dir: dir, Recipes: []recipe.Recipe{testedStack{recipe.Result{TestDatabase: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TestDatabase || res.TestEnv != recipe.DefaultTestEnv {
		t.Errorf("Test Database %t in %+v, want one in %+v", res.TestDatabase, res.TestEnv, recipe.DefaultTestEnv)
	}
}

func TestDatabaseTestOverridesTheRecipesAnswer(t *testing.T) {
	answer := recipe.Result{TestDatabase: true, TestDatabaseWarnings: []string{"phpunit.xml sets DB_DATABASE"}}
	for _, test := range []string{"off", "on"} {
		t.Run(test, func(t *testing.T) {
			dir := project(t, map[string]string{"tested-stack": "", config.FileName: "[database]\ntest = \"" + test + "\"\n"})
			layers, err := config.LoadLayers(dir)
			if err != nil {
				t.Fatal(err)
			}
			stack := testedStack{answer}
			if test == "on" {
				stack = testedStack{recipe.Result{}}
			}

			res, err := recipe.Resolve(recipe.Input{Engines: firstPartyEngines(), Dir: dir, Layers: layers, Recipes: []recipe.Recipe{stack}})
			if err != nil {
				t.Fatal(err)
			}
			if want := test == "on"; res.TestDatabase != want || (!want && res.TestDatabaseWarnings != nil) {
				t.Errorf("with database.test = %s: Test Database %t with warnings %q, want %t", test, res.TestDatabase, res.TestDatabaseWarnings, want)
			}
		})
	}
}
