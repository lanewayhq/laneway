package config

import (
	"fmt"
	"maps"
	"slices"

	"github.com/lanewayhq/laneway/internal/runtimes"
)

// Plugin version pins (spec #48): laneway.toml's [plugin_versions] table pins
// Catalog Plugins to version ranges, such as `mysql = "^1.2"`, so a team
// stays on versions it has checked. Ranges are written as npm's are
// (node-semver): `^1.2`, `~1.2.3`, `1.2` (any 1.2.x), `>=1.2 <2`, `1.2 || 2`.
// Only laneway.toml and laneway.local.toml pin versions; a Recipe's or
// Integration's layer can't.

// PinDialect is the syntax of version ranges in [plugin_versions].
const PinDialect = runtimes.NPM

func checkPluginVersions(pins map[string]string) error {
	for _, name := range slices.Sorted(maps.Keys(pins)) {
		if err := runtimes.CheckConstraint(pins[name], PinDialect); err != nil {
			return fmt.Errorf("plugin_versions.%s: %q isn't a version range, such as \"^1.2\", \"~1.2.3\" or \">=1.2 <2\" (%v)",
				key(name), pins[name], err)
		}
	}
	return nil
}

// Pin is a version range a config file pins a Plugin to.
type Pin struct {
	// Range is the version range, as written.
	Range string
	// Source is the file that pins it.
	Source string
}

// Allows reports whether the pin allows version.
func (p Pin) Allows(version string) bool {
	ok, err := runtimes.Satisfies(version, p.Range, PinDialect)
	return err == nil && ok
}

// PluginPins are the version pins in dir's laneway.toml and
// laneway.local.toml, by Plugin name. A Plugin laneway.local.toml pins is
// pinned only by it, overriding laneway.toml's pin.
func PluginPins(dir string) (map[string]Pin, error) {
	layers, err := LoadLayers(dir)
	if err != nil {
		return nil, err
	}
	pins := map[string]Pin{}
	for _, l := range layers {
		for name, r := range l.PluginVersions {
			pins[name] = Pin{Range: r, Source: l.Source}
		}
	}
	return pins, nil
}
