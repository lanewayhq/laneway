// Package config decodes a Project's config files, laneway.toml and
// laneway.local.toml, one Layer per file. It doesn't merge them: layering
// over the Recipe's defaults belongs to the Recipe Resolver (package
// recipe), which is the only way the daemon reads config.
package config

// FileName is the committed per-Project config file.
const FileName = "laneway.toml"
