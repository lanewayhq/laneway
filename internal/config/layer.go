package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/lanewayhq/laneway/internal/database"
	"github.com/lanewayhq/laneway/internal/engine"
	"github.com/lanewayhq/laneway/internal/envpatch"
)

// LocalFileName is the gitignored per-developer config file. It overrides
// laneway.toml.
const LocalFileName = "laneway.local.toml"

// Layer is one source of config: a Recipe's defaults, laneway.toml or
// laneway.local.toml. A nil pointer or a missing map entry means the layer
// doesn't set that key, so an earlier layer's value stands. Layers are merged
// by the Recipe Resolver, never by decoding several files into one value.
type Layer struct {
	// Source names where the layer came from, for error messages: the
	// file's path, or the Recipe.
	Source string `toml:"-"`
	// Warnings are problems that don't stop the layer loading, such as
	// unknown keys, each prefixed with Source.
	Warnings []string `toml:"-"`

	// Name is the Project name, overriding the directory name.
	Name *string `toml:"name"`
	// Processes are keyed by Process name ([processes.<name>] tables).
	Processes map[string]ProcessLayer `toml:"processes"`
	// StartTimeout is how long the Primary Process gets to accept
	// connections when its Worktree starts, such as "90s".
	StartTimeout *time.Duration `toml:"start_timeout"`
	// IdleTimeout is how long a Worktree may go without requests or open
	// connections before it is Idle and its lazy Processes stop, such as
	// "30m". It overrides the global idle_timeout.
	IdleTimeout *time.Duration `toml:"idle_timeout"`
	// Env is the Env Patch: the keys Laneway owns in each Worktree's .env,
	// keyed by env key ([env] table). Values are templates such as
	// "{url}"; see envpatch.Variables.
	Env map[string]string `toml:"env"`
	// LinkedEnv is more of the Env Patch, only for the Worktrees other than
	// the main one ([linked_env] table): the main Worktree's .env keeps its
	// own values of these keys. A key in both tables takes this one there.
	// Laravel's REDIS_PREFIX is one: each other Worktree needs its own, but
	// the main Worktree keeps Laravel's default.
	LinkedEnv map[string]string `toml:"linked_env"`
	// Services are the Services the Project uses, keyed by engine
	// ([services] table), each with its version: redis = "7". Every
	// Project using redis:7 shares one container.
	Services map[string]string `toml:"services"`
	// Worktree is how Worktrees are created, prepared and removed
	// ([worktree] table).
	Worktree *WorktreeLayer `toml:"worktree"`
	// Database is the Project's database ([database] table).
	Database *DatabaseLayer `toml:"database"`
	// PluginVersions pin Plugins to version ranges, by Plugin name
	// ([plugin_versions] table; see plugins.go).
	PluginVersions map[string]string `toml:"plugin_versions"`
	// Plugins are Plugins the Project uses, by name, besides those whose
	// Recipe, Integrations or Engines it uses: their hooks fire in it. A
	// machine-wide Plugin may only be named in laneway.local.toml (or the
	// global config), never in laneway.toml (see Layer.Personal).
	Plugins []string `toml:"plugins"`
}

// Personal reports whether the layer is the developer's own config,
// laneway.local.toml, rather than committed config or a Recipe's: only
// personal config may turn on a machine-wide Plugin.
func (l Layer) Personal() bool {
	return filepath.Base(l.Source) == LocalFileName
}

// pluginName is what a Plugin's name looks like (see plugin.Manifest.Name).
var pluginName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// checkPluginNames checks the names a plugins key lists.
func checkPluginNames(names []string) error {
	for i, n := range names {
		if !pluginName.MatchString(n) {
			return fmt.Errorf("plugins[%d]: %q isn't a Plugin name: use lowercase letters, digits and dashes, starting with a letter", i, n)
		}
	}
	return nil
}

