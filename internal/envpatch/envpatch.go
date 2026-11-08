// Package envpatch is the Env Patcher. It renders the Env Patch templates
// for a Worktree and writes only the keys Laneway owns into its env file,
// leaving every other line, comment and their order as they are. A patch is
// planned first (Prepare), so it can be shown and confirmed, and then
// applied (Apply). Applying a patch that is already in place changes
// nothing, so it is safe to re-apply whenever its inputs change.
package envpatch

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/lanewayhq/laneway/internal/dotenv"
)

// BackupSuffix is added to an env file's name for the copy of the original
// that Laneway keeps from before it first patched it: .env.laneway.bak.
const BackupSuffix = ".laneway.bak"

// RenderAll renders every template (env key → template) with vars. Errors
// name the key.
func RenderAll(templates, vars map[string]string) (map[string]string, error) {
	values := make(map[string]string, len(templates))
	for _, key := range slices.Sorted(maps.Keys(templates)) {
		v, err := Render(templates[key], vars)
		if err != nil {
			return nil, fmt.Errorf("env.%s: %w", key, err)
		}
		values[key] = v
	}
	return values, nil
}

// File is an env file to patch.
type File struct {
	// Path is the env file, such as <worktree>/.env.
	Path string
	// Seeds are the files Path starts as a copy of when it doesn't exist
	// yet, the first that exists winning: for a new Worktree, the main
	// Worktree's .env and then its own .env.example. With none, Path is
	// created holding only Laneway's keys.
	Seeds []string
}

// Change is what happens to one key Laneway owns.
type Change struct {
	Key string
	// Old is the value before; "" when Added.
	Old string
	New string
	// Added is true when the file didn't have the key.
	Added bool
}

// Plan is how an env file will change. The zero Plan changes nothing.
type Plan struct {
	// Path is the env file.
	Path string
	// Create is true when Path doesn't exist yet.
	Create bool
	// CopyFrom is the seed Path is created as a copy of; "" when it
	// exists already or is created from nothing.
	CopyFrom string
	// Backup is where Apply first copies the original; "" when it won't,
	// because the file is new or a backup exists from an earlier patch.
	Backup string
	// Changes are the keys that change, sorted by key.
	Changes []Change

	content []byte
	target  string // Path with symlinks resolved: where content goes
	mode    fs.FileMode
}

// Empty reports whether applying the plan would change nothing.
func (p Plan) Empty() bool { return !p.Create && len(p.Changes) == 0 }

// Prepare works out how f changes when the keys in values (env key →
// rendered value) are set. It reads files but writes nothing.
func Prepare(f File, values map[string]string) (Plan, error) {
	p := Plan{Path: f.Path, target: f.Path, mode: 0o644}
	data, err := os.ReadFile(f.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		p.Create = true
		for _, seed := range f.Seeds {
			data, err = os.ReadFile(seed)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return Plan{}, err
			}
			p.CopyFrom = seed
			if info, err := os.Stat(seed); err == nil {
				p.mode = info.Mode().Perm()
			}
			break
		}
	case err != nil:
		return Plan{}, err
	default:
		// Write through a symlinked .env rather than replacing the link.
		if p.target, err = filepath.EvalSymlinks(f.Path); err != nil {
			return Plan{}, err
		}
		info, err := os.Stat(p.target)
		if err != nil {
			return Plan{}, err
		}
		p.mode = info.Mode().Perm()
		backup := f.Path + BackupSuffix
		if _, err := os.Lstat(backup); errors.Is(err, fs.ErrNotExist) {
			p.Backup = backup
		}
	}

	env := dotenv.Parse(data)
	for _, key := range slices.Sorted(maps.Keys(values)) {
		old, ok := env.Get(key)
		if ok && old == values[key] {
			continue
		}
		p.Changes = append(p.Changes, Change{Key: key, Old: old, New: values[key], Added: !ok})
		env.Set(key, values[key])
	}
	if p.Empty() {
		return Plan{}, nil
	}
	p.content = env.Bytes()
	return p, nil
}

// Apply makes the planned change: it backs up the original if the plan
// says so, then replaces the file atomically, keeping its permissions.
func (p Plan) Apply() error {
	if p.Empty() {
		return nil
	}
	if p.Backup != "" {
		original, err := os.ReadFile(p.target)
		if err != nil {
			return err
		}
		if err := writeNew(p.Backup, original, p.mode); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("backing up %s: %w", p.Path, err)
		}
	}
	return writeAtomic(p.target, p.content, p.mode)
}

// writeNew writes a file that must not exist yet.
func writeNew(path string, data []byte, mode fs.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

// writeAtomic replaces path with data: it writes a temporary file in the
// same directory and renames it over path, so readers see the old or the
// new file, never a half-written one.
func writeAtomic(path string, data []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".laneway-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
