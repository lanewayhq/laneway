// Package plugins holds the first-party Plugins built into Laneway (spec #48,
// phase 3): the laravel and rails Recipes and the vite Integration. Each is
// an ordinary Plugin written with the Go SDK, in a directory of its own with
// its plugin.json and its module, built by `go generate` and embedded here
// gzipped (see generate.go). They move to lanewayhq/plugins, and out of the
// binary, with the Catalog (ADR-0007).
package plugins

import "embed"

//go:generate go run generate.go

// FS holds each built-in Plugin's directory: <name>/plugin.json and, for a
// Plugin with a module, <name>/<module>.gz.
//
//go:embed */plugin.json */plugin.wasm.gz
var FS embed.FS
