// Package config reads a Project's committed portby.toml. Only the fields the
// daemon uses so far are decoded; unknown keys are ignored so the file can
// grow without breaking older code. Layering with Recipe defaults and
// portby.local.toml belongs to the Recipe Resolver.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// FileName is the committed per-Project config file.
const FileName = "portby.toml"

// File is the decoded contents of portby.toml.
type File struct {
	// Name is the Project name, overriding the directory name.
	Name string `toml:"name"`
}

// Load reads portby.toml from dir. A missing file is not an error: it yields
// an empty File.
func Load(dir string) (File, error) {
	path := filepath.Join(dir, FileName)
	var f File
	_, err := toml.DecodeFile(path, &f)
	if errors.Is(err, os.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, fmt.Errorf("reading %s: %w", path, err)
	}
	return f, nil
}
