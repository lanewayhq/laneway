package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// A Rails-shaped Project for the Rails Recipe through Seam 1: its files, and
// a stub mise resolving a stub Ruby (with Bundler) for it, standing in for
// the real ones.

// railsPostgresYml is a new Rails app's config/database.yml for Postgres.
const railsPostgresYml = `default: &default
  adapter: postgresql
  encoding: unicode
  max_connections: <%= ENV.fetch("RAILS_MAX_THREADS") { 5 } %>

development:
  <<: *default
  database: shop_development

test:
  <<: *default
  database: shop_test
`

// railsSQLiteYml is a new Rails app's config/database.yml for SQLite.
const railsSQLiteYml = `default: &default
  adapter: sqlite3
  max_connections: <%= ENV.fetch("RAILS_MAX_THREADS") { 5 } %>
  timeout: 5000

development:
  <<: *default
  database: storage/development.sqlite3
`

// railsRepo is a freshly cloned Rails-shaped Project using the database
// config databaseYml: no .env, no installed gems, its Ruby version in
// .ruby-version.
func railsRepo(t *testing.T, name, databaseYml string) *gitfixture.Repo {
	t.Helper()
	repo := gitfixture.New(t, name).
		WriteFile(".gitignore", ".env\n/vendor/bundle\n/storage/*.sqlite3\n").
		WriteFile(".ruby-version", "3.4.7\n").
		WriteFile("Gemfile", "source \"https://rubygems.org\"\n\nruby file: \".ruby-version\"\n\ngem \"rails\", \"~> 8.1.0\"\ngem \"puma\"\n").
		WriteFile("config/application.rb", "require_relative \"boot\"\nrequire \"rails/all\"\n\nmodule Shop\n  class Application < Rails::Application\n  end\nend\n").
		WriteFile("config/database.yml", databaseYml).
		WriteFile("bin/rails", "#!/usr/bin/env ruby\nAPP_PATH = File.expand_path(\"../config/application\", __dir__)\nrequire_relative \"../config/boot\"\nrequire \"rails/commands\"\n")
	if err := os.Chmod(filepath.Join(repo.Dir, "bin", "rails"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo.Commit("a fresh Rails app")
}

// railsStubs puts a stub mise on the daemon's PATH that resolves a stub Ruby
// 3.4.7 and Bundler, the way mise does from .ruby-version. `bin/rails server`
// runs a test server reporting its arguments, the Ruby that runs it and the
// DATABASE_URL it sees as its name; `bundle install` leaves a marker of its
// run.
func railsStubs(t *testing.T, d *testkit.Daemon) {
	t.Helper()
	server := testkit.TestServer(t)
	rubyBin := filepath.Join(d.Home, "mise-installs", "ruby", "3.4.7", "bin")
	testkit.WriteExecutable(t, rubyBin, "ruby", fmt.Sprintf(`case "$1 $2" in
"-e "*) printf 3.4.7; exit 0 ;;
*"/bin/rails server")
	shift 2
	args="$*"
	while [ $# -gt 0 ]; do case "$1" in -p) port="$2"; shift ;; esac; shift; done
	exec %s --port="$port" --name "rails server $args ruby=3.4.7 DATABASE_URL=$DATABASE_URL" ;;
*) echo "stub ruby: unexpected $*" >&2; exit 1 ;;
esac
`, server))
	testkit.WriteExecutable(t, rubyBin, "bundle", `[ "$1" = install ] && mkdir -p vendor/bundle && echo "bundle $* with $(command -v ruby)" > vendor/bundle/.installed-by`+"\n")
	testkit.StubMise(t, d.Bin, map[string]string{"PATH": rubyBin + ":" + d.Path})
}

func TestLinkingARailsAppServesItWithRailsServerOnItsOwnPostgresDatabase(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	railsStubs(t, d)
	repo := railsRepo(t, "rails-main", railsPostgresYml)

	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	url := fmt.Sprintf("postgresql://laneway:laneway@%s:5432/rails_main", d.ServiceHost())
	if got := envValue(t, filepath.Join(repo.Dir, ".env"), "DATABASE_URL"); got != url {
		t.Errorf("DATABASE_URL=%q in .env, want %q", got, url)
	}
	dialPostgres(t, d, 5432, "rails_main")
	// Bundler installed the gems, with the Ruby mise resolved.
	assertFile(t, filepath.Join(repo.Dir, "vendor", "bundle", ".installed-by"),
		"bundle install with "+filepath.Join(d.Home, "mise-installs", "ruby", "3.4.7", "bin", "ruby")+"\n")

	web := d.GetServer("https://rails-main.localhost/")
	want := fmt.Sprintf("rails server -b 127.0.0.1 -p %s ruby=3.4.7 DATABASE_URL=%s", web.ListenPort, url)
	if web.Name != want {
		t.Errorf("the web Process is %q, want %q", web.Name, want)
	}
}

func TestANewWorktreeOfARailsAppOnSQLiteGetsItsOwnDatabaseFile(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	railsStubs(t, d)
	repo := railsRepo(t, "rails-lite", railsSQLiteYml)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	mainDB := filepath.Join(repo.Dir, "storage", "development.sqlite3")
	if err := os.WriteFile(mainDB, []byte("main-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	wt := repo.AddWorktree("feat-x")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	db := filepath.Join(wt.Dir, "storage", "development.sqlite3")
	if got := envValue(t, filepath.Join(wt.Dir, ".env"), "DATABASE_URL"); got != "sqlite3:"+db {
		t.Errorf("DATABASE_URL=%q in the Worktree's .env, want sqlite3:%s", got, db)
	}
	assertFile(t, db, "main-data")
	web := d.GetServer("https://feat-x.rails-lite.localhost/")
	if !strings.HasSuffix(web.Name, "DATABASE_URL=sqlite3:"+db) {
		t.Errorf("the Worktree's web Process is %q, want it on DATABASE_URL=sqlite3:%s", web.Name, db)
	}
}

func TestDoctorReportsTheRubyMiseResolvesForARailsApp(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	railsStubs(t, d)
	repo := railsRepo(t, "rails-doctor", railsSQLiteYml)

	res := d.RunCLI(repo.Dir, "doctor")

	assertCheck(t, res.Stdout, "ok", "ruby",
		"3.4.7 at "+filepath.Join(d.Home, "mise-installs", "ruby", "3.4.7", "bin", "ruby")+" (through mise)")
}

func TestAWorktreeStartOfARailsAppReportsRubyOutsideItsGemfile(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	railsStubs(t, d)
	repo := railsRepo(t, "rails-old-ruby", railsSQLiteYml).
		WriteFile(".ruby-version", "3.5.0\n").
		Commit("move to Ruby 3.5")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	startByBrowsing(t, d, "rails-old-ruby.localhost")

	out := waitForLogs(t, d, repo.Dir, []string{"logs", "web"}, "Runtime warning")
	assertContains(t, out, `laneway: Runtime warning: Ruby 3.4.7 doesn't satisfy ruby file: ".ruby-version" in the Gemfile`)
	assertContains(t, out, "mise use ruby@3.5")
}

func TestExecOfBundlerWithoutRubySuggestsInstallingTheGemfilesRubyWithMise(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := railsRepo(t, "rails-no-ruby", railsSQLiteYml)
	d.MustRunCLI(repo.Dir, "link", "--yes")

	res := d.RunCLI(repo.Dir, "exec", "bundle", "exec", "rails", "db:migrate")

	if res.ExitCode != 127 {
		t.Errorf("exit code = %d, want 127\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, "bundle was not found through mise or on PATH")
	assertContains(t, res.Stderr, "`mise use ruby@3.4`")
}

func TestARailsAppsProcfileDevWatchersRunAsProcessesReadingItsEnv(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	railsStubs(t, d)
	repo := railsRepo(t, "rails-procfile", railsPostgresYml).
		WriteFile("Procfile.dev", "web: bin/rails server -p 3000\njs: echo \"js watching for DATABASE_URL=$DATABASE_URL\" && exec sleep 600\n").
		Commit("run the JS watcher with bin/dev")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	startByBrowsing(t, d, "rails-procfile.localhost")

	waitForLogs(t, d, repo.Dir, []string{"logs", "js"},
		fmt.Sprintf("js watching for DATABASE_URL=postgresql://laneway:laneway@%s:5432/rails_procfile", d.ServiceHost()))
}
