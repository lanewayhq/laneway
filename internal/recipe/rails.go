package recipe

import (
	"bufio"
	"bytes"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/dotenv"
	"github.com/lanewayhq/laneway/internal/service"
)

// Rails is the Rails Recipe: a Project whose Gemfile declares the rails gem
// and that has a Rails app's config/application.rb. It is the second Recipe
// in core, there to test the Recipe model before it becomes a Plugin API
// (ADR-0007).
type Rails struct{}

func (Rails) Name() string { return "rails" }

func (Rails) Detect(dir string) bool {
	return exists(dir, filepath.Join("config", "application.rb")) && gemfileDeclares(dir, "rails", "railties")
}

// withEnv loads the Worktree's .env into the environment of the command
// after it. Rails doesn't read .env itself (only through the dotenv gem, which
// many apps don't use), so this is how the Env Patch, DATABASE_URL among it,
// reaches the app. Laneway writes its keys in a form sh reads back exactly
// (see dotenv.Quote).
const withEnv = "set -a; [ ! -f .env ] || . ./.env; set +a; "

// railsServer serves the app with Rails' own server, through the app's
// binstub, on the port Laneway gives the Primary Process.
const railsServer = withEnv + "exec bin/rails server -b 127.0.0.1 -p {port}"

func (Rails) Defaults(dir string) (config.Layer, error) {
	l := config.Layer{Source: "the rails Recipe", Processes: map[string]config.ProcessLayer{}}
	l.Processes["web"] = config.ProcessLayer{Command: ptr(railsServer), Primary: ptr(true)}
	l.Env = map[string]string{}
	railsDatabase(&l, dir)
	// A new Worktree has no gems installed yet. Laneway makes its database
	// itself, so there is no db:prepare.
	l.Worktree = &config.WorktreeLayer{OnCreate: []string{"bundle install"}}
	return l, nil
}

// railsAdapters maps the Active Record adapters Laneway has a database
// engine (database.Engines) for to that engine.
var railsAdapters = map[string]string{
	"sqlite3":    "sqlite",
	"postgresql": "postgres",
	"postgis":    "postgres",
	"mysql2":     "mysql",
	"trilogy":    "mysql",
}

// railsDatabase gives the Project the database its development config uses,
// the way Rails reads it: DATABASE_URL if the Project's env file sets it,
// else the development section of config/database.yml. The app reaches it
// through DATABASE_URL, which Rails prefers to database.yml, keeping the
// app's own adapter as its scheme.
func railsDatabase(l *config.Layer, dir string) {
	db, from, err := railsDevelopmentDatabase(dir)
	if err != nil {
		l.Warnings = append(l.Warnings, fmt.Sprintf("%s: can't read %s, so the Project's database is left alone: %v; "+
			"set [database] in %s to give it one of Laneway's", l.Source, from, err, config.FileName))
		return
	}
	if db.adapter == "" {
		return
	}
	engine, ok := railsAdapters[db.adapter]
	if !ok {
		known := slices.Sorted(maps.Keys(railsAdapters))
		l.Warnings = append(l.Warnings, fmt.Sprintf("%s: %s uses the %q adapter, which Laneway has no database for "+
			"(it knows %s and %s), so the Project's database is left alone; "+
			"set [database] in %s to use one of Laneway's", l.Source, from, db.adapter,
			strings.Join(known[:len(known)-1], ", "), known[len(known)-1], config.FileName))
		return
	}
	if _, inService := service.Lookup(engine); !inService {
		// A file in each Worktree.
		l.Database = &config.DatabaseLayer{Engine: ptr(engine), Path: ptr(railsSQLitePath(dir, db.database))}
		l.Env["DATABASE_URL"] = db.adapter + ":{db}"
		return
	}
	l.Database = &config.DatabaseLayer{Engine: ptr(engine)}
	l.Env["DATABASE_URL"] = db.adapter + "://{db_username}:{db_password}@{db_host}:{db_port}/{db}"
}