// WorktreeLayer is the [worktree] table: where `lane worktree add` creates
// worktrees and what Laneway does when a Worktree is prepared and removed.
type WorktreeLayer struct {
	// Root is the directory `lane worktree add` creates worktrees in. A
	// relative path is relative to the main Worktree's directory.
	Root *string `toml:"root"`
	// Copy are paths, relative to the Worktree, copied from the main
	// Worktree into each other Worktree when it is prepared, before anything
	// else: gitignored files such as AGENTS.md, which a new worktree (only
	// committed files) doesn't have. A directory is copied with everything
	// in it, files the Worktree already has are kept, and a path the main
	// Worktree doesn't have is skipped. Paths from every layer are copied.
	Copy []string `toml:"copy"`
	// OnCreate are setup steps: shell commands run in a Worktree's
	// directory, in order, once its Env Patch is applied. The Worktree is
	// Ready when they have all succeeded. They run after the Recipe's.
	OnCreate []string `toml:"on_create"`
	// OnRemove are shell commands run in the main Worktree's directory, in
	// order, when a Worktree is removed. They run after the Recipe's.
	OnRemove []string `toml:"on_remove"`
	// InheritSetup set to false drops the Recipe's on_create and on_remove
	// steps, leaving only the Project's.
	InheritSetup *bool `toml:"inherit_setup"`
}

// movedKeys are the top-level keys that moved into the [worktree] table, by
// their key there.
var movedKeys = map[string]string{
	"worktree_root": "root",
	"copy":          "copy",
	"on_create":     "on_create",
	"on_remove":     "on_remove",
	"inherit_setup": "inherit_setup",
}

// DatabaseLayer is the [database] table: the database each Worktree gets.
type DatabaseLayer struct {
	// Engine is the database engine: sqlite, or an Engine with database
	// commands (database.EngineNames), such as "postgres". A Project with
	// no engine has no database. A Service engine (postgres, mysql) runs as
	// the Service of that engine, at the version in [services], else the
	// engine's default.
	Engine *string `toml:"engine"`
	// Path is the SQLite database file, relative to the Worktree, such as
	// "database/database.sqlite". Only SQLite has one.
	Path *string `toml:"path"`
	// Worktree is how a Worktree other than the main one gets its
	// database, one of DatabaseModes: a copy of the main Worktree's
	// (clone, the default), an empty one (fresh), or the main Worktree's
	// own (shared).
	Worktree *string `toml:"worktree"`
	// Test is whether each Worktree also gets an empty Test Database,
	// <database>_test in the same Service, one of DatabaseTests: on, off,
	// or auto (the default; a Recipe decides, off without one). Only for
	// databases in a Service, not SQLite.
	Test *string `toml:"test"`
}

// DatabaseModes are the values database.worktree accepts.
var DatabaseModes = []string{"clone", "fresh", "shared"}

// DatabaseTests are the values database.test accepts.
var DatabaseTests = []string{"on", "off", "auto"}

// ProcessLayer is one [processes.<name>] table.
type ProcessLayer struct {
	// Command is run with `sh -c` in the Worktree. Every `{port}` in it is
	// replaced with the port Laneway picked for the Process, which is also
	// in $PORT. In an HTTP Process, every `{vite_config}` is replaced with
	// the path of a Vite config Laneway writes around the Worktree's own, for
	// `vite --config {vite_config}`: Vite's live reload then goes through
	// the Process's host on Laneway's proxy. Every `{php_ini_dir}` is replaced
	// with the directory of PHP settings Laneway writes for development, for
	// `PHP_INI_SCAN_DIR=$PHP_INI_SCAN_DIR:{php_ini_dir} php …`.
	Command *string `toml:"command"`
	// Primary marks the Process that serves the Worktree's Domain. A Project
	// with a single Process doesn't need to set it.
	Primary *bool `toml:"primary"`
	// Disabled turns off a Process an earlier layer declared, such as one
	// from the Recipe. A later layer can set it back to false.
	Disabled *bool `toml:"disabled"`
	// StartMode is when the Process runs: "lazy" (the default) starts it
	// with its Worktree on the first request and stops it when the
	// Worktree is Idle, "always" keeps it running from when the daemon
	// starts, and "manual" only runs it through `lane up <process>`.
	StartMode *string `toml:"start_mode"`
	// HTTP marks a Process that serves HTTP on its {port}. Other than the
	// Primary Process, which always does, it is served at
	// <process>.<domain>. Processes that aren't HTTP get no port.
	HTTP *bool `toml:"http"`
	// StaticDir is a directory, relative to the Worktree, whose files
	// Laneway serves itself for requests to this HTTP Process, such as
	// Laravel's "public": a GET or HEAD for a path that is an existing file
	// there (not a directory, not a .php script) never reaches the Process.
	StaticDir *string `toml:"static_dir"`
}

