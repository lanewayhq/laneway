package recipe_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/config"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
)

// resolveLaravel resolves testdata/<name> the way the daemon does, with the
// Builtin Recipes.
func resolveLaravel(t *testing.T, name string) recipe.Resolved {
	t.Helper()
	res, err := recipe.ResolveDir(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("resolving %s: %v", name, err)
	}
	return res
}

// process is the resolved Process called name, failing the test without it.
func process(t *testing.T, res recipe.Resolved, name string) recipe.Process {
	t.Helper()
	for _, p := range res.Processes {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no Process %q in %+v", name, res.Processes)
	return recipe.Process{}
}

func TestAProjectWithAnArtisanFileIsLaravelAndServedByArtisanServeWithWorkers(t *testing.T) {
	res := resolveLaravel(t, "laravel-mysql")

	if res.Recipe != "laravel" {
		t.Fatalf("Recipe = %q, want laravel", res.Recipe)
	}
	want := recipe.Process{
		Name:      "web",
		Command:   "PHP_CLI_SERVER_WORKERS=8 php artisan serve --host=127.0.0.1 --port={port} --no-reload",
		Primary:   true,
		HTTP:      true,
		StaticDir: "public",
	}
	if got := process(t, res, "web"); !reflect.DeepEqual(got, want) {
		t.Errorf("web =\n  %+v\nwant\n  %+v", got, want)
	}
}

func TestALaravelProjectGetsAQueueWorkerUnlessItsQueueIsSync(t *testing.T) {
	want := recipe.Process{Name: "queue", Command: "php artisan queue:work"}
	if got := process(t, resolveLaravel(t, "laravel-mysql"), "queue"); !reflect.DeepEqual(got, want) {
		t.Errorf("with QUEUE_CONNECTION=database, queue =\n  %+v\nwant\n  %+v", got, want)
	}

	res := resolveLaravel(t, "laravel-queue-sync")
	for _, p := range res.Processes {
		if p.Name == "queue" {
			t.Errorf("with QUEUE_CONNECTION=sync, there is a queue Process: %+v", p)
		}
	}
}

func TestALaravelProjectsDatabaseComesFromDBConnection(t *testing.T) {
	inService := map[string]string{
		"DB_CONNECTION": "", // filled in per case
		"DB_HOST":       "{db_host}",
		"DB_PORT":       "{db_port}",
		"DB_DATABASE":   "{db}",
		"DB_USERNAME":   "{db_username}",
		"DB_PASSWORD":   "{db_password}",
	}
	for _, tc := range []struct {
		fixture, connection string
		database            recipe.Database
		service             recipe.Service
	}{
		{"laravel-mysql", "mysql", recipe.Database{Engine: "mysql", Mode: "clone"}, recipe.Service{Engine: "mysql", Version: "8.4"}},
		{"laravel-pgsql", "pgsql", recipe.Database{Engine: "postgres", Mode: "clone"}, recipe.Service{Engine: "postgres", Version: "18"}},
	} {
		t.Run(tc.connection, func(t *testing.T) {
			res := resolveLaravel(t, tc.fixture)
			if res.Database != tc.database {
				t.Errorf("Database = %+v, want %+v", res.Database, tc.database)
			}
			if s, ok := res.DatabaseService(); !ok || s != tc.service {
				t.Errorf("DatabaseService() = %+v, %v; want %+v", s, ok, tc.service)
			}
			want := map[string]string{}
			for k, v := range inService {
				want[k] = v
			}
			want["DB_CONNECTION"] = tc.connection
			for k, v := range want {
				if res.Env[k] != v {
					t.Errorf("env.%s = %q, want %q", k, res.Env[k], v)
				}
			}
		})
	}
}

func TestALaravelProjectOnSQLiteGetsAFilePerWorktreeAndNoService(t *testing.T) {
	res := resolveLaravel(t, "laravel-sqlite")

	if want := (recipe.Database{Engine: "sqlite", Path: "database/database.sqlite", Mode: "clone"}); res.Database != want {
		t.Errorf("Database = %+v, want %+v", res.Database, want)
	}
	if s, ok := res.DatabaseService(); ok {
		t.Errorf("DatabaseService() = %+v, want none", s)
	}
	if res.Env["DB_CONNECTION"] != "sqlite" || res.Env["DB_DATABASE"] != "{db}" {
		t.Errorf("env DB_CONNECTION = %q, DB_DATABASE = %q; want sqlite and {db}", res.Env["DB_CONNECTION"], res.Env["DB_DATABASE"])
	}
	for _, k := range []string{"DB_HOST", "DB_PORT", "DB_USERNAME", "DB_PASSWORD"} {
		if v, ok := res.Env[k]; ok {
			t.Errorf("env.%s = %q, want it left alone", k, v)
		}
	}
}

func TestALaravelProjectWithAnotherDatabaseConnectionGetsNoDatabaseAndAWarning(t *testing.T) {
	res := resolveLaravel(t, "laravel-sqlsrv")

	if res.Database != (recipe.Database{}) {
		t.Errorf("Database = %+v, want none", res.Database)
	}
	for k := range res.Env {
		if strings.HasPrefix(k, "DB_") {
			t.Errorf("env.%s = %q, want no DB_ keys", k, res.Env[k])
		}
	}
	if !slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, `DB_CONNECTION is "sqlsrv"`) }) {
		t.Errorf("Warnings = %q, want one about DB_CONNECTION sqlsrv", res.Warnings)
	}
}

