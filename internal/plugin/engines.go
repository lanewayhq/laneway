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

// EnginesLinkedAt are the Engines with a web UI (engine.Manifest.WebPort)
// added by Plugins that link their Services to url, a Dashboard link's URL
// as the manifest writes it, such as "https://mail.{dashboard_domain}/":
// those whose web UI Laneway serves there.
func (h *Host) EnginesLinkedAt(url string) []*engine.Manifest {
	var engines []*engine.Manifest
	for _, p := range h.List() {
		if !slices.ContainsFunc(p.Dashboard.Links, func(l LinkDecl) bool { return l.Scope == ScopeService && l.URL == url }) {
			continue
		}
		for _, e := range p.Engines {
			if e.WebPort > 0 {
				engines = append(engines, e)
			}
		}
	}
	return engines
}

// checkEngineNames reports an Engine p adds that takes the name of another
// installed Plugin's.
func (h *Host) checkEngineNames(p Plugin) error {
	taken := map[string]string{} // Engine name → who has it
	for _, other := range h.List() {
		if other.Name == p.Name {
			continue // adding p replaces it
		}
		for _, e := range other.Engines {
			taken[e.Name] = "Plugin " + other.Name
		}
	}
	for _, e := range p.Engines {
		if who, ok := taken[e.Name]; ok {
			return fmt.Errorf("Plugin %s adds the Engine %s, but %s already has an Engine of that name", p.Name, e.Name, who)
		}
	}
	return nil
}
