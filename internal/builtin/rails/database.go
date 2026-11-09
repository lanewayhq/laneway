package main

import (
	"bytes"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lanewayhq/laneway/sdk"
)

// adapter is a database engine Laneway has for an Active Record adapter.
type adapter struct {
	engine string
	// file marks a database that is a file in each Worktree (SQLite),
	// rather than one in a Service.
	file bool
}

// adapters maps the Active Record adapters Laneway has a database engine for
// to that engine.
var adapters = map[string]adapter{
	"sqlite3":    {"sqlite", true},
	"postgresql": {"postgres", false},
	"postgis":    {"postgres", false},
	"mysql2":     {"mysql", false},
	"trilogy":    {"mysql", false},
}

// database gives the Project the database its development config uses, the
// way Rails reads it: DATABASE_URL if the Project's env file sets it, else
// the development section of config/database.yml. The app reaches it through
// DATABASE_URL, which Rails prefers to database.yml, keeping the app's own
// adapter as its scheme. It returns a warning for a database Laneway leaves
// alone.
func database(l *sdk.Layer, p sdk.Project) []string {
	db, from, err := developmentDatabase(p)
	if err != nil {
		return []string{fmt.Sprintf("can't read %s, so the Project's database is left alone: %v; "+
			"set [database] in laneway.toml to give it one of Laneway's", from, err)}
	}
	if db.adapter == "" {
		return nil
	}
	a, ok := adapters[db.adapter]
	if !ok {
		known := slices.Sorted(maps.Keys(adapters))
		return []string{fmt.Sprintf("%s uses the %q adapter, which Laneway has no database for "+
			"(it knows %s and %s), so the Project's database is left alone; "+
			"set [database] in laneway.toml to use one of Laneway's", from, db.adapter,
			strings.Join(known[:len(known)-1], ", "), known[len(known)-1])}
	}
	if a.file {
		l.Database = &sdk.Database{Engine: a.engine, Path: sqlitePath(p, db.database)}
		l.Env["DATABASE_URL"] = db.adapter + ":{db}"
		return nil
	}
	l.Database = &sdk.Database{Engine: a.engine}
	l.Env["DATABASE_URL"] = db.adapter + "://{db_username}:{db_password}@{db_host}:{db_port}/{db}"
	return nil
}

// railsDB is the database config Rails uses in an environment.
type railsDB struct {
	// adapter is the Active Record adapter, such as postgresql; "" if
	// the Project configures no database.
	adapter string
	// database is the database's name or, for SQLite, its file.
	database string
}

// developmentDatabase reads the Project's development database config, and
// from is where it comes from, for messages. DATABASE_URL in .env (or,
// before there is one, .env.example) wins over config/database.yml, as it
// does in Rails.
func developmentDatabase(p sdk.Project) (db railsDB, from string, err error) {
	raw, _ := p.EnvFile().Get("DATABASE_URL")
	if raw = strings.TrimSpace(raw); raw != "" {
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
	db, err = ymlDatabase(p, "development")
	return db, databaseYml, err
}

// databaseYml is the Rails app's database config, relative to it.
const databaseYml = "config/database.yml"

// ymlDatabase reads the database config/database.yml gives the Rails
// environment env (development, test): with several databases, the primary
// one, the app's own. It is empty if the file has none for env.
func ymlDatabase(p sdk.Project, env string) (railsDB, error) {
	if !p.Exists(databaseYml) {
		return railsDB{}, nil
	}
	data, err := p.ReadFile(databaseYml)
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

// erbTag matches an ERB tag in a Rails YAML config, such as
// <%= ENV["X"] %>.
var erbTag = regexp.MustCompile(`(?s)<%.*?%>`)

// withoutERB is a Rails YAML config with its ERB tags taken out, so it parses
// as YAML: an output tag becomes the word ERB, any other tag nothing. The
// Recipe only reads settings that are rarely computed.
func withoutERB(data []byte) []byte {
	return erbTag.ReplaceAllFunc(data, func(tag []byte) []byte {
		if bytes.HasPrefix(tag, []byte("<%=")) {
			return []byte("ERB")
		}
		return nil
	})
}

// sqlitePath is the SQLite file relative to the Worktree: file, if it is
// inside the Worktree, else Rails' default, storage/development.sqlite3.
// (After the Env Patch, DATABASE_URL names the Worktree's own file by its
// absolute path.)
func sqlitePath(p sdk.Project, file string) string {
	if rel, ok := p.Rel(file); ok {
		return rel
	}
	return "storage/development.sqlite3"
}