// laravelProject is a temp Laravel-shaped Project: an artisan file, the
// given files and nothing else.
func laravelProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["artisan"] = "#!/usr/bin/env php\n"
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func resolveDir(t *testing.T, dir string) recipe.Resolved {
	t.Helper()
	res, err := recipe.ResolveDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestALaravelProjectAlwaysUsesMailpitAndGetsItsURLAndMailSettings(t *testing.T) {
	res := resolveLaravel(t, "laravel-mysql")

	want := []recipe.Service{{Engine: "mailpit", Version: "v1"}, {Engine: "mysql", Version: "8.4"}}
	if !reflect.DeepEqual(res.Services, want) {
		t.Errorf("Services = %+v, want %+v", res.Services, want)
	}
	for k, v := range map[string]string{
		"APP_URL":     "{url}",
		"MAIL_MAILER": "smtp",
		"MAIL_HOST":   "{mailpit_host}",
		"MAIL_PORT":   "{mailpit_port}",
	} {
		if res.Env[k] != v {
			t.Errorf("env.%s = %q, want %q", k, res.Env[k], v)
		}
	}
	for _, k := range []string{"REDIS_HOST", "REDIS_PORT", "REDIS_PASSWORD"} {
		if v, ok := res.Env[k]; ok {
			t.Errorf("without Redis, env.%s = %q, want it left alone", k, v)
		}
	}
}

func TestALaravelProjectUsesRedisWhenItsCacheQueueOrSessionDoes(t *testing.T) {
	for _, setting := range []string{"CACHE_STORE", "CACHE_DRIVER", "QUEUE_CONNECTION", "SESSION_DRIVER"} {
		t.Run(setting, func(t *testing.T) {
			res := resolveDir(t, laravelProject(t, map[string]string{".env": "DB_CONNECTION=sqlite\n" + setting + "=redis\n"}))

			if !slices.Contains(res.Services, recipe.Service{Engine: "redis", Version: "7"}) {
				t.Errorf("Services = %+v, want redis:7 among them", res.Services)
			}
			for k, v := range map[string]string{
				"REDIS_HOST":     "{redis_host}",
				"REDIS_PORT":     "{redis_port}",
				"REDIS_PASSWORD": "{redis_password}",
			} {
				if res.Env[k] != v {
					t.Errorf("env.%s = %q, want %q", k, res.Env[k], v)
				}
			}
		})
	}
}

func TestOnlyWorktreesOtherThanTheMainOneGetTheirOwnRedisAndCachePrefixes(t *testing.T) {
	res := resolveLaravel(t, "laravel-mysql")

	main := res.EnvFor(true)
	for _, k := range []string{"REDIS_PREFIX", "CACHE_PREFIX"} {
		if v, ok := main[k]; ok {
			t.Errorf("the main Worktree's env.%s = %q, want Laravel's default left alone", k, v)
		}
	}
	if main["APP_URL"] != "{url}" {
		t.Errorf("the main Worktree's env.APP_URL = %q, want {url}", main["APP_URL"])
	}
	linked := res.EnvFor(false)
	for _, k := range []string{"REDIS_PREFIX", "CACHE_PREFIX"} {
		if v := linked[k]; v != "{project}_{worktree}_" {
			t.Errorf("another Worktree's env.%s = %q, want {project}_{worktree}_", k, v)
		}
	}
	if linked["APP_URL"] != "{url}" {
		t.Errorf("another Worktree's env.APP_URL = %q, want {url}", linked["APP_URL"])
	}
}

func TestLinkedEnvMergesByKeyOverTheRecipes(t *testing.T) {
	res := resolveLaravel(t, "laravel-linked-env")

	linked := res.EnvFor(false)
	for k, v := range map[string]string{
		"HORIZON_PREFIX": "{project}_{worktree}_horizon:",
		"CACHE_PREFIX":   "{worktree}_cache_",
		"REDIS_PREFIX":   "{project}_{worktree}_",
	} {
		if linked[k] != v {
			t.Errorf("another Worktree's env.%s = %q, want %q", k, linked[k], v)
		}
	}
	if v, ok := res.EnvFor(true)["HORIZON_PREFIX"]; ok {
		t.Errorf("the main Worktree's env.HORIZON_PREFIX = %q, want none", v)
	}
}

// keyGenerate is the Laravel Recipe's setup step that gives a .env without
// an APP_KEY one, as `php artisan key:generate` does after a fresh clone.
const keyGenerate = `grep -Eq "^APP_KEY=[\"']?[^\"'[:space:]#]" .env || php artisan key:generate`

func TestALaravelProjectIsSetUpWithComposerItsNodePackageManagerAndAStorageLink(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		npm   string // the Node step; "" for none
	}{
		{"no package.json", map[string]string{}, ""},
		{"package.json without a lockfile", map[string]string{"package.json": "{}"}, "npm install"},
		{"npm", map[string]string{"package.json": "{}", "package-lock.json": "{}"}, "npm install"},
		{"pnpm", map[string]string{"package.json": "{}", "pnpm-lock.yaml": ""}, "pnpm install"},
		{"yarn", map[string]string{"package.json": "{}", "yarn.lock": ""}, "yarn install"},
		{"bun", map[string]string{"package.json": "{}", "bun.lock": ""}, "bun install"},
		{"bun's binary lockfile", map[string]string{"package.json": "{}", "bun.lockb": ""}, "bun install"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.files[".env"] = "DB_CONNECTION=sqlite\n"
			res := resolveDir(t, laravelProject(t, tc.files))

			want := []string{"composer install", keyGenerate}
			if tc.npm != "" {
				want = append(want, tc.npm)
			}
			want = append(want, "php artisan storage:link")
			if !reflect.DeepEqual(res.OnCreate, want) {
				t.Errorf("OnCreate =\n  %q\nwant\n  %q", res.OnCreate, want)
			}
		})
	}
}

