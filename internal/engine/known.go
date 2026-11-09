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
	// Buckets are the names of the Buckets config declares ([buckets]),
	// which an Engine with Bucket commands gives a variable each:
	// {<engine>_bucket_<name>} (Manifest.DeclaredBucketVariable).
	Buckets []string
}

// Variables are the Service variables of the Installed Engines, with those
// of the Buckets config declares.
func (k Known) Variables() []string {
	var vars []string
	for _, m := range k.Installed {
		vars = append(vars, m.ProjectVariables(k.Buckets)...)
	}
	return vars
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

// TemplateError is why a template using Service variables no installed
// Engine gives (uses reports whether it uses one) may be fine once an
// Engine Laneway can fetch is installed: that Engine isn't. It is nil if
// the template uses no variable of one. Every Engine gives
// {<engine>_host}, {<engine>_port} and {<engine>_password}, but only one
// with users gives {<engine>_username}, only one with Service Domains
// {<engine>_endpoint}, and only one with Bucket commands {<engine>_bucket}
// and {<engine>_bucket_<name>} for each of Buckets, which Laneway can't
// tell before fetching it.
func (k Known) TemplateError(uses func(variable string) bool) error {
	for _, name := range k.Fetchable {
		if !k.CanFetch(name) {
			continue
		}
		if slices.ContainsFunc((&Manifest{Name: name}).Variables(), uses) {
			return NotInstalled(name)
		}
		maybe := []string{name + "_username", name + "_endpoint", name + "_bucket"}
		maybe = append(maybe, Assumed(name).DeclaredBucketVariables(k.Buckets)...)
		for _, v := range maybe {
			if uses(v) {
				return fmt.Errorf("the %s Engine isn't installed, so Laneway can't tell whether it has {%s}; "+
					"run `lane doctor` to fetch it from the Catalog", name, v)
			}
		}
	}
	return nil
}

// NotInstalled is the error for config naming the Engine name, which
// Laneway hasn't installed but can fetch from the Catalog.
func NotInstalled(name string) error {
	return fmt.Errorf("the %s Engine isn't installed; run `lane doctor` to fetch it from the Catalog", name)
}
