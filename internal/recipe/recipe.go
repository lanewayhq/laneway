// Package recipe is the Recipe Resolver: a pure function from a Project
// directory and its config layers to everything Portby runs for the Project.
// The daemon reads config only through it.
package recipe

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/config"
	"github.com/mrsafalpiya/portby.dev/internal/runtimes"
)

// Input is what Resolve works from.
type Input struct {
	// Dir is the Project directory Recipes detect from.
	Dir string
	// Layers are applied over the Recipe's defaults in order, later
	// winning: normally portby.toml, then portby.local.toml.
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
	// Services are the Services the Project uses, one per engine, sorted
	// by engine.
	Services []Service
	// OnCreate are the setup steps (shell commands) run in a new Worktree:
	// the Recipe's first, then the Project's layers' in order, unless a
	// Project layer sets inherit_setup = false.
	OnCreate []string
	// OnRemove are the shell commands run in the main Worktree when a
	// Worktree is removed, merged like OnCreate.
	OnRemove []string
	// WorktreeRoot is where `portby worktree add` creates worktrees, as
	// configured (maybe relative to the main Worktree); "" means the
	// default, see DefaultWorktreeRoot.
	WorktreeRoot string
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
}

// Service is a Service the Project uses: an engine at a version, such as
// redis at "7".
type Service struct {
	Engine  string
	Version string
}

// StartMode is when a Process runs. The zero value is Lazy.
type StartMode string

const (
	// Lazy Processes start with their Worktree on its first request and
	// stop when it is Idle. It is the default.
	Lazy StartMode = ""
	// Always Processes start with the daemon and are never stopped by
	// Idle.
	Always StartMode = "always"
	// Manual Processes only start through `portby up <process>`.
	Manual StartMode = "manual"
)

// String is the Start Mode as written in config: lazy, always or manual.
func (m StartMode) String() string {
	if m == Lazy {
		return "lazy"
	}
	return string(m)
}

// Layer is r as a config layer: what `portby init` writes to portby.toml.
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
		l.Processes[p.Name] = pl
	}
	disabled := true
	for _, name := range r.Disabled {
		l.Processes[name] = config.ProcessLayer{Disabled: &disabled}
	}
	l.Env = maps.Clone(r.Env)
	for _, svc := range r.Services {
		if l.Services == nil {
			l.Services = map[string]string{}
		}
		l.Services[svc.Engine] = svc.Version
	}
	l.OnCreate = slices.Clone(r.OnCreate)
	l.OnRemove = slices.Clone(r.OnRemove)
	if r.Recipe != "" && len(r.OnCreate)+len(r.OnRemove) > 0 {
		// The Recipe's steps are written out, so they mustn't be
		// inherited again.
		inherit := false
		l.InheritSetup = &inherit
	}
	if r.WorktreeRoot != "" {
		l.WorktreeRoot = &r.WorktreeRoot
	}
	return l
}

// DefaultWorktreeRoot is where `portby worktree add` creates worktrees unless
// worktree_root says otherwise: a directory next to the main Worktree named
// after it, such as ../shop.worktrees for a main Worktree at shop. Worktrees
// stay out of the repository, so nothing needs ignoring and tools scanning the
// repository don't see them, but they stay next to it.
func DefaultWorktreeRoot(mainDir string) string {
	return filepath.Join(filepath.Dir(mainDir), filepath.Base(mainDir)+".worktrees")
}

// WorktreeRootOf is the absolute directory `portby worktree add` creates the
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

// Builtin are the Recipes Portby ships, in detection order. There are none
// yet; the Laravel Recipe will be the first.
var Builtin []Recipe

// ResolveDir resolves the Project in dir from its portby.toml and
// portby.local.toml with the Builtin Recipes, checking the Runtimes found
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
	for _, r := range in.Recipes {
		if !r.Detect(in.Dir) {
			continue
		}
		defaults, err := r.Defaults(in.Dir)
		if err != nil {
			return Resolved{}, err
		}
		res.Recipe = r.Name()
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
		// The layers that last set the command, http and start_mode, for
		// errors.
		commandSource, httpSource, startModeSource string
	}
	procs := map[string]*merged{}
	services := map[string]string{} // engine → version, later layers winning
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
		if l.WorktreeRoot != nil {
			res.WorktreeRoot = *l.WorktreeRoot
		}
		if res.Recipe != "" && i == 0 {
			recipeCreate, recipeRemove = l.OnCreate, l.OnRemove
		} else {
			res.OnCreate = append(res.OnCreate, l.OnCreate...)
			res.OnRemove = append(res.OnRemove, l.OnRemove...)
			if l.InheritSetup != nil {
				inherit = *l.InheritSetup
			}
		}
		for key, template := range l.Env {
			if res.Env == nil {
				res.Env = map[string]string{}
			}
			res.Env[key] = template
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
			if pl.StartMode != nil {
				p.StartMode, p.startModeSource = StartMode(*pl.StartMode), l.Source
				if p.StartMode == "lazy" {
					p.StartMode = Lazy
				}
			}
		}
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
