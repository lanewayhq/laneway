package e2e

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// A Laravel-shaped Project for the Laravel Recipe through Seam 1: its files,
// and stub php, composer and pnpm commands standing in for the real ones.

// laravelEnvExample is a fresh Laravel app's .env.example, using MySQL and
// caching in Redis.
const laravelEnvExample = `APP_NAME=Laravel
APP_ENV=local
APP_KEY=
APP_URL=http://localhost

DB_CONNECTION=mysql
DB_HOST=127.0.0.1
DB_PORT=3306
DB_DATABASE=laravel
DB_USERNAME=root
DB_PASSWORD=

SESSION_DRIVER=database
QUEUE_CONNECTION=database
CACHE_STORE=redis

REDIS_HOST=127.0.0.1
REDIS_PASSWORD=null
REDIS_PORT=6379

MAIL_MAILER=log
MAIL_HOST=127.0.0.1
MAIL_PORT=2525
`

// laravelRepo is a freshly cloned Laravel-shaped Project: no .env, no
// vendor/ or node_modules/, with pnpm's lockfile and an asset in public/.
func laravelRepo(t *testing.T, name string) *gitfixture.Repo {
	t.Helper()
	return gitfixture.New(t, name).
		WriteFile("artisan", "#!/usr/bin/env php\n<?php // Laravel's console\n").
		WriteFile(".gitignore", ".env\n/vendor\n/node_modules\n/public/storage\n").
		WriteFile(".env.example", laravelEnvExample).
		WriteFile("composer.json", `{"require": {"php": "^8.2"}}`+"\n").
		WriteFile("package.json", `{"private": true}`+"\n").
		WriteFile("pnpm-lock.yaml", "lockfileVersion: '9.0'\n").
		WriteFile("public/index.php", "<?php // the front controller\n").
		WriteFile("public/build/app.css", "body { color: teal }\n").
		WriteFile("storage/app/public/welcome.txt", "a file on the public disk\n").
		Commit("a fresh Laravel app")
}

// laravelStubs puts stub php, composer, pnpm and node on the daemon's PATH.
// `php artisan serve` runs a test server reporting its own arguments,
// PHP_CLI_SERVER_WORKERS and PHP_INI_SCAN_DIR as its name; key:generate and storage:link do what
// Laravel's do to the files; composer and pnpm leave a marker of their run.
func laravelStubs(t *testing.T, d *testkit.Daemon) {
	t.Helper()
	server := testkit.TestServer(t)
	testkit.WriteExecutable(t, d.Bin, "php", fmt.Sprintf(`case "$1 $2" in
"-r "*) printf '%%s\n' 8.4.2 Core mbstring; exit 0 ;;
"artisan serve")
	shift 2
	for a in "$@"; do case "$a" in --port=*) port="${a#--port=}" ;; esac; done
	exec %s --port="$port" --name "artisan serve workers=$PHP_CLI_SERVER_WORKERS ini=$PHP_INI_SCAN_DIR $*" ;;
"artisan queue:work") echo "queue:work is working"; exec sleep 600 ;;
"artisan key:generate") sed -i 's/^APP_KEY=.*/APP_KEY=base64:generated-by-key-generate/' .env ;;
"artisan storage:link") [ -e public/storage ] || ln -s ../storage/app/public public/storage ;;
*) echo "stub php: unexpected $*" >&2; exit 1 ;;
esac
`, server))
	testkit.WriteExecutable(t, d.Bin, "composer", `[ "$1" = install ] && mkdir -p vendor && echo "composer $*" > vendor/autoload.php`+"\n")
	testkit.WriteExecutable(t, d.Bin, "pnpm", `mkdir -p node_modules && echo "pnpm $*" > node_modules/.installed-by`+"\n")
	testkit.StubNode(t, d.Bin, "v22.11.0")
}

