// Package recipe is the Recipe Resolver: a pure function from a Project
// directory and its config layers to everything Laneway runs for the Project.
// The daemon reads config only through it.
package recipe

import (
	"cmp"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/detect"
	"github.com/lanewayhq/laneway/internal/engine"
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
	// Integrations are the candidates; every one that detects the Project
	// adds its layer over the Recipe's.
	Integrations []Integration
	// Engines are the Engines Laneway can run, which the layers' Services,
	// database engine and Service variables must be among (see
	// config.Layer.CheckEngines).
	Engines engine.Set
	// CatalogEngines are the Engines Laneway hasn't installed but can
	// fetch from the Catalog: config naming one says so, rather than that
	// the Engine is unknown.
	CatalogEngines []string
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
	// Detection is the rule a Project of this Recipe's kind matches.
	Detection() detect.Rule
	// Defaults is the Recipe's answer for the Project in dir: the bottom
	// config layer, and its Test Database decision.
	Defaults(dir string) (Result, error)
}

// Result is a Recipe's answer for a Project it detected.
type Result struct {
	// Layer is the bottom config layer, under the Integrations' and the
	// Project's own.
	Layer config.Layer
	// TestDatabase decides database.test = auto (or unset): whether the
	// Project's tests use a Test Database, for instance from how they are
	// configured.
	TestDatabase bool
	// TestDatabaseWarnings are the reasons the Project's tests may not use
	// its Test Database, each naming the file at fault, such as a
	// phpunit.xml that hard-codes DB_DATABASE.
	TestDatabaseWarnings []string
	// TestEnv is the env file the Project's tests read; zero for
	// DefaultTestEnv.
	TestEnv TestEnv
	// ExportEnv says the stack reads its settings from the environment,
	// not .env, so the commands Laneway runs in a Worktree get its .env
	// exported (see Resolved.ExportEnv).
	ExportEnv bool
}

// TestEnv is the env file a Project's tests read their settings from, which
// Laneway patches to point them at the Worktree's Test Database.
type TestEnv struct {
	// File is the env file's name in the Worktree, such as ".env.testing".
	File string
	// Layered means the tests read File over .env, so a File holding only
	// the Env Patch is complete and Laneway may create it. Otherwise File
	// replaces .env for the tests, and Laneway only patches one the
	// Worktree has, or a copy of the main Worktree's.
	Layered bool
}

// DefaultTestEnv is the test env file of a Project whose Recipe names none:
// Laravel's .env.testing, which replaces .env when the tests run.
var DefaultTestEnv = TestEnv{File: ".env.testing"}

// Resolved is the Project's resolved config.
type Resolved struct {
	// Recipe is the name of the Recipe that detected the Project; "" if
	// none did.
	Recipe string
	// RecipeRule is the part of the Recipe's detection rule the Project
	// matched, described for people, such as "artisan exists"; "" with
	// Recipe.
	RecipeRule string
	// Integrations are the names of the Integrations that detected the
	// Project, in the order their layers apply (by name); nil if none did.
	Integrations []string
	// IntegrationRules are, by Integration name, the part of each detected
	// Integration's detection rule the Project matched, described for
	// people, such as "package.json depends on vite"; nil with
	// Integrations.
	IntegrationRules map[string]string
	// Plugins are the Plugins the Project's config files (and the global
	// config) turn on by name, sorted, so their hooks fire in it. The
	// Resolver doesn't know which Plugins are installed or machine-wide:
	// plugin.Host.ResolveDir checks the names and sets this.
	Plugins []string
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
	// the Recipe's and the Integrations' first, then the Project's layers'
	// in order, unless a Project layer sets worktree.inherit_setup = false.
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
	// decides so (Result.TestDatabase).
	TestDatabase bool
	// TestEnv is the env file the Project's tests read, which Laneway
	// points at the Test Database (Result.TestEnv, else DefaultTestEnv).
	// Zero when TestDatabase is false.
	TestEnv TestEnv
	// TestDatabaseWarnings are why the Project's tests may not use the
	// Test Database although there is one, such as a phpunit.xml that
	// hard-codes DB_DATABASE (Result.TestDatabaseWarnings). Each names the file it
	// comes from. Empty when TestDatabase is false.
	TestDatabaseWarnings []string
	// ExportEnv is true when the Recipe says its stack reads settings from
	// the environment rather than .env (Result.ExportEnv): setup steps,
	// exec effects and `lane exec` then run with the Worktree's .env
	// exported, as its Processes load it.
	ExportEnv bool
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
	// Engine is sqlite or an Engine with database commands
	// (database.EngineNames), or "" for no database.
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
	if (r.Recipe != "" || len(r.Integrations) > 0) && len(r.OnCreate)+len(r.OnRemove) > 0 {
		// The Recipe's and Integrations' steps are written out, so they
		// mustn't be inherited again.
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

// Resolve merges the detected Recipe's defaults, then the detected
// Integrations', then in.Layers.
func Resolve(in Input) (Resolved, error) {
	for _, l := range in.Layers {
		if err := l.CheckEngines(in.Engines, in.CatalogEngines); err != nil {
			return Resolved{}, err
		}
	}
	var res Resolved
	var layers []config.Layer // the Recipe's, then the Integrations', then in.Layers
	var answer Result         // the detected Recipe's
	for _, r := range in.Recipes {
		matched, ok := r.Detection().Match(in.Dir)
		if !ok {
			continue
		}
		var err error
		if answer, err = r.Defaults(in.Dir); err != nil {
			return Resolved{}, err
		}
		res.Recipe, res.RecipeRule = r.Name(), matched
		layers = append(layers, answer.Layer)
		break
	}
	recipeLayers := len(layers)
	integrations, names, rules, err := detectIntegrations(in.Dir, in.Integrations)
	if err != nil {
		return Resolved{}, err
	}
	res.Integrations, res.IntegrationRules = names, rules
	if err := checkIntegrations(integrations, in.Layers); err != nil {
		return Resolved{}, err
	}
	layers = append(layers, integrations...)
	// The layers before project are the Recipe's and the Integrations'.
	project := len(layers)
	for _, l := range layers {
		if err := l.CheckEngines(in.Engines, in.CatalogEngines); err != nil {
			return Resolved{}, err
		}
	}
	layers = append(layers, in.Layers...)
	// Setup steps append, the Recipe's and Integrations' first, unless a
	// Project layer turns inheriting them off.
	inherit := true
	var recipeCreate, recipeRemove []string

	type merged struct {
		Process
		disabled bool
		http     *bool
		source   string // the layer that declared the Process
		// integration marks a Process an Integration declared.
		integration bool
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
		if i < project {
			recipeCreate = append(recipeCreate, wt.OnCreate...)
			recipeRemove = append(recipeRemove, wt.OnRemove...)
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
				p = &merged{Process: Process{Name: name}, source: l.Source,
					integration: recipeLayers <= i && i < project}
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
	e, inService := in.Engines.Lookup(res.Database.Engine)
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
		res.TestDatabase = inService && answer.TestDatabase
	}
	res.ExportEnv = answer.ExportEnv
	if res.TestDatabase {
		res.TestEnv = cmp.Or(answer.TestEnv, DefaultTestEnv)
		res.TestDatabaseWarnings = answer.TestDatabaseWarnings
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
	// Integrations' Processes don't count unless there are only those: an
	// Integration never takes the Domain from the Project's own Process.
	own := slices.DeleteFunc(slices.Clone(enabled), func(p *merged) bool { return p.integration })
	if len(own) == 0 {
		own = enabled
	}
	if len(own) == 1 && (own[0].http == nil || *own[0].http) {
		own[0].Primary = true
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
