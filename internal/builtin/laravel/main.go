// Command laravel is the laravel Plugin, built into Laneway: the Laravel
// Recipe, for a Project with an artisan file. Its answer comes from the
// Project's files: the database, Services and queue worker from .env (or
// .env.example before there is one), the Node package manager from the
// lockfile, and the Test Database from .env.testing and phpunit.xml.
package main

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/lanewayhq/laneway/sdk"
)

func main() {
	sdk.Run(sdk.Plugin{Recipes: map[string]sdk.Recipe{"laravel": recipe}})
}

// webCommand serves the app with PHP's built-in server and 8 workers, which
// spike #4 found matches PHP-FPM once Laneway serves public/ itself (see
// static_dir). Laravel ignores PHP_CLI_SERVER_WORKERS without --no-reload, so
// artisan doesn't restart itself when .env changes: Laneway restarts the
// Worktree's Processes when it changes .env. --no-reload is also what lets
// PHP_INI_SCAN_DIR reach the server, adding Laneway's PHP ini
// ({php_ini_dir}: no OPcache revalidation delay, so an edited Blade view
// shows on the next request, spike #2; and $_SERVER['HTTPS'] set, so the app
// builds https:// URLs behind Laneway's proxy without trusting it).
const webCommand = "PHP_INI_SCAN_DIR=$PHP_INI_SCAN_DIR:{php_ini_dir} PHP_CLI_SERVER_WORKERS=8 php artisan serve --host=127.0.0.1 --port={port} --no-reload"

func recipe(p sdk.Project) (sdk.RecipeResult, error) {
	env := laravelEnv{p.EnvFile()}
	l := sdk.Layer{Processes: map[string]sdk.Process{}}
	l.Processes["web"] = sdk.Process{Command: webCommand, Primary: true, StaticDir: "public"}
	if env.setting("QUEUE_CONNECTION", "database") != "sync" {
		l.Processes["queue"] = sdk.Process{Command: "php artisan queue:work"}
	}
	l.Env = map[string]string{"APP_URL": "{url}"}
	l.Services = map[string]string{}
	res := sdk.RecipeResult{TestEnv: &sdk.TestEnv{File: testEnvFile}}
	res.Warnings = database(&l, p, env)
	services(&l, env)
	l.Worktree = &sdk.Worktree{OnCreate: setup(p)}
	// Every Worktree but the main one gets its own Redis keys and cache
	// entries, so its queue worker never runs another Worktree's jobs. The
	// main Worktree keeps Laravel's defaults (derived from APP_NAME).
	l.LinkedEnv = map[string]string{
		"REDIS_PREFIX": "{project}_{worktree}_",
		"CACHE_PREFIX": "{project}_{worktree}_",
	}
	res.Layer = l
	res.TestDatabase = autoTestDatabase(p)
	res.TestDatabaseWarnings = testDatabaseWarnings(p)
	return res, nil
}

// keyGenerate gives a .env without an APP_KEY one. A fresh clone's .env is a
// copy of .env.example, whose APP_KEY is empty; another Worktree's .env is a
// copy of the main Worktree's, so it shares its key and keeps it.
const keyGenerate = `grep -Eq "^APP_KEY=[\"']?[^\"'[:space:]#]" .env || php artisan key:generate`

// setup are the setup steps: Composer packages, an app key if .env has none
// (Composer's autoloader first: artisan needs it), Node packages with the
// package manager whose lockfile the Project has, and the public/storage
// link.
func setup(p sdk.Project) []string {
	steps := []string{"composer install", keyGenerate}
	if p.Exists("package.json") {
		manager := "npm"
		for _, lock := range []struct{ file, manager string }{
			{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lock", "bun"}, {"bun.lockb", "bun"}, {"package-lock.json", "npm"},
		} {
			if p.Exists(lock.file) {
				manager = lock.manager
				break
			}
		}
		steps = append(steps, manager+" install")
	}
	return append(steps, "php artisan storage:link")
}

// services adds Redis when the cache, queue or session use it, and Mailpit,
// which catches the Project's mail, always, each at its Engine's default
// version.
func services(l *sdk.Layer, env laravelEnv) {
	redis := env.setting("CACHE_STORE", env.setting("CACHE_DRIVER", "database")) == "redis" ||
		env.setting("QUEUE_CONNECTION", "database") == "redis" ||
		env.setting("SESSION_DRIVER", "database") == "redis"
	if redis {
		l.Services["redis"] = ""
		l.Env["REDIS_HOST"] = "{redis_host}"
		l.Env["REDIS_PORT"] = "{redis_port}"
		l.Env["REDIS_PASSWORD"] = "{redis_password}"
	}
	l.Services["mailpit"] = ""
	l.Env["MAIL_MAILER"] = "smtp"
	l.Env["MAIL_HOST"] = "{mailpit_host}"
	l.Env["MAIL_PORT"] = "{mailpit_port}"
}

// connections maps the Laravel DB_CONNECTION values Laneway has a database
// engine for to that engine, and whether it is a file in each Worktree
// (SQLite) rather than a database in a Service.
var connections = map[string]struct {
	engine string
	file   bool
}{"sqlite": {"sqlite", true}, "pgsql": {"postgres", false}, "mysql": {"mysql", false}}

// database gives the Project the database its DB_CONNECTION names: a
// database in a database Service (Postgres, MySQL), or a SQLite file per
// Worktree. It returns a warning for a connection Laneway has no database
// for.
func database(l *sdk.Layer, p sdk.Project, env laravelEnv) []string {
	connection := env.setting("DB_CONNECTION", "sqlite")
	c, ok := connections[connection]
	if !ok {
		known := slices.Sorted(maps.Keys(connections))
		return []string{fmt.Sprintf("DB_CONNECTION is %q, which Laneway has no database for "+
			"(it knows %s and %s), so the Project's DB_ settings are left alone; "+
			"set [database] in laneway.toml to use one of Laneway's", connection,
			strings.Join(known[:len(known)-1], ", "), known[len(known)-1])}
	}
	l.Env["DB_CONNECTION"] = connection
	l.Env["DB_DATABASE"] = "{db}"
	if c.file {
		l.Database = &sdk.Database{Engine: c.engine, Path: sqlitePath(p, env)}
		return nil
	}
	l.Database = &sdk.Database{Engine: c.engine}
	l.Env["DB_HOST"] = "{db_host}"
	l.Env["DB_PORT"] = "{db_port}"
	l.Env["DB_USERNAME"] = "{db_username}"
	l.Env["DB_PASSWORD"] = "{db_password}"
	return nil
}

// sqlitePath is the SQLite file relative to the Worktree: Laravel's default,
// database/database.sqlite, unless DB_DATABASE is a file inside the Worktree.
// (After the Env Patch, DB_DATABASE is the Worktree's own file, by its
// absolute path.)
func sqlitePath(p sdk.Project, env laravelEnv) string {
	file := env.setting("DB_DATABASE", "")
	if rel, ok := p.Rel(file); ok && path.IsAbs(file) {
		return rel
	}
	return "database/database.sqlite"
}

// laravelEnv is the settings the Recipe reads from the Project's env file.
type laravelEnv struct{ env sdk.EnvFile }

// setting is the value of key the way Laravel's env() reads it, or def,
// Laravel's own default in its config files, if it's unset or null.
func (e laravelEnv) setting(key, def string) string {
	v, ok := e.env.Get(key)
	if v = strings.TrimSpace(v); !ok || v == "" || strings.EqualFold(v, "null") || strings.EqualFold(v, "(null)") {
		return def
	}
	return v
}
