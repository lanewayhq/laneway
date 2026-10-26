package recipe

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mrsafalpiya/portby.dev/internal/config"
	"github.com/mrsafalpiya/portby.dev/internal/dotenv"
	"github.com/mrsafalpiya/portby.dev/internal/service"
)

// Laravel is the Laravel Recipe: a Project with an artisan file. Its
// defaults come from the Project's files: the database, Services and queue
// worker from .env (or .env.example before there is one), the Node package
// manager from the lockfile.
type Laravel struct{}

var _ TestDatabaseRecipe = Laravel{}

func (Laravel) Name() string { return "laravel" }

func (Laravel) Detect(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, "artisan"))
	return err == nil && fi.Mode().IsRegular()
}

// webCommand serves the app with PHP's built-in server and 8 workers, which
// spike #4 found matches PHP-FPM once Portby serves public/ itself (see
// StaticDir). Laravel ignores PHP_CLI_SERVER_WORKERS without --no-reload, so
// artisan doesn't restart itself when .env changes: Portby restarts the
// Worktree's Processes when it changes .env. --no-reload is also what lets
// PHP_INI_SCAN_DIR reach the server, adding Portby's PHP ini
// ({php_ini_dir}: no OPcache revalidation delay, so an edited Blade view
// shows on the next request; spike #2).
const webCommand = "PHP_INI_SCAN_DIR=$PHP_INI_SCAN_DIR:{php_ini_dir} PHP_CLI_SERVER_WORKERS=8 php artisan serve --host=127.0.0.1 --port={port} --no-reload"

func (Laravel) Defaults(dir string) (config.Layer, error) {
	env := readLaravelEnv(dir)
	l := config.Layer{Source: "the laravel Recipe", Processes: map[string]config.ProcessLayer{}}
	l.Processes["web"] = config.ProcessLayer{Command: ptr(webCommand), Primary: ptr(true), StaticDir: ptr("public")}
	if env.setting("QUEUE_CONNECTION", "database") != "sync" {
		l.Processes["queue"] = config.ProcessLayer{Command: ptr("php artisan queue:work")}
	}
	// The vite Process: an HTTP Process, served at vite.<domain>, when
	// package.json uses laravel-vite-plugin.
	if usesLaravelVitePlugin(dir) {
		l.Processes["vite"] = config.ProcessLayer{Command: ptr(viteCommand), HTTP: ptr(true)}
	}
	l.Env = map[string]string{"APP_URL": "{url}"}
	l.Services = map[string]string{}
	laravelDatabase(&l, dir, env)
	laravelServices(&l, env)
	l.OnCreate = laravelSetup(dir)
	// Every Worktree but the main one gets its own Redis keys and cache
	// entries, so its queue worker never runs another Worktree's jobs. The
	// main Worktree keeps Laravel's defaults (derived from APP_NAME).
	l.LinkedEnv = map[string]string{
		"REDIS_PREFIX": "{project}_{worktree}_",
		"CACHE_PREFIX": "{project}_{worktree}_",
	}
	return l, nil
}

// keyGenerate gives a .env without an APP_KEY one. A fresh clone's .env is a
// copy of .env.example, whose APP_KEY is empty; another Worktree's .env is a
// copy of the main Worktree's, so it shares its key and keeps it.
const keyGenerate = `grep -Eq "^APP_KEY=[\"']?[^\"'[:space:]#]" .env || php artisan key:generate`

