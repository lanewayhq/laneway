package plugin

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sync"

	"github.com/lanewayhq/laneway/plugins"
)

// BuiltinSource is the Source of a Plugin built into Laneway.
const BuiltinSource = "built into Laneway"

// builtin are the Plugins built into Laneway (package plugins), sorted by
// name, read once per process.
var builtin = sync.OnceValues(func() ([]Plugin, error) { return loadBuiltin(plugins.FS) })

// loadBuiltin reads the built-in Plugins in fsys: a directory per Plugin,
// named after it, holding its plugin.json and its module gzipped, as
// <module>.gz.
func loadBuiltin(fsys fs.FS) ([]Plugin, error) {
	manifests, err := fs.Glob(fsys, "*/"+ManifestFile)
	if err != nil {
		return nil, err
	}
	var all []Plugin
	for _, file := range manifests {
		dir := path.Dir(file)
		data, err := fs.ReadFile(fsys, file)
		if err != nil {
			return nil, err
		}
		m, err := parseManifest(data)
		if err != nil {
			return nil, fmt.Errorf("built-in Plugin %s: %w", dir, err)
		}
		if m.Name != dir {
			return nil, fmt.Errorf("built-in Plugin %s is named %s", dir, m.Name)
		}
		sub, err := fs.Sub(fsys, dir)
		if err != nil {
			return nil, err
		}
		p := Plugin{Manifest: m, Source: BuiltinSource, embedded: sub}
		if m.needsModule() {
			gz, err := fs.ReadFile(sub, m.module()+".gz")
			if err != nil {
				return nil, fmt.Errorf("built-in Plugin %s: %w; build it with `go generate ./plugins`", dir, err)
			}
			sum := sha256.Sum256(gz)
			p.SHA256 = hex.EncodeToString(sum[:])
		}
		all = append(all, p)
	}
	return all, nil
}

// embeddedModule is the module of the built-in Plugin p, ungzipped.
func embeddedModule(p Plugin) ([]byte, error) {
	gz, err := fs.ReadFile(p.embedded, p.module()+".gz")
	if err != nil {
		return nil, err
	}
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}
