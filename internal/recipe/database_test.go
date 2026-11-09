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

// databaseStack is a test Recipe, detected by a `database-stack` file, whose
// Projects use a SQLite database, as the Laravel Recipe's do when
// DB_CONNECTION is sqlite.
type databaseStack struct{}

func (databaseStack) Name() string { return "database-stack" }

func (databaseStack) Detection() detect.Rule { return detect.Rule{File: "database-stack"} }

func (databaseStack) Defaults(dir string) (recipe.Result, error) {
	return layered(config.ParseLayer("the database-stack Recipe", []byte(`
[database]
engine = "sqlite"
path = "database/database.sqlite"
`)))
}

func TestAWorktreesDatabaseIsClonedUnlessConfigured(t *testing.T) {
	res := mustResolveFixture(t, "database-default")

	want := recipe.Database{Engine: "sqlite", Path: "database/database.sqlite", Mode: "clone"}
	if res.Database != want {
		t.Errorf("Database = %+v, want %+v", res.Database, want)
	}
}

func TestDatabaseConfigMergesKeyByKeyOverTheRecipesDefaults(t *testing.T) {
	res := mustResolveFixture(t, "database-layers", databaseStack{})

	want := recipe.Database{Engine: "sqlite", Path: "database/database.sqlite", Mode: "shared"}
	if res.Database != want {
		t.Errorf("Database = %+v, want %+v", res.Database, want)
	}
}

func TestAPostgresDatabaseUsesThePostgresServiceAtItsDefaultVersion(t *testing.T) {
	res := mustResolveFixture(t, "database-postgres")

	want := recipe.Database{Engine: "postgres", Mode: "clone"}
	if res.Database != want {
		t.Errorf("Database = %+v, want %+v", res.Database, want)
	}
	if !slices.Equal(res.Services, []recipe.Service{{Engine: "postgres", Version: "18"}}) {
		t.Errorf("Services = %+v, want postgres 18", res.Services)
	}
	if svc, ok := res.DatabaseService(); !ok || svc != (recipe.Service{Engine: "postgres", Version: "18"}) {
		t.Errorf("DatabaseService() = %+v, %t, want postgres 18", svc, ok)
	}
}

func TestAPostgresDatabaseUsesThePinnedPostgresVersion(t *testing.T) {
	res := mustResolveFixture(t, "database-postgres-pinned")

	if !slices.Equal(res.Services, []recipe.Service{{Engine: "postgres", Version: "16"}}) {
		t.Errorf("Services = %+v, want postgres 16", res.Services)
	}
}

func TestASQLiteDatabaseUsesNoService(t *testing.T) {
	res := mustResolveFixture(t, "database-default")

	if len(res.Services) != 0 {
		t.Errorf("Services = %+v, want none", res.Services)
	}
	if svc, ok := res.DatabaseService(); ok {
		t.Errorf("DatabaseService() = %+v, want none", svc)
	}
}

func TestAProjectWithoutADatabaseEngineHasNoDatabase(t *testing.T) {
	if res := mustResolveFixture(t, "committed-only"); res.Database != (recipe.Database{}) {
		t.Errorf("Database = %+v, want none", res.Database)
	}
}

func TestInvalidDatabaseConfigNamesTheFileAndTheKey(t *testing.T) {
	tests := []struct {
		fixture string
		want    []string // substrings of the error, besides the file
	}{
		{"invalid-database-engine", []string{"database.engine", "sqlite", `"oracle"`}},
		{"invalid-database-mode", []string{"database.worktree", "clone, fresh or shared", `"copy"`}},
		{"invalid-database-path", []string{"database.path", "inside the Worktree"}},
		{"invalid-database-no-path", []string{"database.path", "isn't set"}},
		{"invalid-database-not-table", []string{"database", "table"}},
		{"invalid-database-postgres-path", []string{"database.path", "only for SQLite", "postgres"}},
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

func TestDatabaseConfigWrittenAsALayerResolvesToItself(t *testing.T) {
	for _, fixture := range []string{"database-default", "database-layers", "database-postgres", "database-postgres-pinned"} {
		t.Run(fixture, func(t *testing.T) {
			res := mustResolveFixture(t, fixture, databaseStack{})

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
			if again.Database != res.Database || !slices.Equal(again.Services, res.Services) {
				t.Errorf("written as:\n%s\nresolves to %+v %+v, want %+v %+v", buf.String(),
					again.Database, again.Services, res.Database, res.Services)
			}
		})
	}
}
