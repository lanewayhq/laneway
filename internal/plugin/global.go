package plugin

import (
	"fmt"
	"maps"
	"slices"

	"github.com/lanewayhq/laneway/internal/config"
)

// Machine-wide Plugins (ADR-0007): added with `lane plugin add --global`,
// such as one that notifies the developer's phone when a Worktree is Ready.
// Its hooks fire in the Projects that turn it on in the developer's own
// config: the global config's plugins (every Project) or a Project's
// laneway.local.toml. A committed laneway.toml can never turn one on, so a
// teammate's config can't run code across all of a developer's Projects.

// AddGlobal is AddFrom for a machine-wide Plugin.
func (h *Host) AddGlobal(src, source string) (added Plugin, replaced *Plugin, err error) {
	return h.add(src, source, true, "")
}

// turnedOn are the Plugins the global config and the Project's config files
// (layers, in order) turn on by name, sorted. Each name must be an
// installed Plugin, and a machine-wide one only in personal config; errors
// name the file and say what to change.
func (h *Host) turnedOn(layers []config.Layer) ([]string, error) {
	global, err := config.LoadGlobal(h.opts.GlobalConfig)
	if err != nil {
		return nil, err
	}
	on := map[string]bool{}
	for _, name := range global.Plugins {
		p, ok := h.lookup(name)
		switch {
		case !ok:
			return nil, fmt.Errorf("%s: plugins names %s, but no Plugin %s is installed; add it with `lane plugin add --global <dir|github.com/<owner>/<repo>>`, or remove it from plugins",
				h.opts.GlobalConfig, name, name)
		case !p.Global:
			return nil, fmt.Errorf("%s: plugins names %s, but Plugin %s wasn't added with --global, so it can't be turned on in every Project; "+
				"add it again with `lane plugin add --global %s`, or name it in one Project's %s instead", h.opts.GlobalConfig, name, name, p.Source, config.LocalFileName)
		}
		on[name] = true
	}
	for _, l := range layers {
		for _, name := range l.Plugins {
			p, ok := h.lookup(name)
			switch {
			case !ok:
				return nil, fmt.Errorf("%s: plugins names %s, but no Plugin %s is installed; add it with `lane plugin add <dir|github.com/<owner>/<repo>>`, or remove it from plugins",
					l.Source, name, name)
			case p.Global && !l.Personal():
				return nil, fmt.Errorf("%s: plugins names %s, a machine-wide Plugin, which only your own config can turn on, never a committed %s; "+
					"remove it, and turn it on in %s for this Project, or under plugins in %s for every Project",
					l.Source, name, config.FileName, config.LocalFileName, h.opts.GlobalConfig)
			}
			on[name] = true
		}
	}
	return slices.Sorted(maps.Keys(on)), nil
}
