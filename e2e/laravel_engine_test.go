package e2e

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// The Laravel Recipe's slow tier: a real Laravel app, run by real PHP and
// Composer, with its database in MySQL on a real container engine.
//
//	PORTBY_SLOW=1 \
//	PORTBY_PHP=/path/to/php \
//	PORTBY_COMPOSER=/path/to/composer.phar \
//	PORTBY_LARAVEL_APP=/path/to/a/laravel/app \
//	go test ./e2e -run TestARealLaravelApp
//
// PORTBY_LARAVEL_APP is any Laravel 11+ app whose composer.lock Composer can
// install (from its cache, $PORTBY_COMPOSER_CACHE or the user's, so no
// download is needed); the test copies it without vendor/, node_modules/,
// .env and its Node files, and gives it routes and a migration of its own.

// laravelRoutes are the app's routes: /portby reports what the app sees.
const laravelRoutes = `<?php

use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Route;

Route::get('/portby', fn () => [
    'url' => config('app.url'),
    'database' => DB::connection()->getDatabaseName(),
    'notes' => DB::table('notes')->orderBy('id')->pluck('body'),
    'workers' => getenv('PHP_CLI_SERVER_WORKERS'),
    'sapi' => PHP_SAPI,
]);
`

const notesMigration = `<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('notes', function (Blueprint $table) {
            $table->id();
            $table->string('body');
        });
    }
};
`

// laravelReport is what the app's /portby route answers.
type laravelReport struct {
	URL      string   `json:"url"`
	Database string   `json:"database"`
	Notes    []string `json:"notes"`
	Workers  string   `json:"workers"`
	SAPI     string   `json:"sapi"`
}

func TestARealLaravelAppIsServedAtEachWorktreesDomainWithItsOwnDatabase(t *testing.T) {
	php, composer, app := os.Getenv("PORTBY_PHP"), os.Getenv("PORTBY_COMPOSER"), os.Getenv("PORTBY_LARAVEL_APP")
	for name, engine := range realEngines(t) {
		t.Run(name, func(t *testing.T) {
			if php == "" || composer == "" || app == "" {
				t.Skip("set PORTBY_PHP, PORTBY_COMPOSER and PORTBY_LARAVEL_APP to run a real Laravel app")
			}
			t.Parallel()
			d, prefix := realEngineDaemon(t, engine)
			removeContainer(t, engine, prefix+"-mysql-8.4")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			if err := engine.Pull(ctx, "docker.io/library/mysql:8.4"); err != nil {
				t.Fatalf("pulling mysql:8.4: %v", err)
			}
			realLaravelTools(t, d, php, composer)
			project := "lara-real-" + name
			db := strings.ReplaceAll(project, "-", "_")
			repo := realLaravelRepo(t, project, app)

			d.MustRunCLI(repo.Dir, "link", "--yes")
			if setup := waitForSetup(t, d, repo.Dir); setup.State != api.SetupReady {
				t.Fatalf("the main Worktree's setup is %+v, want Ready", setup)
			}
			main := laravelAt(t, d, "https://"+project+".localhost/portby")
			want := laravelReport{URL: "https://" + project + ".localhost", Database: db, Notes: []string{}, Workers: "8", SAPI: "cli-server"}
			if !reflect.DeepEqual(main, want) {
				t.Fatalf("the main Worktree's app reports %+v, want %+v", main, want)
			}
			if res := d.Get("https://" + project + ".localhost/portby.txt"); res.StatusCode != http.StatusOK || res.Body != "served from public/\n" {
				t.Errorf("GET /portby.txt = %d %q, want the file in public/", res.StatusCode, res.Body)
			}
			myExec(t, dialMySQL(t, d, 3306, db), "INSERT INTO notes (body) VALUES ('written in main')")

			wt := repo.AddWorktree("feature")
			if setup := waitForSetup(t, d, wt.Dir); setup.State != api.SetupReady {
				t.Fatalf("the Worktree's setup is %+v, want Ready", setup)
			}
			feature := laravelAt(t, d, "https://feature."+project+".localhost/portby")
			want = laravelReport{URL: "https://feature." + project + ".localhost", Database: db + "__feature",
				Notes: []string{"written in main"}, Workers: "8", SAPI: "cli-server"}
			if !reflect.DeepEqual(feature, want) {
				t.Fatalf("the Worktree's app reports %+v, want %+v", feature, want)
			}
			// The Worktree's database is a copy: writes stay in it.
			myExec(t, dialMySQL(t, d, 3306, db+"__feature"), "INSERT INTO notes (body) VALUES ('written in feature')")
			if got := laravelAt(t, d, "https://"+project+".localhost/portby").Notes; !reflect.DeepEqual(got, []string{"written in main"}) {
				t.Errorf("after a write in the Worktree, the main Worktree's notes are %q", got)
			}
		})
	}
}

