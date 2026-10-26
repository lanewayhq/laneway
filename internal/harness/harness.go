// Package harness holds the Harness Generators behind `portby harness add`:
// they merge config into a Worktree's files so that a Harness (Claude Code,
// T3 Code) waits until its Worktree is Ready, and so that agents know how to
// find its URL and run commands in it.
//
// Generated files are committed, so everything generated must be harmless for
// teammates without Portby (see ReadyCommand) and must never overwrite
// anyone's own config: JSON files are merged structurally, keeping every
// existing entry in its place; Markdown files are merged between markers.
// Running a generator again changes nothing.
package harness

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// ReadyCommand is the shell command Harness hooks run: `portby worktree
// ready`, guarded so that it succeeds quietly when `portby` isn't installed
// or there is nothing to wait for (exit 3: not a Linked repository, or no
// daemon). It still fails when setup Failed (1) or waiting gave up (4).
// It runs under `sh` because T3 Code types its scripts into the developer's
// interactive shell, which may not be POSIX and where `exit` would close the
// terminal.
const ReadyCommand = `sh -c 'command -v portby >/dev/null 2>&1 || exit 0; portby worktree ready || [ $? -eq 3 ]'`

// readyTimeout is how long a Harness may let ReadyCommand run, in seconds.
// It is longer than `portby worktree ready`'s own 10 minute timeout, so that
// Portby gives up first and says why.
const readyTimeout = 660

// A generator returns the files it changes in the Worktree at root.
type generator func(root string) ([]edit, error)

var generators = map[string]generator{
	"claude": claude,
	"t3":     t3,
}

// Names are the Harnesses that have a generator, sorted.
func Names() []string {
	names := make([]string, 0, len(generators))
	for name := range generators {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ErrUnknownHarness is returned by Add for a Harness without a generator.
var ErrUnknownHarness = errors.New("unknown Harness")

// Change is what Add did to one file.
type Change struct {
	// Path is the file, relative to the Worktree's root.
	Path string
	// Created: the file didn't exist. Changed: it did, and was rewritten.
	// Neither: it was already up to date.
	Created, Changed bool
}

// Result is what Add did.
type Result struct {
	Changes []Change
	// Warnings are things the developer should know about, such as config
	// of their own that stops the generated config from working.
	Warnings []string
}

// Add runs the named Harness's generator on the Worktree at root. Every file
// is merged in memory first, so a file it can't merge leaves all of them
// untouched.
func Add(root, harness string) (Result, error) {
	gen, ok := generators[harness]
	if !ok {
		return Result{}, fmt.Errorf("%w %q", ErrUnknownHarness, harness)
	}
	edits, err := gen(root)
	if err != nil {
		return Result{}, err
	}
	var res Result
	for _, e := range edits {
		res.Warnings = append(res.Warnings, e.warnings...)
		c := Change{Path: e.path}
		if e.old == nil || !bytes.Equal(e.old, e.new) {
			c.Created, c.Changed = e.old == nil, e.old != nil
			if err := writeFile(filepath.Join(root, e.path), e.new); err != nil {
				return res, err
			}
		}
		res.Changes = append(res.Changes, c)
	}
	return res, nil
}

// edit is a generator's new content for one file.
type edit struct {
	path     string // relative to the Worktree's root
	old, new []byte // old is nil if the file doesn't exist
	warnings []string
}

// readFile returns the file at root/rel, or nil if it doesn't exist.
func readFile(root, rel string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(root, rel))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if data == nil && err == nil {
		data = []byte{}
	}
	return data, err
}

// writeFile writes data to path, through a symlink if path is one, keeping
// an existing file's mode.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// mergeJSON merges into the JSON object in old (nil: a new file) with merge,
// and returns the new file. A file whose content merge doesn't change is
// returned as it was, formatting and all.
func mergeJSON(path string, old []byte, merge func(*object) (bool, error)) ([]byte, error) {
	doc := newObject()
	if len(bytes.TrimSpace(old)) > 0 {
		var err error
		if doc, err = parseObject(old); err != nil {
			return nil, fmt.Errorf("%s isn't valid JSON (%v); fix it and run this again", path, err)
		}
	}
	changed, err := merge(doc)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if !changed && old != nil {
		return old, nil
	}
	return encodeObject(doc, indentOf(old)), nil
}

// objectAt returns the object under key in o, adding an empty one if key is
// missing. where names the key for errors.
func objectAt(o *object, key, where string) (*object, error) {
	v, ok := o.get(key)
	if !ok {
		child := newObject()
		o.set(key, child)
		return child, nil
	}
	child, ok := v.(*object)
	if !ok {
		return nil, fmt.Errorf("%s is not an object", where)
	}
	return child, nil
}

// arrayAt returns the array under key in o (nil if key is missing).
func arrayAt(o *object, key, where string) ([]any, error) {
	v, ok := o.get(key)
	if !ok {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an array", where)
	}
	return list, nil
}
