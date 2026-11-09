package engine

import (
	"fmt"
	"slices"
	"strings"
)

// Known are the Engines config may name: those installed, and those
// Laneway hasn't installed but can fetch from the Catalog, such as after an
// upgrade left the installed Plugins behind. Config naming one Laneway can
// fetch says to fetch it, rather than that the Engine is unknown.
type Known struct {
	// Installed are the Engines Laneway can run (see plugin.Host.Engines).
	Installed Set
	// Fetchable are the names of the Engines that Catalog Plugins not
	// installed add (see plugin.Host.FetchableEngines).
	Fetchable []string
}

// CanFetch reports whether the Engine name isn't installed but can be
// fetched from the Catalog.
func (k Known) CanFetch(name string) bool {
	_, installed := k.Installed.Lookup(name)
	return !installed && slices.Contains(k.Fetchable, name)
}

// Lookup returns the installed Engine called name, for a Service of it
// that config names, or why config can't name it: that it isn't
// installed, if Laneway can fetch it, or else that Laneway has no such
// Service.
func (k Known) Lookup(name string) (*Manifest, error) {
	if m, ok := k.Installed.Lookup(name); ok {
		return m, nil
	}
	if k.CanFetch(name) {
		return nil, NotInstalled(name)
	}
	return nil, fmt.Errorf("Laneway has no Service %q; the Services are %s", name, strings.Join(k.Installed.Names(), ", "))
}

// NotInstalled is the error for config naming the Engine name, which
// Laneway hasn't installed but can fetch from the Catalog.
func NotInstalled(name string) error {
	return fmt.Errorf("the %s Engine isn't installed; run `lane doctor` to fetch it from the Catalog", name)
}
