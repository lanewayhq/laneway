package plugins_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lanewayhq/laneway/internal/recipe"
)

// railsGemfile is a new Rails app's Gemfile, which loads no .env files.
const railsGemfile = "source \"https://rubygems.org\"\n\ngem \"rails\", \"~> 8.1.0\"\ngem \"puma\"\n"

// railsProject is a Rails app in a temporary directory: a Gemfile declaring
// rails and config/application.rb, plus files (path → content), which may
// replace the Gemfile.
func railsProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	all := map[string]string{"Gemfile": railsGemfile, "config/application.rb": "module Shop\nend\n"}
	for name, content := range files {
		all[name] = content
	}
	for name, content := range all {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func mustResolveDir(t *testing.T, dir string) recipe.Resolved {
	t.Helper()
	res, err := resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestARailsAppGetsATestDatabaseWhenItsTestConfigUsesOne(t *testing.T) {
	for _, tc := range []struct {
		name        string
		databaseYml string
		want        bool
	}{
		{"its test database is in the Service", "default: &default\n  adapter: postgresql\n\n" +
			"development:\n  <<: *default\n  database: shop_development\n\ntest:\n  <<: *default\n  database: shop_test\n", true},
		{"several test databases, the primary in the Service", "development:\n  primary:\n    adapter: mysql2\n    database: shop_development\n" +
			"test:\n  primary:\n    adapter: trilogy\n    database: shop_test\n  cache:\n    adapter: sqlite3\n    database: storage/test_cache.sqlite3\n", true},
		{"no test config", "development:\n  adapter: postgresql\n  database: shop_development\n", false},
		{"tests on SQLite", "development:\n  adapter: postgresql\n  database: shop_development\ntest:\n  adapter: sqlite3\n  database: storage/test.sqlite3\n", false},
		{"everything on SQLite", "development:\n  adapter: sqlite3\n  database: storage/development.sqlite3\ntest:\n  adapter: sqlite3\n  database: storage/test.sqlite3\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := mustResolveDir(t, railsProject(t, map[string]string{"config/database.yml": tc.databaseYml}))
			if res.TestDatabase != tc.want {
				t.Errorf("TestDatabase = %t, want %t", res.TestDatabase, tc.want)
			}
		})
	}
}

func TestDatabaseTestOffTurnsOffARailsAppsTestDatabase(t *testing.T) {
	res := mustResolveDir(t, railsProject(t, map[string]string{
		"config/database.yml": "development:\n  adapter: postgresql\n  database: shop_development\ntest:\n  adapter: postgresql\n  database: shop_test\n",
		"laneway.toml":        "[database]\ntest = \"off\"\n",
	}))
	if res.TestDatabase {
		t.Error("TestDatabase = true, want database.test = off to turn it off")
	}
}
