package recipe

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/lanewayhq/laneway/internal/config"
)

// Integration is a tool a Project uses alongside its stack, such as Vite:
// how to recognise that the Project uses it, on its own whatever the
// Project's Recipe, and what it adds to the Project's config.
type Integration interface {
	// Name identifies the Integration, such as "vite".
	Name() string
	// Detect reports whether the Project in dir uses the Integration.
	Detect(dir string) bool
	// Defaults is the config layer the Integration adds for the Project in
	// dir, applied over the Recipe's. It may not choose the Primary
	// Process.
	Defaults(dir string) (config.Layer, error)
}

// BuiltinIntegrations are the Integrations Laneway ships.
var BuiltinIntegrations = []Integration{Vite{}}

// detectIntegrations are the layers of the Integrations that detect the
// Project in dir, sorted by name so that they apply in a stable order, with
// their names.
func detectIntegrations(dir string, integrations []Integration) ([]config.Layer, []string, error) {
	detected := slices.SortedFunc(slices.Values(integrations), func(a, b Integration) int {
		return cmp.Compare(a.Name(), b.Name())
	})
	var layers []config.Layer
	var names []string
	for _, in := range detected {
		if !in.Detect(dir) {
			continue
		}
		l, err := in.Defaults(dir)
		if err != nil {
			return nil, nil, err
		}
		layers = append(layers, l)
		names = append(names, in.Name())
	}
	return layers, names, nil
}

// checkIntegrations fails when an Integration layer sets
// processes.<name>.primary, which only the Recipe and the Project's config
// may, so that no Integration takes the Domain; or when two Integration
// layers set the same key to
// different values and no Project layer sets it: no Integration silently
// wins over another, and the Project's config settles it. (Integrations
// agreeing on a value, such as two wanting redis 7, don't conflict.)
func checkIntegrations(integrations, project []config.Layer) error {
	for _, l := range integrations {
		for _, name := range slices.Sorted(maps.Keys(l.Processes)) {
			if l.Processes[name].Primary != nil {
				return fmt.Errorf("%s: processes.%s.primary: only a Recipe or %s chooses the Primary Process, "+
					"not an Integration; remove primary from the Integration's processes.%s", l.Source, name, config.FileName, name)
			}
		}
	}
	settled := map[string]bool{}
	for _, l := range project {
		for key := range layerKeys(l) {
			settled[key] = true
		}
	}
	type setting struct{ value, source string }
	set := map[string]setting{}
	for _, l := range integrations {
		keys := layerKeys(l)
		for _, key := range slices.Sorted(maps.Keys(keys)) {
			first, ok := set[key]
			switch {
			case !ok:
				set[key] = setting{keys[key], l.Source}
			case first.value != keys[key] && !settled[key]:
				return fmt.Errorf("%s and %s both set %s, to %s and %s; "+
					"set %s in %s to settle it", first.source, l.Source, key, first.value, keys[key], key, config.FileName)
			}
		}
	}
	return nil
}

// layerKeys are the keys l sets, as written in config such as
// "processes.web.command", with their values as TOML would write them.
// Lists (worktree.copy, on_create, on_remove) aren't keys: every layer's
// are kept.
func layerKeys(l config.Layer) map[string]string {
	keys := map[string]string{}
	str := func(key string, v *string) {
		if v != nil {
			keys[key] = strconv.Quote(*v)
		}
	}
	boolean := func(key string, v *bool) {
		if v != nil {
			keys[key] = strconv.FormatBool(*v)
		}
	}
	str("name", l.Name)
	if l.StartTimeout != nil {
		keys["start_timeout"] = strconv.Quote(l.StartTimeout.String())
	}
	if l.IdleTimeout != nil {
		keys["idle_timeout"] = strconv.Quote(l.IdleTimeout.String())
	}
	for name, p := range l.Processes {
		key := "processes." + name + "."
		str(key+"command", p.Command)
		boolean(key+"primary", p.Primary)
		boolean(key+"disabled", p.Disabled)
		str(key+"start_mode", p.StartMode)
		boolean(key+"http", p.HTTP)
		str(key+"static_dir", p.StaticDir)
	}
	for k, v := range l.Env {
		keys["env."+k] = strconv.Quote(v)
	}
	for k, v := range l.LinkedEnv {
		keys["linked_env."+k] = strconv.Quote(v)
	}
	for k, v := range l.Services {
		keys["services."+k] = strconv.Quote(v)
	}
	if wt := l.Worktree; wt != nil {
		str("worktree.root", wt.Root)
		boolean("worktree.inherit_setup", wt.InheritSetup)
	}
	if db := l.Database; db != nil {
		str("database.engine", db.Engine)
		str("database.path", db.Path)
		str("database.worktree", db.Worktree)
		str("database.test", db.Test)
	}
	return keys
}
