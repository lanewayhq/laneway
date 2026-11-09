// Package recipe is the Recipe Resolver: a pure function from a Project
// directory and its config layers to everything Laneway runs for the Project.
// The daemon reads config only through it.
package recipe

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/runtimes"
	"github.com/lanewayhq/laneway/internal/service"
)

// Input is what Resolve works from.
type Input struct {
	// Dir is the Project directory Recipes detect from.
	Dir string
	// Layers are applied over the Recipe's defaults in order, later
	// winning: normally laneway.toml, then laneway.local.toml.
	Layers []config.Layer
	// Recipes are the candidates; the first one to detect the Project
	// supplies the defaults.
	Recipes []Recipe
	// Runtimes are the Runtimes resolved for Dir (see package runtimes).
	// If set, they are checked against the Project's declared constraints
	// into Resolved.RuntimeWarnings; nil skips the check.
	Runtimes []runtimes.Runtime
}

// Recipe is a stack definition: how to recognise a kind of Project and what
// it runs by default.
type Recipe interface {
	// Name identifies the Recipe, such as "laravel".
	Name() string
	// Detect reports whether the Project in dir is this Recipe's kind.
	Detect(dir string) bool
	// Defaults is the bottom config layer for the Project in dir.
	Defaults(dir string) (config.Layer, error)
}

// TestDatabaseRecipe is a Recipe that decides whether a Project it detected
// has a Test Database when database.test is auto (or unset), for instance
// from how the Project's tests are configured. Without it, auto is off.
type TestDatabaseRecipe interface {
	Recipe
	// AutoTestDatabase reports whether the Project in dir uses a Test
	// Database.
	AutoTestDatabase(dir string) bool
}

// TestDatabaseWarner is a Recipe that can tell when the tests of a Project it
// detected bypass the Project's Test Database.
type TestDatabaseWarner interface {
	Recipe
	// TestDatabaseWarnings are the reasons the tests of the Project in dir
	// may not use its Test Database, each naming the file at fault.
	TestDatabaseWarnings(dir string) []string
}

// Resolved is the Project's resolved config.
type Resolved struct {
	// Recipe is the name of the Recipe that detected the Project; "" if
	// none did.
	Recipe string
	// Name is the configured Project name; "" means use the directory name.
	Name string
	// Processes are the enabled Processes, sorted by name.
	Processes []Process
	// Disabled names the Processes config turned off, sorted.
	Disabled []string
	// StartTimeout is how long the Primary Process gets to accept
	// connections when its Worktree starts; 0 means the daemon's default.
	StartTimeout time.Duration
	// IdleTimeout is how long a Worktree may go without traffic before it
	// is Idle; 0 means the global default.
	IdleTimeout time.Duration
	// Env is the Env Patch: env key → template, merged by key. nil if no
	// layer declares any.
	Env map[string]string
	// LinkedEnv is the rest of the Env Patch of the Worktrees other than
	// the main one ([linked_env]), merged by key; nil if no layer declares
	// any. See EnvFor.
	LinkedEnv map[string]string
	// Services are the Services the Project uses, one per engine, sorted
	// by engine.
	Services []Service
	// Copy are the paths copied from the main Worktree into each other
	// Worktree when it is prepared: every layer's, in order, without
	// repeats.
	Copy []string
	// OnCreate are the setup steps (shell commands) run in a new Worktree:
	// the Recipe's first, then the Project's layers' in order, unless a
	// Project layer sets worktree.inherit_setup = false.
	OnCreate []string
	// OnRemove are the shell commands run in the main Worktree when a
	// Worktree is removed, merged like OnCreate.
	OnRemove []string
	// WorktreeRoot is where `lane worktree add` creates worktrees, as
	// configured (maybe relative to the main Worktree); "" means the
	// default, see DefaultWorktreeRoot.
	WorktreeRoot string
	// Database is the database each Worktree gets; its Engine is "" if
	// the Project has none.
	Database Database
	// TestDatabase is the Test Database decision: whether each Worktree
	// also gets an empty Test Database, <database>_test, in the database's
	// Service. True only when the database is in a Service
	// (DatabaseService) and database.test is on, or auto and the Recipe
	// decides so (TestDatabaseRecipe).
	TestDatabase bool
	// TestDatabaseWarnings are why the Project's tests may not use the
	// Test Database although there is one, such as a phpunit.xml that
	// hard-codes DB_DATABASE (TestDatabaseWarner). Each names the file it
	// comes from. Empty when TestDatabase is false.
	TestDatabaseWarnings []string
	// Warnings are problems that didn't stop resolution, such as unknown
	// keys. Each names the file it comes from.
	Warnings []string
	// RuntimeWarnings are mismatches between Input.Runtimes and the
	// Runtime constraints the Project declares, and needed Runtimes that
	// are missing.
	RuntimeWarnings []runtimes.Warning
}