// TestTheKeyGenerateStepOnlyGeneratesAMissingKey runs the step's condition
// (without PHP: a stub php records that it ran) against .env files.
func TestTheKeyGenerateStepOnlyGeneratesAMissingKey(t *testing.T) {
	for env, generates := range map[string]bool{
		"APP_KEY=\n":                               true,
		"APP_KEY=\"\"\n":                           true,
		"APP_KEY=''\n":                             true,
		"APP_KEY= # set by key:generate\n":         true,
		"APP_NAME=x\n":                             true,
		"APP_KEY=base64:abc=\n":                    false,
		"APP_KEY=\"base64:abc=\"\n":                false,
		"APP_KEY=SomeRandomString32Characters!!\n": false,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "php"), []byte("#!/bin/sh\ntouch generated\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", "-c", keyGenerate)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%q: %v\n%s", env, err, out)
		}
		_, err := os.Stat(filepath.Join(dir, "generated"))
		if ran := err == nil; ran != generates {
			t.Errorf(".env %q: key:generate ran = %v, want %v", env, ran, generates)
		}
	}
}

func TestAFreshlyClonedLaravelProjectIsReadFromItsEnvExample(t *testing.T) {
	res := resolveDir(t, laravelProject(t, map[string]string{
		".env.example": "DB_CONNECTION=pgsql\nQUEUE_CONNECTION=sync\nCACHE_STORE=redis\n",
	}))

	if res.Database.Engine != "postgres" {
		t.Errorf("Database.Engine = %q, want postgres from .env.example", res.Database.Engine)
	}
	if !slices.Contains(res.Services, recipe.Service{Engine: "redis", Version: "7"}) {
		t.Errorf("Services = %+v, want redis:7 from .env.example", res.Services)
	}
	for _, p := range res.Processes {
		if p.Name == "queue" {
			t.Errorf("with .env.example's QUEUE_CONNECTION=sync, there is a queue Process: %+v", p)
		}
	}
}

