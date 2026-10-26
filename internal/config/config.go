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
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// FileName is the committed per-Project config file.
const FileName = "portby.toml"

// File is the decoded contents of portby.toml.
type File struct {
	// Name is the Project name, overriding the directory name.
	Name string `toml:"name"`
	// Processes are keyed by Process name ([processes.<name>] tables).
	Processes map[string]Process `toml:"processes"`
}

// Process is one [processes.<name>] table.
type Process struct {
	// Command is run with `sh -c` in the Worktree. Every `{port}` in it is
	// replaced with the port Portby picked for the Process, which is also
	// in $PORT.
	Command string `toml:"command"`
	// Primary marks the Process that serves the Worktree's Domain. A Project
	// with a single Process doesn't need to set it.
	Primary bool `toml:"primary"`
}

// PrimaryProcess returns the name of the Primary Process: the one marked
// primary, or the only Process if there is just one.
func (f File) PrimaryProcess() (string, bool) {
	for name, p := range f.Processes {
		if p.Primary || len(f.Processes) == 1 {
			return name, true
		}
	}
	return "", false
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
	if err := f.validate(); err != nil {
		return File{}, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

func (f File) validate() error {
	var primaries []string
	for name, p := range f.Processes {
		if strings.TrimSpace(p.Command) == "" {
			return fmt.Errorf("Process %q has no command", name)
		}
		if p.Primary {
			primaries = append(primaries, name)
		}
	}
	if len(primaries) > 1 {
		slices.Sort(primaries)
		return fmt.Errorf("only one Process can be the Primary Process, but %s are all marked primary",
			strings.Join(primaries, ", "))
	}
	return nil
}