// laravelAt GETs the app's /portby route at url.
func laravelAt(t *testing.T, d *testkit.Daemon, url string) laravelReport {
	t.Helper()
	res := d.Get(url)
	var r laravelReport
	if res.StatusCode != http.StatusOK || json.Unmarshal([]byte(res.Body), &r) != nil {
		t.Fatalf("GET %s = %d:\n%s", url, res.StatusCode, res.Body)
	}
	return r
}

// realLaravelTools puts php and a composer running the phar with it on the
// daemon's PATH. Composer uses the user's cache, so it installs offline.
func realLaravelTools(t *testing.T, d *testkit.Daemon, php, composer string) {
	t.Helper()
	if err := os.Symlink(php, filepath.Join(d.Bin, "php")); err != nil {
		t.Fatal(err)
	}
	cache := os.Getenv("PORTBY_COMPOSER_CACHE")
	if cache == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			t.Fatal(err)
		}
		cache = filepath.Join(dir, "composer")
	}
	testkit.WriteExecutable(t, d.Bin, "composer",
		"COMPOSER_CACHE_DIR="+shellQuoted(cache)+" exec php "+shellQuoted(composer)+" --no-interaction \"$@\"\n")
}

func shellQuoted(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// realLaravelRepo is a git repository holding a copy of the Laravel app at
// app as a fresh clone has it, using MySQL, migrated by its own setup step.
func realLaravelRepo(t *testing.T, name, app string) *gitfixture.Repo {
	t.Helper()
	repo := gitfixture.New(t, name)
	skip := map[string]bool{"vendor": true, "node_modules": true, ".env": true, "package.json": true,
		"package-lock.json": true, "vite.config.js": true, "public/hot": true, "public/storage": true, ".git": true}
	err := filepath.WalkDir(app, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(app, path)
		switch {
		case rel == ".":
			return nil
		case skip[filepath.ToSlash(rel)], e.Type()&fs.ModeSymlink != 0,
			strings.HasPrefix(rel, "storage/logs/") && e.Name() != ".gitignore",
			strings.HasSuffix(rel, ".sqlite"):
			if e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		case e.IsDir():
			return os.MkdirAll(filepath.Join(repo.Dir, rel), 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(repo.Dir, rel), data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("copying the Laravel app %s: %v", app, err)
	}
	example := readFile(t, filepath.Join(repo.Dir, ".env.example"))
	example = strings.Replace(example, "DB_CONNECTION=sqlite", "DB_CONNECTION=mysql", 1)
	return repo.
		WriteFile(".env.example", example).
		WriteFile("routes/web.php", laravelRoutes).
		WriteFile("database/migrations/2026_01_01_000000_create_notes_table.php", notesMigration).
		WriteFile("public/portby.txt", "served from public/\n").
		WriteFile("portby.toml", "on_create = [\"php artisan migrate --force\"]\n").
		Commit("a Laravel app")
}