func TestLinkingAFreshLaravelAppSetsItUpAndServesItWithArtisanServe(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	laravelStubs(t, d)
	repo := laravelRepo(t, "lara-main")

	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	// .env is .env.example, patched, with a new app key.
	env := filepath.Join(repo.Dir, ".env")
	for key, want := range map[string]string{
		"APP_NAME":       "Laravel",
		"APP_KEY":        "base64:generated-by-key-generate",
		"APP_URL":        "https://lara-main.localhost",
		"DB_CONNECTION":  "mysql",
		"DB_HOST":        d.ServiceHost(),
		"DB_PORT":        "3306",
		"DB_DATABASE":    "lara_main",
		"DB_USERNAME":    "laneway",
		"DB_PASSWORD":    "laneway",
		"REDIS_HOST":     d.ServiceHost(),
		"REDIS_PORT":     "6379",
		"REDIS_PASSWORD": "laneway",
		"MAIL_MAILER":    "smtp",
		"MAIL_HOST":      d.ServiceHost(),
		"MAIL_PORT":      "1025",
	} {
		if got := envValue(t, env, key); got != want {
			t.Errorf("%s=%q in .env, want %q", key, got, want)
		}
	}
	for _, key := range []string{"REDIS_PREFIX", "CACHE_PREFIX"} {
		assertNotContains(t, readFile(t, env), key+"=")
	}
	// Composer's and pnpm's packages, and the public/storage link.
	assertFile(t, filepath.Join(repo.Dir, "vendor", "autoload.php"), "composer install\n")
	assertFile(t, filepath.Join(repo.Dir, "node_modules", ".installed-by"), "pnpm install\n")
	if res := d.Get("https://lara-main.localhost/storage/welcome.txt"); res.Body != "a file on the public disk\n" {
		t.Errorf("GET /storage/welcome.txt = %d %q, want the file through the storage link", res.StatusCode, res.Body)
	}

	web := d.GetServer("https://lara-main.localhost/")
	for _, want := range []string{"artisan serve workers=8 ", "--host=127.0.0.1", "--port=" + web.ListenPort, "--no-reload"} {
		if !strings.Contains(web.Name, want) {
			t.Errorf("the web Process is %q, want %q in it", web.Name, want)
		}
	}
	// PHP's own scan dir, plus Laneway's ini turning off OPcache's
	// revalidation delay, so an edited view shows on the next request.
	ini := regexp.MustCompile(` ini=(\S*) `).FindStringSubmatch(web.Name)
	if ini == nil || !strings.HasPrefix(ini[1], ":/") {
		t.Fatalf("the web Process is %q, want PHP_INI_SCAN_DIR=:<Laneway's dir>", web.Name)
	}
	inis, _ := filepath.Glob(filepath.Join(ini[1][1:], "*.ini"))
	var settings string
	for _, f := range inis {
		settings += readFile(t, f)
	}
	assertContains(t, settings, "opcache.revalidate_freq=0\n")
	if css := d.Get("https://lara-main.localhost/build/app.css"); css.StatusCode != http.StatusOK || css.Body != "body { color: teal }\n" {
		t.Errorf("GET /build/app.css = %d %q, want the file from public/", css.StatusCode, css.Body)
	}
	waitForLogs(t, d, repo.Dir, []string{"logs", "queue"}, "queue:work is working")
}

func TestALaravelWorktreeSharesTheAppKeyButHasItsOwnURLDatabaseAndPrefixes(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	laravelStubs(t, d)
	repo := laravelRepo(t, "lara-wt")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	main := dialMySQL(t, d, 3306, "lara_wt")
	myExec(t, main, "CREATE TABLE notes (body text)")
	myExec(t, main, "INSERT INTO notes VALUES ('written in main')")

	wt := repo.AddWorktree("feature-a")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	env := filepath.Join(wt.Dir, ".env")
	for key, want := range map[string]string{
		"APP_KEY":      "base64:generated-by-key-generate", // the main Worktree's, not a new one
		"APP_URL":      "https://feature-a.lara-wt.localhost",
		"DB_DATABASE":  "lara_wt__feature_a",
		"REDIS_PREFIX": "lara-wt_feature-a_",
		"CACHE_PREFIX": "lara-wt_feature-a_",
	} {
		if got := envValue(t, env, key); got != want {
			t.Errorf("%s=%q in the Worktree's .env, want %q", key, got, want)
		}
	}
	copied := dialMySQL(t, d, 3306, "lara_wt__feature_a")
	if got := myColumn(t, copied, "SELECT body FROM notes"); !slices.Equal(got, []string{"written in main"}) {
		t.Errorf("the Worktree's database has notes %q, want a copy of the main Worktree's", got)
	}
	if _, err := os.Stat(filepath.Join(wt.Dir, "vendor", "autoload.php")); err != nil {
		t.Errorf("the Worktree has no Composer packages: %v", err)
	}
	if web := d.GetServer("https://feature-a.lara-wt.localhost/"); !strings.Contains(web.Name, "artisan serve") {
		t.Errorf("the Worktree's Domain reached %q, want its own artisan serve", web.Name)
	}
}