// laravelSetup are the setup steps: Composer packages, an app key if .env
// has none (Composer's autoloader first: artisan needs it), Node packages
// with the package manager whose lockfile the Project has, and the
// public/storage link.
func laravelSetup(dir string) []string {
	steps := []string{"composer install", keyGenerate}
	if exists(dir, "package.json") {
		manager := "npm"
		for _, lock := range []struct{ file, manager string }{
			{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lock", "bun"}, {"bun.lockb", "bun"}, {"package-lock.json", "npm"},
		} {
			if exists(dir, lock.file) {
				manager = lock.manager
				break
			}
		}
		steps = append(steps, manager+" install")
	}
	return append(steps, "php artisan storage:link")
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// laravelServices adds Redis when the cache, queue or session use it, and
// Mailpit, which catches the Project's mail, always.
func laravelServices(l *config.Layer, env laravelEnv) {
	redis := env.setting("CACHE_STORE", env.setting("CACHE_DRIVER", "database")) == "redis" ||
		env.setting("QUEUE_CONNECTION", "database") == "redis" ||
		env.setting("SESSION_DRIVER", "database") == "redis"
	if redis {
		l.Services[service.Redis.Name] = service.Redis.DefaultVersion
		l.Env["REDIS_HOST"] = "{redis_host}"
		l.Env["REDIS_PORT"] = "{redis_port}"
		l.Env["REDIS_PASSWORD"] = "{redis_password}"
	}
	l.Services[service.Mailpit.Name] = service.Mailpit.DefaultVersion
	l.Env["MAIL_MAILER"] = "smtp"
	l.Env["MAIL_HOST"] = "{mailpit_host}"
	l.Env["MAIL_PORT"] = "{mailpit_port}"
}

// laravelDatabase gives the Project the database its DB_CONNECTION names: a
// database in the Postgres or MySQL Service, or a SQLite file per Worktree.
func laravelDatabase(l *config.Layer, dir string, env laravelEnv) {
	connection := env.setting("DB_CONNECTION", "sqlite")
	switch connection {
	case "sqlite":
		l.Database = &config.DatabaseLayer{Engine: ptr("sqlite"), Path: ptr(sqlitePath(dir, env))}
		l.Env["DB_CONNECTION"] = connection
		l.Env["DB_DATABASE"] = "{db}"
		return
	case "pgsql":
		l.Database = &config.DatabaseLayer{Engine: ptr("postgres")}
	case "mysql":
		l.Database = &config.DatabaseLayer{Engine: ptr("mysql")}
	default:
		l.Warnings = append(l.Warnings, fmt.Sprintf("%s: DB_CONNECTION is %q, which Portby has no database for "+
			"(it knows sqlite, pgsql and mysql), so the Project's DB_ settings are left alone; "+
			"set [database] in %s to use one of Portby's", l.Source, connection, config.FileName))
		return
	}
	l.Env["DB_CONNECTION"] = connection
	l.Env["DB_HOST"] = "{db_host}"
	l.Env["DB_PORT"] = "{db_port}"
	l.Env["DB_DATABASE"] = "{db}"
	l.Env["DB_USERNAME"] = "{db_username}"
	l.Env["DB_PASSWORD"] = "{db_password}"
}

// sqlitePath is the SQLite file relative to the Worktree: Laravel's default,
// database/database.sqlite, unless DB_DATABASE is a file inside the Worktree.
// (After the Env Patch, DB_DATABASE is the Worktree's own file.)
func sqlitePath(dir string, env laravelEnv) string {
	const def = "database/database.sqlite"
	file := env.setting("DB_DATABASE", "")
	abs, err := filepath.Abs(dir)
	if !filepath.IsAbs(file) || err != nil {
		return def
	}
	rel, err := filepath.Rel(abs, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return def
	}
	return filepath.ToSlash(rel)
}

// laravelEnv is the settings the Recipe reads from the Project's env file.
type laravelEnv struct{ f *dotenv.File }

// readLaravelEnv reads dir's .env or, before there is one (Portby creates it
// from .env.example), its .env.example. With neither, every setting is
// Laravel's default.
func readLaravelEnv(dir string) laravelEnv {
	for _, name := range []string{".env", ".env.example"} {
		if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			return laravelEnv{dotenv.Parse(data)}
		}
	}
	return laravelEnv{dotenv.Parse(nil)}
}

// setting is the value of key the way Laravel's env() reads it, or def,
// Laravel's own default in its config files, if it's unset or null.
func (e laravelEnv) setting(key, def string) string {
	v, ok := e.f.Get(key)
	if v = strings.TrimSpace(v); !ok || v == "" || strings.EqualFold(v, "null") || strings.EqualFold(v, "(null)") {
		return def
	}
	return v
}

func ptr[T any](v T) *T { return &v }