func TestALaravelProjectWithoutEnvFilesGetsLaravelsDefaults(t *testing.T) {
	res := resolveDir(t, laravelProject(t, map[string]string{}))

	if res.Database.Engine != "sqlite" {
		t.Errorf("Database.Engine = %q, want Laravel's default sqlite", res.Database.Engine)
	}
	process(t, res, "queue") // Laravel's default queue is database
	want := []recipe.Service{{Engine: "mailpit", Version: "v1"}}
	if !reflect.DeepEqual(res.Services, want) {
		t.Errorf("Services = %+v, want %+v", res.Services, want)
	}
}

func TestProjectConfigLayersOverTheLaravelRecipe(t *testing.T) {
	res := resolveLaravel(t, "laravel-layers")

	want := []recipe.Process{{
		Name:      "web",
		Command:   "PHP_CLI_SERVER_WORKERS=4 php -d xdebug.mode=debug artisan serve --host=127.0.0.1 --port={port} --no-reload",
		Primary:   true,
		HTTP:      true,
		StaticDir: "public",
	}}
	if !reflect.DeepEqual(res.Processes, want) {
		t.Errorf("Processes =\n  %+v\nwant\n  %+v", res.Processes, want)
	}
	if want := []string{"queue"}; !reflect.DeepEqual(res.Disabled, want) {
		t.Errorf("Disabled = %q, want %q", res.Disabled, want)
	}
	if s, _ := res.DatabaseService(); s != (recipe.Service{Engine: "mysql", Version: "8.0"}) {
		t.Errorf("DatabaseService() = %+v, want the pinned mysql:8.0", s)
	}
	if got := res.OnCreate[len(res.OnCreate)-1]; got != "php artisan migrate --seed" {
		t.Errorf("OnCreate = %q, want the Project's step after the Recipe's", res.OnCreate)
	}
	if res.Env["MAIL_MAILER"] != "log" || res.Env["MAIL_HOST"] != "{mailpit_host}" {
		t.Errorf("env MAIL_MAILER = %q, MAIL_HOST = %q; want log from portby.local.toml and the Recipe's {mailpit_host}",
			res.Env["MAIL_MAILER"], res.Env["MAIL_HOST"])
	}
}

func TestTheLaravelRecipesConfigWrittenAsALayerResolvesToItself(t *testing.T) {
	for _, fixture := range []string{"laravel-mysql", "laravel-pgsql", "laravel-sqlite", "laravel-sqlsrv", "laravel-queue-sync", "laravel-linked-env", "laravel-layers"} {
		t.Run(fixture, func(t *testing.T) {
			res := resolveLaravel(t, fixture)

			var buf strings.Builder
			if err := config.Encode(&buf, res.Layer()); err != nil {
				t.Fatal(err)
			}
			written, err := config.ParseLayer("written", []byte(buf.String()))
			if err != nil {
				t.Fatalf("reading back:\n%s\n%v", buf.String(), err)
			}
			for _, recipes := range [][]recipe.Recipe{nil, recipe.Builtin} {
				again, err := recipe.Resolve(recipe.Input{Dir: filepath.Join("testdata", fixture), Layers: []config.Layer{written}, Recipes: recipes})
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
