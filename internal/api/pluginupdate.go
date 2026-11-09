package api

// PluginUpdate is a newer release of an installed Catalog Plugin: the
// latest the Catalog lists that every version pin in a Linked Project's
// laneway.toml allows.
type PluginUpdate struct {
	Name string `json:"name"`
	// From is the installed version, To the release's.
	From string `json:"from"`
	To   string `json:"to"`
	// SHA256 is the release's, as the Catalog lists it. Updating checks it
	// is still the one confirmed.
	SHA256 string `json:"sha256"`
	// NewEffects and NewHTTPHosts are the capabilities the release asks
	// for that the installed version doesn't have: updating to it needs a
	// new confirmation. Only an update preview fills them in, since they
	// are read from the release itself.
	NewEffects   []PluginEffect `json:"newEffects,omitempty"`
	NewHTTPHosts []string       `json:"newHttpHosts,omitempty"`
}

// UpdatePluginsRequest is the body of POST /v1/plugins/update: update
// installed Catalog Plugins to their latest allowed release.
type UpdatePluginsRequest struct {
	// Name is the Plugin to update; "" is every Catalog Plugin.
	Name string `json:"name,omitempty"`
	// Preview only finds the updates, downloading each release to read
	// what it may do, and installs nothing, so the developer can confirm
	// new capabilities first.
	Preview bool `json:"preview,omitempty"`
	// Confirmed are the updates to make, as a preview returned them: each
	// is installed only if the Catalog still lists the same release.
	Confirmed []PluginUpdate `json:"confirmed,omitempty"`
}

// UpdatePluginsResponse is the updates made (or, previewing, available).
type UpdatePluginsResponse struct {
	Updates []PluginUpdate `json:"updates"`
	// OutOfDate are the files Generators of an updated Plugin wrote that
	// they would now write differently. Laneway never rewrites them
	// itself.
	OutOfDate []OutOfDateFile `json:"outOfDate,omitempty"`
	// Warnings are problems that didn't stop the update.
	Warnings []string `json:"warnings,omitempty"`
}

// OutOfDateFile is a file in a Worktree that `lane generate <plugin>` would
// now change.
type OutOfDateFile struct {
	Plugin string `json:"plugin"`
	// Worktree is the Worktree's directory; Path the file, relative to it.
	Worktree string `json:"worktree"`
	Path     string `json:"path"`
}

// Error codes of POST /v1/plugins/update.
const (
	// CodeNotFromCatalog: the Plugin was added by hand, so the Catalog
	// can't update it.
	CodeNotFromCatalog = "not_from_catalog"
	// CodeCatalogChanged: the Catalog no longer lists a release that was
	// confirmed; preview the update again.
	CodeCatalogChanged = "catalog_changed"
)