// Process is one resolved Process.
type Process struct {
	Name    string
	Command string
	// Primary marks the Primary Process. At most one Process has it.
	Primary bool
	// HTTP marks a Process that serves HTTP: the Primary Process, and any
	// other Process config marks with http = true. Only HTTP Processes get
	// a port, and those other than the Primary Process are served at
	// <process>.<domain>.
	HTTP bool
	// StartMode says when the Process runs.
	StartMode StartMode
	// StaticDir is the directory, relative to the Worktree, whose existing
	// files Laneway serves itself instead of sending the request to this
	// HTTP Process; "" for none.
	StaticDir string
}

// Service is a Service the Project uses: an engine at a version, such as
// redis at "7".
type Service = service.Ref

// StartMode is when a Process runs. The zero value is Lazy.
type StartMode string

const (
	// Lazy Processes start with their Worktree on its first request and
	// stop when it is Idle. It is the default.
	Lazy StartMode = ""
	// Always Processes start with the daemon and are never stopped by
	// Idle.
	Always StartMode = "always"
	// Manual Processes only start through `lane up <process>`.
	Manual StartMode = "manual"
)

// String is the Start Mode as written in config: lazy, always or manual.
func (m StartMode) String() string {
	if m == Lazy {
		return "lazy"
	}
	return string(m)
}

// Database is the resolved [database] table, merged key by key.
type Database struct {
	// Engine is one of config.DatabaseEngines, or "" for no database.
	Engine string
	// Path is the SQLite file, relative to the Worktree.
	Path string
	// Mode is how a Worktree other than the main one gets its database,
	// one of config.DatabaseModes; DefaultDatabaseMode unless configured.
	Mode string
	// Test is database.test as configured, one of config.DatabaseTests,
	// or "" if no layer sets it (which means auto). Resolved.TestDatabase
	// is the decision.
	Test string
}

// DatabaseService is the Service the Project's database lives in: the
// Service of the database engine (postgres), which Resolve adds to Services
// if no layer declares it. False for SQLite and for no database.
func (r Resolved) DatabaseService() (Service, bool) {
	for _, s := range r.Services {
		if s.Engine == r.Database.Engine {
			return s, true
		}
	}
	return Service{}, false
}

// EnvFor is the Env Patch templates of the main Worktree (main) or of any
// other Worktree: Env, plus LinkedEnv for the others. nil if there are none.
func (r Resolved) EnvFor(main bool) map[string]string {
	if main || len(r.LinkedEnv) == 0 {
		return r.Env
	}
	env := maps.Clone(r.Env)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, r.LinkedEnv)
	return env
}

// DefaultDatabaseMode is how a Worktree gets its database unless
// database.worktree says otherwise: as a copy of the main Worktree's.
const DefaultDatabaseMode = "clone"