// railsDB is the database config Rails uses in development.
type railsDB struct {
	// adapter is the Active Record adapter, such as postgresql; "" if
	// the Project configures no database.
	adapter string
	// database is the database's name or, for SQLite, its file.
	database string
}

// railsDevelopmentDatabase reads the Project's development database config,
// and from is where it comes from, for messages. DATABASE_URL in .env (or,
// before there is one, .env.example) wins over config/database.yml, as it
// does in Rails.
func railsDevelopmentDatabase(dir string) (db railsDB, from string, err error) {
	if raw, ok := railsEnv(dir, "DATABASE_URL"); ok {
		from = "DATABASE_URL"
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" {
			return railsDB{}, from, fmt.Errorf("%q is not a database URL", raw)
		}
		db.adapter = u.Scheme
		if db.database = u.Opaque; db.database == "" {
			db.database = strings.TrimPrefix(u.Path, "/")
			if u.Host == "" {
				db.database = u.Path
			}
		}
		return db, from, nil
	}
	db, err = railsYmlDatabase(dir, "development")
	return db, railsDatabaseYml, err
}

// railsDatabaseYml is the Rails app's database config, relative to it.
const railsDatabaseYml = "config/database.yml"

// railsYmlDatabase reads the database config/database.yml gives the Rails
// environment env (development, test): with several databases, the primary
// one, the app's own. It is empty if the file has none for env.
func railsYmlDatabase(dir, env string) (railsDB, error) {
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(railsDatabaseYml)))
	if os.IsNotExist(err) {
		return railsDB{}, nil
	}
	if err != nil {
		return railsDB{}, err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(withoutERB(data), &doc); err != nil {
		return railsDB{}, err
	}
	config, _ := doc[env].(map[string]any)
	if _, ok := config["adapter"]; !ok {
		if primary, ok := config["primary"].(map[string]any); ok {
			config = primary
		}
	}
	var db railsDB
	db.adapter, _ = config["adapter"].(string)
	db.database, _ = config["database"].(string)
	return db, nil
}

// erbTag matches an ERB tag in database.yml, such as <%= ENV["X"] %>.
var erbTag = regexp.MustCompile(`(?s)<%.*?%>`)

// withoutERB is database.yml with its ERB tags taken out, so it parses as
// YAML: an output tag becomes the word ERB, any other tag nothing. Laneway
// only reads the adapter and database, which are rarely computed.
func withoutERB(data []byte) []byte {
	return erbTag.ReplaceAllFunc(data, func(tag []byte) []byte {
		if bytes.HasPrefix(tag, []byte("<%=")) {
			return []byte("ERB")
		}
		return nil
	})
}

// railsEnv is the value of key in the Project's .env or, before there is one
// (Laneway creates it from .env.example), its .env.example.
func railsEnv(dir, key string) (string, bool) {
	for _, name := range []string{".env", ".env.example"} {
		if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			v, ok := dotenv.Parse(data).Get(key)
			v = strings.TrimSpace(v)
			return v, ok && v != ""
		}
	}
	return "", false
}

// railsSQLitePath is the SQLite file relative to the Worktree: file, if it
// is inside the Worktree, else Rails' default, storage/development.sqlite3.
// (After the Env Patch, DATABASE_URL names the Worktree's own file by its
// absolute path.)
func railsSQLitePath(dir, file string) string {
	const def = "storage/development.sqlite3"
	if file == "" {
		return def
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return def
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(abs, file)
	}
	rel, err := filepath.Rel(abs, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return def
	}
	return filepath.ToSlash(rel)
}

// gemLine matches a Gemfile line declaring a gem: gem "name", ….
var gemLine = regexp.MustCompile(`^\s*gem\s*\(?\s*["']([^"']+)["']`)

// gemfileDeclares reports whether the Gemfile in dir declares any of gems.
func gemfileDeclares(dir string, gems ...string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "Gemfile"))
	if err != nil {
		return false
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		m := gemLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		for _, gem := range gems {
			if m[1] == gem {
				return true
			}
		}
	}
	return false
}
