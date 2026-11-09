package plugin

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"

	"github.com/lanewayhq/laneway/internal/engine"
)

// Engines from Plugins (spec #48): a Plugin's manifest lists Engine
// manifests, which need no module. Even Redis, Mailpit, Postgres and MySQL
// come from Plugins, fetched from the Catalog when a Project's config names
// them.

// validateEngineFiles checks the manifest's engines are paths inside the
// Plugin's directory, each listed once.
func (m Manifest) validateEngineFiles() error {
	for i, f := range m.EngineFiles {
		if path.IsAbs(f) || !fs.ValidPath(f) || f == "." {
			return fmt.Errorf("engines[%d]: %q must be an Engine manifest inside the Plugin's directory, such as \"engine.toml\"", i, f)
		}
		if slices.Index(m.EngineFiles, f) < i {
			return fmt.Errorf("engines[%d]: the Plugin lists %s twice", i, f)
		}
	}
	return nil
}

// readEngines reads the Engine manifests m lists from fsys, the Plugin's
// directory, which errors call dir.
func readEngines(fsys fs.FS, dir string, m Manifest) ([]*engine.Manifest, error) {
	var engines []*engine.Manifest
	for _, f := range m.EngineFiles {
		file := filepath.Join(dir, filepath.FromSlash(f))
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, fmt.Errorf("%s: the Engine manifest %s isn't there; write it, or remove it from engines", filepath.Join(dir, ManifestFile), file)
		}
		e, err := engine.Parse(file, data)
		if err != nil {
			return nil, err
		}
		for _, other := range engines {
			if other.Name == e.Name {
				return nil, fmt.Errorf("Plugin %s adds the Engine %s twice", m.Name, e.Name)
			}
			for _, d := range e.Domains {
				if _, ok := other.Domain(d.Label); ok {
					return nil, fmt.Errorf("Plugin %s's Engines %s and %s both declare the Service Domain %s", m.Name, other.Name, e.Name, d.Label)
				}
			}
		}
		engines = append(engines, e)
	}
	return engines, nil
}

// Engines are every Engine Laneway can run: the installed Plugins', by
// Plugin name.
func (h *Host) Engines() engine.Set {
	var set engine.Set
	for _, p := range h.List() {
		set = append(set, p.Engines...)
	}
	return set
}

// ServiceDomain finds the installed Engine that declares the Service Domain
// whose label is label, such as Mailpit's for "mail".
func (h *Host) ServiceDomain(label string) (*engine.Manifest, engine.ServiceDomain, bool) {
	for _, p := range h.List() {
		for _, e := range p.Engines {
			if d, ok := e.Domain(label); ok {
				return e, d, true
			}
		}
	}
	return nil, engine.ServiceDomain{}, false
}

// checkEngineNames reports an Engine p adds that takes the name of another
// installed Plugin's, or a Service Domain label another installed Plugin's
// Engine declares.
func (h *Host) checkEngineNames(p Plugin) error {
	taken := map[string]string{}  // Engine name → who has it
	labels := map[string]string{} // Service Domain label → whose Engine declares it
	for _, other := range h.List() {
		if other.Name == p.Name {
			continue // adding p replaces it
		}
		for _, e := range other.Engines {
			taken[e.Name] = "Plugin " + other.Name
			for _, d := range e.Domains {
				labels[d.Label] = fmt.Sprintf("Plugin %s's Engine %s", other.Name, e.Name)
			}
		}
	}
	for _, e := range p.Engines {
		if who, ok := taken[e.Name]; ok {
			return fmt.Errorf("Plugin %s adds the Engine %s, but %s already has an Engine of that name", p.Name, e.Name, who)
		}
		for _, d := range e.Domains {
			if who, ok := labels[d.Label]; ok {
				return fmt.Errorf("Plugin %s's Engine %s declares the Service Domain %s, but %s already does", p.Name, e.Name, d.Label, who)
			}
		}
	}
	return nil
}