// Layer is r as a config layer: what `lane init` writes to laneway.toml.
// Every enabled Process is written with its command, the Primary Process is
// marked explicitly, and disabled Processes stay disabled.
func (r Resolved) Layer() config.Layer {
	var l config.Layer
	if r.Name != "" {
		l.Name = &r.Name
	}
	if r.StartTimeout != 0 {
		l.StartTimeout = &r.StartTimeout
	}
	if r.IdleTimeout != 0 {
		l.IdleTimeout = &r.IdleTimeout
	}
	if len(r.Processes)+len(r.Disabled) > 0 {
		l.Processes = map[string]config.ProcessLayer{}
	}
	for _, p := range r.Processes {
		pl := config.ProcessLayer{Command: &p.Command}
		switch {
		case p.Primary:
			pl.Primary = &p.Primary
		case p.HTTP, len(r.Processes) == 1:
			// A lone Process would otherwise become the Primary Process.
			pl.HTTP = &p.HTTP
		}
		if p.StartMode != Lazy {
			mode := p.StartMode.String()
			pl.StartMode = &mode
		}
		if p.StaticDir != "" {
			pl.StaticDir = &p.StaticDir
		}
		l.Processes[p.Name] = pl
	}
	disabled := true
	for _, name := range r.Disabled {
		l.Processes[name] = config.ProcessLayer{Disabled: &disabled}
	}
	l.Env = maps.Clone(r.Env)
	l.LinkedEnv = maps.Clone(r.LinkedEnv)
	for _, svc := range r.Services {
		if l.Services == nil {
			l.Services = map[string]string{}
		}
		l.Services[svc.Engine] = svc.Version
	}
	wt := config.WorktreeLayer{
		Copy:     slices.Clone(r.Copy),
		OnCreate: slices.Clone(r.OnCreate),
		OnRemove: slices.Clone(r.OnRemove),
	}
	if r.Recipe != "" && len(r.OnCreate)+len(r.OnRemove) > 0 {
		// The Recipe's steps are written out, so they mustn't be
		// inherited again.
		inherit := false
		wt.InheritSetup = &inherit
	}
	if r.WorktreeRoot != "" {
		wt.Root = &r.WorktreeRoot
	}
	if wt.Root != nil || wt.Copy != nil || wt.OnCreate != nil || wt.OnRemove != nil {
		l.Worktree = &wt
	}
	if db := r.Database; db != (Database{}) {
		l.Database = &config.DatabaseLayer{}
		for _, kv := range []struct {
			from string
			to   **string
		}{{db.Engine, &l.Database.Engine}, {db.Path, &l.Database.Path}, {db.Mode, &l.Database.Worktree}, {db.Test, &l.Database.Test}} {
			if kv.from != "" {
				*kv.to = &kv.from
			}
		}
	}
	return l
}

// DefaultWorktreeRoot is where `lane worktree add` creates worktrees unless
// worktree.root says otherwise: a directory next to the main Worktree named
// after it, such as ../shop.worktrees for a main Worktree at shop. Worktrees
// stay out of the repository, so nothing needs ignoring and tools scanning the
// repository don't see them, but they stay next to it.
func DefaultWorktreeRoot(mainDir string) string {
	return filepath.Join(filepath.Dir(mainDir), filepath.Base(mainDir)+".worktrees")
}

// WorktreeRootOf is the absolute directory `lane worktree add` creates the
// worktrees of the Project whose main Worktree is mainDir in.
func (r Resolved) WorktreeRootOf(mainDir string) string {
	switch {
	case r.WorktreeRoot == "":
		return DefaultWorktreeRoot(mainDir)
	case filepath.IsAbs(r.WorktreeRoot):
		return filepath.Clean(r.WorktreeRoot)
	default:
		return filepath.Join(mainDir, r.WorktreeRoot)
	}
}

// Builtin are the Recipes Laneway ships, in detection order.
var Builtin = []Recipe{Laravel{}, Rails{}}

// ResolveDir resolves the Project in dir from its laneway.toml and
// laneway.local.toml with the Builtin Recipes, checking the Runtimes found
// for dir if any are given. It is how the daemon reads config, every time it
// is about to start a Worktree's Processes.
func ResolveDir(dir string, found ...runtimes.Runtime) (Resolved, error) {
	layers, err := config.LoadLayers(dir)
	if err != nil {
		return Resolved{}, err
	}
	return Resolve(Input{Dir: dir, Layers: layers, Recipes: Builtin, Runtimes: found})
}

