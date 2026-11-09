package plugins_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
)

func TestALaravelProjectHasATestDatabaseWhenItsTestsUseOne(t *testing.T) {
	tests := []struct {
		fixture string
		want    bool
	}{
		{"laravel-test-env-testing", true},
		{"laravel-test-phpunit-mysql", true},
		{"laravel-test-phpunit-dist", true},
		// Laravel's env() reads $_SERVER too.
		{"laravel-test-phpunit-server", true},
		// Neither: the tests use .env's database, or one PHPUnit makes.
		{"laravel-mysql", false},
		{"laravel-test-phpunit-sqlite", false},
		{"laravel-test-phpunit-commented", false},
		// PHPUnit reads phpunit.xml, not phpunit.xml.dist, when it has both.
		{"laravel-test-phpunit-over-dist", false},
		// Test Databases are only for databases in a Service.
		{"laravel-test-sqlite-env-testing", false},
		{"laravel-test-off", false},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			if got := resolveLaravel(t, tt.fixture).TestDatabase; got != tt.want {
				t.Errorf("TestDatabase = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestAPHPUnitConfigThatHardCodesDBDatabaseGivesAWarningNamingTheFile(t *testing.T) {
	tests := []struct {
		fixture, file, value string
	}{
		{"laravel-test-phpunit-database", "phpunit.xml", `"shop_testing"`},
		{"laravel-test-phpunit-dist-database", "phpunit.xml.dist", `"testing"`},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			res := resolveLaravel(t, tt.fixture)

			if !res.TestDatabase {
				t.Fatal("TestDatabase = false, want true")
			}
			if len(res.TestDatabaseWarnings) != 1 {
				t.Fatalf("TestDatabaseWarnings = %q, want one", res.TestDatabaseWarnings)
			}
			for _, want := range []string{tt.file + " sets DB_DATABASE to " + tt.value, "Test Database", "never edits " + tt.file} {
				if !strings.Contains(res.TestDatabaseWarnings[0], want) {
					t.Errorf("warning %q doesn't say %q", res.TestDatabaseWarnings[0], want)
				}
			}
		})
	}
}

func TestOnlyAPHPUnitConfigThatBypassesTheTestDatabaseGivesAWarning(t *testing.T) {
	// The Test Database is used, or there is none to bypass.
	for _, fixture := range []string{"laravel-test-phpunit-mysql", "laravel-test-env-testing", "laravel-test-off-phpunit-database", "laravel-test-phpunit-sqlite"} {
		t.Run(fixture, func(t *testing.T) {
			if w := resolveLaravel(t, fixture).TestDatabaseWarnings; len(w) != 0 {
				t.Errorf("TestDatabaseWarnings = %q, want none", w)
			}
		})
	}
}

func TestALaravelWorktreeHasATestDatabaseWhenTheMainWorktreeHasAnUncommittedEnvTesting(t *testing.T) {
	env, err := os.ReadFile(filepath.Join("testdata", "laravel-mysql", ".env"))
	if err != nil {
		t.Fatal(err)
	}
	repo := gitfixture.New(t, "lara-untracked-env-testing").
		WriteFile("artisan", "#!/usr/bin/env php\n").
		WriteFile(".env", string(env)).
		WriteFile(".gitignore", ".env\n.env.testing\n").
		Commit("a Laravel app whose test env isn't committed")
	wt := repo.AddWorktree("feature")
	wt.WriteFile(".env", string(env)) // Laneway copies the main Worktree's
	if resolveDir(t, wt.Dir).TestDatabase {
		t.Fatal("TestDatabase = true without any .env.testing, want false")
	}

	repo.WriteFile(".env.testing", "APP_ENV=testing\n")

	// Setup copies the main Worktree's .env.testing into the Worktree.
	if !resolveDir(t, wt.Dir).TestDatabase {
		t.Error("TestDatabase = false, want true: the Worktree's .env.testing will be a copy of the main Worktree's")
	}
}