// StartModes are the valid values of processes.<name>.start_mode.
var StartModes = []string{"lazy", "always", "manual"}

// LoadLayers reads dir's laneway.toml and then laneway.local.toml, in the
// order they apply. Missing files are skipped.
func LoadLayers(dir string) ([]Layer, error) {
	var layers []Layer
	for _, name := range []string{FileName, LocalFileName} {
		l, err := LoadLayer(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		layers = append(layers, l)
	}
	return layers, nil
}

// LoadLayer reads one config file. A missing file gives an error matching
// fs.ErrNotExist.
func LoadLayer(path string) (Layer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Layer{}, err
	}
	return ParseLayer(path, data)
}

// ParseLayer decodes and validates TOML config. source names it in errors:
// a file's path, or a Recipe for its defaults. Errors name the source and
// the key at fault.
func ParseLayer(source string, data []byte) (Layer, error) {
	l := Layer{Source: source}
	md, err := toml.Decode(string(data), &l)
	if err != nil {
		return Layer{}, decodeError(source, err)
	}
	// The decoder silently skips a non-table value for a map field.
	for _, key := range append([]toml.Key{{"processes"}, {"env"}, {"linked_env"}, {"database"}, {"services"}, {"plugin_versions"}}, processKeys(l)...) {
		if t := md.Type(key...); t != "" && t != "Hash" {
			return Layer{}, fmt.Errorf("%s: %s must be a table, not a %s", source, key, strings.ToLower(t))
		}
	}
	// A key that moved would otherwise only warn, and the Worktrees would
	// silently go without their setup.
	for _, key := range md.Undecoded() {
		if moved, ok := movedKeys[key.String()]; ok {
			return Layer{}, fmt.Errorf("%s: %s moved into the [worktree] table: write it as %s under [worktree]", source, key, moved)
		}
	}
	if err := l.validate(); err != nil {
		return Layer{}, fmt.Errorf("%s: %w", source, err)
	}
	// Unknown keys only warn, so a config written for a newer Laneway, or
	// with a typo, still loads. Only the outermost unknown key is reported.
	unknown := map[string]bool{}
	for _, key := range md.Undecoded() {
		unknown[key.String()] = true
		if len(key) > 1 && unknown[key[:len(key)-1].String()] {
			continue
		}
		l.Warnings = append(l.Warnings, fmt.Sprintf("%s: unknown key %q", source, key.String()))
	}
	return l, nil
}

func processKeys(l Layer) []toml.Key {
	var keys []toml.Key
	for name := range l.Processes {
		keys = append(keys, toml.Key{"processes", name})
	}
	return keys
}

// CheckEngines checks what in the layer needs an Engine Laneway can run,
// engines: the Services it declares, database.engine, and the Service
// variables its templates use. Engines come from Plugins, so this is checked
// once they are known, when the Recipe Resolver merges the layer, not when
// it's parsed. Naming an Engine among catalog, the Engines Laneway hasn't
// installed but can fetch from the Catalog, is an EngineNotInstalled error
// rather than an unknown Engine. Errors name the layer's Source and the key
// at fault.
func (l Layer) CheckEngines(engines engine.Set, catalog []string) error {
	if err := l.checkEngines(engines, catalog); err != nil {
		return fmt.Errorf("%s: %w", l.Source, err)
	}
	return nil
}