// Resolve merges the detected Recipe's defaults with in.Layers.
func Resolve(in Input) (Resolved, error) {
	var res Resolved
	layers := in.Layers
	var detected Recipe
	for _, r := range in.Recipes {
		if !r.Detect(in.Dir) {
			continue
		}
		defaults, err := r.Defaults(in.Dir)
		if err != nil {
			return Resolved{}, err
		}
		res.Recipe, detected = r.Name(), r
		layers = append([]config.Layer{defaults}, layers...)
		break
	}
	// Setup steps append, the Recipe's first, unless a Project layer turns
	// inheriting them off.
	inherit := true
	var recipeCreate, recipeRemove []string

	type merged struct {
		Process
		disabled bool
		http     *bool
		source   string // the layer that declared the Process
		// The layers that last set the command, http, start_mode and
		// static_dir, for errors.
		commandSource, httpSource, startModeSource, staticDirSource string
	}
	procs := map[string]*merged{}
	var engineSource, pathSource, testSource string // the layers that last set database.engine, .path and .test
	services := map[string]string{}                 // engine → version, later layers winning
	for i, l := range layers {
		res.Warnings = append(res.Warnings, l.Warnings...)
		if l.Name != nil {
			res.Name = *l.Name
		}
		if l.StartTimeout != nil {
			res.StartTimeout = *l.StartTimeout
		}
		if l.IdleTimeout != nil {
			res.IdleTimeout = *l.IdleTimeout
		}
		wt := config.WorktreeLayer{}
		if l.Worktree != nil {
			wt = *l.Worktree
		}
		if wt.Root != nil {
			res.WorktreeRoot = *wt.Root
		}
		for _, path := range wt.Copy {
			if !slices.Contains(res.Copy, path) {
				res.Copy = append(res.Copy, path)
			}
		}
		if res.Recipe != "" && i == 0 {
			recipeCreate, recipeRemove = wt.OnCreate, wt.OnRemove
		} else {
			res.OnCreate = append(res.OnCreate, wt.OnCreate...)
			res.OnRemove = append(res.OnRemove, wt.OnRemove...)
			if wt.InheritSetup != nil {
				inherit = *wt.InheritSetup
			}
		}
		if db := l.Database; db != nil {
			if db.Engine != nil {
				res.Database.Engine, engineSource = *db.Engine, l.Source
			}
			if db.Path != nil {
				res.Database.Path, pathSource = *db.Path, l.Source
			}
			if db.Worktree != nil {
				res.Database.Mode = *db.Worktree
			}
			if db.Test != nil {
				res.Database.Test, testSource = *db.Test, l.Source
			}
		}
		for key, template := range l.Env {
			if res.Env == nil {
				res.Env = map[string]string{}
			}
			res.Env[key] = template
		}
		for key, template := range l.LinkedEnv {
			if res.LinkedEnv == nil {
				res.LinkedEnv = map[string]string{}
			}
			res.LinkedEnv[key] = template
		}
		maps.Copy(services, l.Services)
		for name, pl := range l.Processes {
			p := procs[name]
			if p == nil {
				p = &merged{Process: Process{Name: name}, source: l.Source}
				procs[name] = p
			}
			if pl.Command != nil {
				p.Command, p.commandSource = *pl.Command, l.Source
			}
			if pl.Primary != nil {
				if *pl.Primary {
					// A layer choosing a Primary Process replaces the
					// earlier layers' choice.
					for _, other := range procs {
						other.Primary = false
					}
				}
				p.Primary = *pl.Primary
			}
			if pl.HTTP != nil {
				p.http, p.httpSource = pl.HTTP, l.Source
			}
			if pl.Disabled != nil {
				p.disabled = *pl.Disabled
			}
			if pl.StaticDir != nil {
				p.StaticDir, p.staticDirSource = *pl.StaticDir, l.Source
			}
			if pl.StartMode != nil {
				p.StartMode, p.startModeSource = StartMode(*pl.StartMode), l.Source
				if p.StartMode == "lazy" {
					p.StartMode = Lazy
				}
			}
		}
	}
	if res.Database.Engine != "" && res.Database.Mode == "" {
		res.Database.Mode = DefaultDatabaseMode
	}
	if res.Database.Engine == "sqlite" && res.Database.Path == "" {
		return Resolved{}, fmt.Errorf("%s: database.engine is sqlite, but database.path isn't set; "+
			"set it to the SQLite file, relative to the Worktree, such as \"database/database.sqlite\"", engineSource)
	}
	if e := res.Database.Engine; e != "" && e != "sqlite" && res.Database.Path != "" {
		return Resolved{}, fmt.Errorf("%s: database.path is only for SQLite databases, but database.engine is %s; "+
			"remove database.path", pathSource, e)
	}
	// A database in a Service needs the Service: at the version
	// [services] pins, else the engine's default.
	e, inService := service.Lookup(res.Database.Engine)
	if inService && services[e.Name] == "" {
		services[e.Name] = e.DefaultVersion
	}
	if res.Database.Test == "on" && res.Database.Engine != "" && !inService {
		return Resolved{}, fmt.Errorf("%s: database.test is on, but Test Databases are only for databases in a Service "+
			"and database.engine is %s; set database.test to off", testSource, res.Database.Engine)
	}
	switch res.Database.Test {
	case "on":
		res.TestDatabase = inService
	case "", "auto":
		r, ok := detected.(TestDatabaseRecipe)
		res.TestDatabase = inService && ok && r.AutoTestDatabase(in.Dir)
	}
	if w, ok := detected.(TestDatabaseWarner); ok && res.TestDatabase {
		res.TestDatabaseWarnings = w.TestDatabaseWarnings(in.Dir)
	}
	if inherit {
		res.OnCreate = append(slices.Clone(recipeCreate), res.OnCreate...)
		res.OnRemove = append(slices.Clone(recipeRemove), res.OnRemove...)
	}
	var enabled []*merged
	for _, name := range slices.Sorted(maps.Keys(procs)) {
		p := procs[name]
		if p.disabled {
			res.Disabled = append(res.Disabled, name)
			continue
		}
		if p.Command == "" {
			return Resolved{}, fmt.Errorf("%s: processes.%s: Process %q has no command; "+
				"set processes.%s.command, or check the Process name for a typo", p.source, name, name, name)
		}
		enabled = append(enabled, p)
	}
	// A lone Process is the Primary Process unless it says it isn't HTTP.
	if len(enabled) == 1 && (enabled[0].http == nil || *enabled[0].http) {
		enabled[0].Primary = true
	}
	var primary bool
	for _, p := range enabled {
		primary = primary || p.Primary
	}
	for _, p := range enabled {
		switch {
		case p.Primary && p.http != nil && !*p.http:
			return Resolved{}, fmt.Errorf("%s: processes.%s.http is false, but %q is the Primary Process, "+
				"which serves the Domain over HTTP", p.httpSource, p.Name, p.Name)
		case !p.Primary && p.http != nil && *p.http && !primary:
			return Resolved{}, fmt.Errorf("%s: processes.%s.http is true, but no Process is the Primary Process; "+
				"a Worktree with HTTP Processes needs exactly one: set primary = true on the Process that serves the Domain",
				p.httpSource, p.Name)
		}
		if p.Primary && p.StartMode == Manual {
			return Resolved{}, fmt.Errorf("%s: processes.%s.start_mode: the Primary Process can't be manual, "+
				"because requests to the Worktree need it; make it lazy or always", p.startModeSource, p.Name)
		}
		p.HTTP = p.Primary || (p.http != nil && *p.http)
		if !p.HTTP && strings.Contains(p.Command, "{port}") {
			return Resolved{}, fmt.Errorf("%s: processes.%s.command uses {port}, but %q isn't an HTTP Process, "+
				"so it gets no port; set processes.%s.http = true if it serves HTTP", p.commandSource, p.Name, p.Name, p.Name)
		}
		if !p.HTTP && strings.Contains(p.Command, "{vite_config}") {
			return Resolved{}, fmt.Errorf("%s: processes.%s.command uses {vite_config}, but %q isn't an HTTP Process, "+
				"so it has no host for Vite's live reload; set processes.%s.http = true if it serves HTTP",
				p.commandSource, p.Name, p.Name, p.Name)
		}
		if !p.HTTP && p.StaticDir != "" {
			return Resolved{}, fmt.Errorf("%s: processes.%s.static_dir is set, but %q isn't an HTTP Process, "+
				"so no requests reach it; set processes.%s.http = true if it serves HTTP", p.staticDirSource, p.Name, p.Name, p.Name)
		}
		res.Processes = append(res.Processes, p.Process)
	}
	for _, engine := range slices.Sorted(maps.Keys(services)) {
		res.Services = append(res.Services, Service{Engine: engine, Version: services[engine]})
	}
	if in.Runtimes != nil {
		res.RuntimeWarnings = runtimes.Check(in.Dir, in.Runtimes)
	}
	return res, nil
}
