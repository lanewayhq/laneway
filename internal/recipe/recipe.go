// Package recipe is the Recipe Resolver: a pure function from a Project
// directory and its config layers to everything Portby runs for the Project.
// The daemon reads config only through it.
package recipe

import (
	"fmt"
	"maps"
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
	return l
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
	for _, l := range layers {
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
		for key, template := range l.Env {
			if res.Env == nil {
				res.Env = map[string]string{}
			}
			res.Env[key] = template
		}
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
	if in.Runtimes != nil {
		res.RuntimeWarnings = runtimes.Check(in.Dir, in.Runtimes)
	}
	return res, nil
}
