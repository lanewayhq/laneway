package daemon

import (
	"fmt"
	"strings"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/config"
)

// A Project's config may name Plugins it uses (plugins = [...]). One in the
// Catalog is fetched like any other Catalog Plugin the Project needs; any
// other is only ever added by hand (spec #48, ADR-0007), so that committed
// config can never make Laneway run code from just any repository.

// namedPlugin is a Plugin a Project's config names that isn't installed.
type namedPlugin struct {
	name string
	file string // the config file naming it
}

// missingNamedPlugins are the Plugins the config of the Project in dir names
// that aren't installed or built into Laneway, in the order named. Config
// that can't be read names none here: resolving it says why.
func (d *Daemon) missingNamedPlugins(dir string) []namedPlugin {
	layers, err := config.LoadLayers(dir)
	if err != nil {
		return nil
	}
	var missing []namedPlugin
	seen := map[string]bool{}
	for _, l := range layers {
		for _, name := range l.Plugins {
			if seen[name] || d.plugins.Has(name) {
				continue
			}
			seen[name] = true
			missing = append(missing, namedPlugin{name: name, file: l.Source})
		}
	}
	return missing
}

// addByHand is how to add a Plugin outside the Catalog.
const addByHand = "add it by hand with `lane plugin add github.com/<owner>/<repo>` (or `lane plugin add <dir>`) and run `lane link` again, or remove it from plugins"

// notInCatalogError says that Plugins the Project's config names aren't
// installed and aren't in the Catalog, so Laneway won't fetch them, and how
// to add them by hand.
func notInCatalogError(plugins []namedPlugin) error {
	var b strings.Builder
	for _, p := range plugins {
		fmt.Fprintf(&b, "%s: plugins names %s, but no Plugin %s is installed, and it isn't in the Catalog\n", p.file, p.name, p.name)
	}
	b.WriteString("Laneway fetches only Catalog Plugins by itself, so that config, such as a committed laneway.toml, can never make it run code from just any repository; ")
	b.WriteString("if you trust the Plugin, " + addByHand)
	return &api.Error{Code: api.CodeInvalidConfig, Message: b.String()}
}

// uncheckedNamedError says that Plugins the Project's config names aren't
// installed, and the Catalog couldn't be checked for them (why says why).
func uncheckedNamedError(plugins []namedPlugin, why error) error {
	var b strings.Builder
	for _, p := range plugins {
		fmt.Fprintf(&b, "%s: plugins names %s, but no Plugin %s is installed\n", p.file, p.name, p.name)
	}
	fmt.Fprintf(&b, "Laneway couldn't check whether the Catalog has it: %v\n", why)
	b.WriteString("once Laneway can reach the Catalog, run `lane link` again; if the Plugin isn't in the Catalog, " + addByHand)
	return &api.Error{Code: api.CodePluginsUnavailable, Message: b.String()}
}
