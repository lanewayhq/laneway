// Package recipe is the Recipe Resolver: a pure function from a Project
// directory and its config layers to everything Portby runs for the Project.
// The daemon reads config only through it.
package recipe

import (
	"fmt"
	"maps"
	"slices"

	"github.com/mrsafalpiya/portby.dev/internal/config"
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
	// Warnings are problems that didn't stop resolution, such as unknown
	// keys. Each names the file it comes from.
	Warnings []string
}

// Process is one resolved Process.
type Process struct {
	Name    string
	Command string
	// Primary marks the Primary Process. At most one Process has it.
	Primary bool
}

// Layer is r as a config layer: what `portby init` writes to portby.toml.
// Every enabled Process is written with its command, the Primary Process is
// marked explicitly, and disabled Processes stay disabled.
func (r Resolved) Layer() config.Layer {
	var l config.Layer
	if r.Name != "" {
		l.Name = &r.Name
	}
	if len(r.Processes)+len(r.Disabled) > 0 {
		l.Processes = map[string]config.ProcessLayer{}
	}
	for _, p := range r.Processes {
		pl := config.ProcessLayer{Command: &p.Command}
		if p.Primary {
			pl.Primary = &p.Primary
		}
		l.Processes[p.Name] = pl
	}
	disabled := true
	for _, name := range r.Disabled {
		l.Processes[name] = config.ProcessLayer{Disabled: &disabled}
	}
	return l
}

// Builtin are the Recipes Portby ships, in detection order. There are none
// yet; the Laravel Recipe will be the first.
var Builtin []Recipe

// ResolveDir resolves the Project in dir from its portby.toml and
// portby.local.toml with the Builtin Recipes. It is how the daemon reads
// config, every time it is about to start a Worktree's Processes.
func ResolveDir(dir string) (Resolved, error) {
	layers, err := config.LoadLayers(dir)
	if err != nil {
		return Resolved{}, err
	}
	return Resolve(Input{Dir: dir, Layers: layers, Recipes: Builtin})
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
		source   string // the layer that declared the Process
	}
	procs := map[string]*merged{}
	for _, l := range layers {
		res.Warnings = append(res.Warnings, l.Warnings...)
		if l.Name != nil {
			res.Name = *l.Name
		}
		for name, pl := range l.Processes {
			p := procs[name]
			if p == nil {
				p = &merged{Process: Process{Name: name}, source: l.Source}
				procs[name] = p
			}
			if pl.Command != nil {
				p.Command = *pl.Command
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
			if pl.Disabled != nil {
				p.disabled = *pl.Disabled
			}
		}
	}
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
		res.Processes = append(res.Processes, p.Process)
	}
	if len(res.Processes) == 1 {
		res.Processes[0].Primary = true
	}
	return res, nil
}