func (l Layer) checkEngines(engines engine.Set, catalog []string) error {
	// fetchable reports whether the Engine name is one Laneway can fetch.
	fetchable := func(name string) bool {
		_, installed := engines.Lookup(name)
		return !installed && slices.Contains(catalog, name)
	}
	if db := l.Database; db != nil && db.Engine != nil {
		if names := database.EngineNames(engines); !slices.Contains(names, *db.Engine) {
			if fetchable(*db.Engine) {
				return fmt.Errorf("database.engine: %w", EngineNotInstalled(*db.Engine))
			}
			return fmt.Errorf("database.engine must be %s, not %q", oneOf(names), *db.Engine)
		}
	}
	serviceVars := engines.Variables()
	for _, env := range []struct {
		table     string
		templates map[string]string
	}{{"env", l.Env}, {"linked_env", l.LinkedEnv}} {
		for _, k := range slices.Sorted(maps.Keys(env.templates)) {
			tmpl := env.templates[k]
			err := envpatch.CheckTemplate(tmpl, serviceVars)
			if err == nil {
				continue
			}
			// A Service variable of an Engine Laneway can fetch, such as
			// {redis_port}, isn't unknown.
			for _, name := range catalog {
				if fetchable(name) && slices.ContainsFunc(engine.Assumed(name).Variables(), func(v string) bool {
					return envpatch.Uses(tmpl, v)
				}) {
					err = EngineNotInstalled(name)
					break
				}
			}
			return fmt.Errorf("%s.%s: %w", env.table, key(k), err)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(l.Services)) {
		if _, ok := engines.Lookup(name); !ok {
			if fetchable(name) {
				return fmt.Errorf("services.%s: %w", key(name), EngineNotInstalled(name))
			}
			return fmt.Errorf("services.%s: Laneway has no Service %q; the Services are %s",
				key(name), name, strings.Join(engines.Names(), ", "))
		}
	}
	return nil
}

// EngineNotInstalled is the error for config naming the Engine name, which
// Laneway hasn't installed but can fetch from the Catalog.
func EngineNotInstalled(name string) error {
	return fmt.Errorf("the %s Engine isn't installed; run `lane doctor` to fetch it from the Catalog", name)
}

// validate checks what can be checked within one layer. Rules that need
// the merged config, such as every Process having a command, belong to the
// Recipe Resolver.
func (l Layer) validate() error {
	if l.Name != nil && strings.TrimSpace(*l.Name) == "" {
		return errors.New("name is empty")
	}
	if l.StartTimeout != nil && *l.StartTimeout <= 0 {
		return fmt.Errorf("start_timeout must be positive, such as \"90s\", not %s", *l.StartTimeout)
	}
	if l.IdleTimeout != nil && *l.IdleTimeout <= 0 {
		return fmt.Errorf("idle_timeout must be positive, such as \"30m\", not %s", *l.IdleTimeout)
	}
	if err := l.Database.validate(); err != nil {
		return err
	}
	if err := checkPluginNames(l.Plugins); err != nil {
		return err
	}
	if err := checkPluginVersions(l.PluginVersions); err != nil {
		return err
	}
	if err := l.Worktree.validate(); err != nil {
		return err
	}
	for _, env := range []struct {
		table     string
		templates map[string]string
	}{{"env", l.Env}, {"linked_env", l.LinkedEnv}} {
		for _, k := range slices.Sorted(maps.Keys(env.templates)) {
			if !envKey.MatchString(k) {
				return fmt.Errorf("%s.%s: an env key must be letters, digits and _ and not start with a digit", env.table, key(k))
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(l.Services)) {
		if !serviceVersion.MatchString(l.Services[name]) {
			return fmt.Errorf("services.%s must be a version, such as \"7\" or \"7.2\", not %q", name, l.Services[name])
		}
	}
	var primaries []string
	for _, name := range slices.Sorted(maps.Keys(l.Processes)) {
		p := l.Processes[name]
		if p.Command != nil && strings.TrimSpace(*p.Command) == "" {
			return fmt.Errorf("processes.%s.command is empty", name)
		}
		if p.StartMode != nil && !slices.Contains(StartModes, *p.StartMode) {
			return fmt.Errorf("processes.%s.start_mode must be lazy, always or manual, not %q", name, *p.StartMode)
		}
		if p.StaticDir != nil {
			if err := insideWorktree("processes."+name+".static_dir", *p.StaticDir, "public"); err != nil {
				return err
			}
		}
		if p.Primary != nil && *p.Primary {
			primaries = append(primaries, "processes."+name+".primary")
		}
	}
	if len(primaries) > 1 {
		return fmt.Errorf("only one Process can be the Primary Process, but %s are all true",
			strings.Join(primaries, " and "))
	}
	return nil
}

func (wt *WorktreeLayer) validate() error {
	if wt == nil {
		return nil
	}
	if wt.Root != nil && strings.TrimSpace(*wt.Root) == "" {
		return errors.New("worktree.root is empty")
	}
	for i, path := range wt.Copy {
		if !filepath.IsLocal(filepath.FromSlash(path)) {
			return fmt.Errorf("worktree.copy[%d]: %q isn't a path inside the Worktree; use one relative to it, such as \"AGENTS.md\"", i, path)
		}
	}
	for _, steps := range []struct {
		key   string
		steps []string
	}{{"on_create", wt.OnCreate}, {"on_remove", wt.OnRemove}} {
		for i, step := range steps.steps {
			if strings.TrimSpace(step) == "" {
				return fmt.Errorf("worktree.%s[%d] is empty", steps.key, i)
			}
		}
	}
	return nil
}

// serviceVersion is what a Service's version may look like: an image tag.
var serviceVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (db *DatabaseLayer) validate() error {
	if db == nil {
		return nil
	}
	if db.Worktree != nil && !slices.Contains(DatabaseModes, *db.Worktree) {
		return fmt.Errorf("database.worktree must be %s, not %q", oneOf(DatabaseModes), *db.Worktree)
	}
	if db.Test != nil && !slices.Contains(DatabaseTests, *db.Test) {
		return fmt.Errorf("database.test must be %s, not %q", oneOf(DatabaseTests), *db.Test)
	}
	if db.Path != nil {
		return insideWorktree("database.path", *db.Path, "database/database.sqlite")
	}
	return nil
}

// insideWorktree checks that the value of key is a path inside the Worktree,
// relative to it, such as example.
func insideWorktree(key, value, example string) error {
	switch path := filepath.Clean(value); {
	case strings.TrimSpace(value) == "":
		return fmt.Errorf("%s is empty", key)
	case filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../"):
		return fmt.Errorf("%s must be inside the Worktree, relative to it, such as %q, not %q", key, example, value)
	}
	return nil
}

// oneOf lists values as "a, b or c".
func oneOf(values []string) string {
	if len(values) == 1 {
		return values[0]
	}
	return strings.Join(values[:len(values)-1], ", ") + " or " + values[len(values)-1]
}

// envKey is what phpdotenv accepts as a key.
var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

// typeError matches the decoder's errors for a value of the wrong type.
var typeError = regexp.MustCompile(`^toml: line (\d+) \(last key "([^"]+)"\): (.*)$`)

// decodeError rewrites a TOML error as "<source>: line N: <key>: <problem>".
func decodeError(source string, err error) error {
	var pe toml.ParseError
	if errors.As(err, &pe) && pe.LastKey != "" {
		return fmt.Errorf("%s: line %d: %s: %s", source, pe.Position.Line, pe.LastKey, pe.Message)
	}
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: line %d: %s", source, pe.Position.Line, pe.Message)
	}
	if m := typeError.FindStringSubmatch(err.Error()); m != nil {
		return fmt.Errorf("%s: line %s: %s: %s", source, m[1], m[2], m[3])
	}
	return fmt.Errorf("%s: %w", source, err)
}
