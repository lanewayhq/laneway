package recipe_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/recipe"
)

// resolveRails resolves testdata/<name> the way the daemon does, with the
// Builtin Recipes.
func resolveRails(t *testing.T, name string) recipe.Resolved {
	t.Helper()
	res, err := recipe.ResolveDir(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("resolving %s: %v", name, err)
	}
	return res
}

func TestAProjectWithRailsInItsGemfileIsRailsAndServedByRailsServerOnItsPort(t *testing.T) {
	res := resolveRails(t, "rails-postgres")

	if res.Recipe != "rails" {
		t.Fatalf("Recipe = %q, want rails", res.Recipe)
	}
	want := recipe.Process{
		Name:    "web",
		Command: "set -a; [ ! -f .env ] || . ./.env; set +a; exec bin/rails server -b 127.0.0.1 -p {port}",
		Primary: true,
		HTTP:    true,
	}
	if got := process(t, res, "web"); !reflect.DeepEqual(got, want) {
		t.Errorf("web =\n  %+v\nwant\n  %+v", got, want)
	}
}

func TestARailsProjectsDatabaseComesFromDatabaseYmlAndReachesRailsAsDatabaseURL(t *testing.T) {
	for _, tc := range []struct {
		fixture  string
		database recipe.Database
		service  *recipe.Service
		url      string
	}{
		{"rails-postgres", recipe.Database{Engine: "postgres", Mode: "clone"}, &recipe.Service{Engine: "postgres", Version: "18"},
			"postgresql://{db_username}:{db_password}@{db_host}:{db_port}/{db}"},
		{"rails-trilogy", recipe.Database{Engine: "mysql", Mode: "clone"}, &recipe.Service{Engine: "mysql", Version: "8.4"},
			"trilogy://{db_username}:{db_password}@{db_host}:{db_port}/{db}"},
		// The app's primary database, when development has several.
		{"rails-multi-database", recipe.Database{Engine: "mysql", Mode: "clone"}, &recipe.Service{Engine: "mysql", Version: "8.4"},
			"mysql2://{db_username}:{db_password}@{db_host}:{db_port}/{db}"},
		// A file per Worktree, where database.yml puts it.
		{"rails-sqlite", recipe.Database{Engine: "sqlite", Path: "db/development.sqlite3", Mode: "clone"}, nil,
			"sqlite3:{db}"},
		// DATABASE_URL wins over database.yml, as in Rails.
		{"rails-database-url", recipe.Database{Engine: "postgres", Mode: "clone"}, &recipe.Service{Engine: "postgres", Version: "18"},
			"postgis://{db_username}:{db_password}@{db_host}:{db_port}/{db}"},
		{"rails-database-url-sqlite", recipe.Database{Engine: "sqlite", Path: "storage/dev.sqlite3", Mode: "clone"}, nil,
			"sqlite3:{db}"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			res := resolveRails(t, tc.fixture)
			if res.Database != tc.database {
				t.Errorf("Database = %+v, want %+v", res.Database, tc.database)
			}
			s, ok := res.DatabaseService()
			switch {
			case tc.service == nil && ok:
				t.Errorf("DatabaseService() = %+v, want none", s)
			case tc.service != nil && (!ok || s != *tc.service):
				t.Errorf("DatabaseService() = %+v, %v; want %+v", s, ok, *tc.service)
			}
			if got := res.Env["DATABASE_URL"]; got != tc.url {
				t.Errorf("env.DATABASE_URL = %q, want %q", got, tc.url)
			}
			if len(res.Warnings) > 0 {
				t.Errorf("Warnings = %q, want none", res.Warnings)
			}
		})
	}
}

func TestARailsProjectWithoutADatabaseGetsNone(t *testing.T) {
	res := resolveRails(t, "rails-no-database")

	if res.Database != (recipe.Database{}) {
		t.Errorf("Database = %+v, want none", res.Database)
	}
	if _, ok := res.Env["DATABASE_URL"]; ok {
		t.Errorf("env.DATABASE_URL = %q, want it left alone", res.Env["DATABASE_URL"])
	}
	if len(res.Warnings) > 0 {
		t.Errorf("Warnings = %q, want none", res.Warnings)
	}
}

func TestARailsProjectOnAnAdapterLanewayHasNoDatabaseForIsWarnedAndLeftAlone(t *testing.T) {
	res := resolveRails(t, "rails-sqlserver")

	if res.Database != (recipe.Database{}) {
		t.Errorf("Database = %+v, want none", res.Database)
	}
	if _, ok := res.Env["DATABASE_URL"]; ok {
		t.Errorf("env.DATABASE_URL = %q, want it left alone", res.Env["DATABASE_URL"])
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("Warnings = %q, want one", res.Warnings)
	}
	for _, want := range []string{"the rails Recipe", "config/database.yml", `"sqlserver" adapter`, "mysql2, postgis, postgresql, sqlite3 and trilogy", "[database] in laneway.toml"} {
		if !strings.Contains(res.Warnings[0], want) {
			t.Errorf("warning %q doesn't say %q", res.Warnings[0], want)
		}
	}
}

func TestALaravelProjectIsNotRailsAndARubyProjectWithoutRailsIsNeither(t *testing.T) {
	if res := resolveRails(t, "laravel-pgsql"); res.Recipe != "laravel" {
		t.Errorf("laravel-pgsql: Recipe = %q, want laravel", res.Recipe)
	}
	dir := project(t, map[string]string{"Gemfile": "source \"https://rubygems.org\"\ngem \"sinatra\"\n# gem \"rails\"\n", "config.ru": "run App\n"})
	res, err := recipe.ResolveDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Recipe != "" {
		t.Errorf("a Sinatra app: Recipe = %q, want none", res.Recipe)
	}
}

func TestANewRailsWorktreeInstallsItsGemsWithBundler(t *testing.T) {
	res := resolveRails(t, "rails-postgres")

	if want := []string{"bundle install"}; !reflect.DeepEqual(res.OnCreate, want) {
		t.Errorf("OnCreate = %q, want %q", res.OnCreate, want)
	}
}
