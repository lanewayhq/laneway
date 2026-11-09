// Package fileblock merges file blocks into a Worktree's files. A file block
// is the effect a Generator returns (ADR-0007): a part of one file that
// Laneway owns, never the whole file, so that generated config can live in
// committed files beside the developer's own:
//
//   - JSON files are merged structurally: Laneway sets the keys it owns and
//     finds its own entries in arrays by what they contain, keeping every
//     other key and entry, their order and the file's indentation.
//   - Markdown files are merged between markers: everything between them is
//     Laneway's and is rewritten, everything else is left alone.
//
// Merging the same blocks again changes nothing, and a file is only written
// when its content changes. Every file is merged in memory first, so a file
// that can't be merged leaves all of them untouched.
package fileblock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Block is one file block: exactly one of Markdown, RemoveMarkdown and JSON
// says what Laneway owns in the file at Path.
type Block struct {
	// Path is the file, relative to the Worktree's root, with forward
	// slashes, such as ".claude/settings.json".
	Path string `json:"path"`
	// Markdown is the body of Laneway's marked block in the file, which is
	// created if it doesn't exist.
	Markdown *string `json:"markdown,omitempty"`
	// RemoveMarkdown takes Laneway's marked block out of the file, if it
	// has one.
	RemoveMarkdown bool `json:"remove_markdown,omitempty"`
	// JSON is what Laneway owns in the JSON object the file holds, which is
	// created if it doesn't exist.
	JSON *JSON `json:"json,omitempty"`
	// Warning is shown to the developer when the block changes the file,
	// such as a command to run afterwards.
	Warning string `json:"warning,omitempty"`
}

// JSON is what Laneway owns in a JSON file.
type JSON struct {
	// New is the object the file starts as when it doesn't exist yet, such
	// as {"$schema": "…"}; it is not added to an existing file.
	New json.RawMessage `json:"new,omitempty"`
	// Entries are what Laneway sets in the file, in order.
	Entries []Entry `json:"entries,omitempty"`
}

// Entry is one value Laneway owns in a JSON file. At is a path of object
// keys, objects on the way being added if missing. Without Match, Laneway
// owns the key At and sets it to Value. With Match, At holds an array and
// Laneway owns the element Match finds in it: Value's keys are set in that
// element (keeping the element's other keys), or Value is appended if no
// element matches.
//
// An element matches when it has every key of Match with a matching value:
// an object matches the same way, an array when each of its elements matches
// some element of the element's array, anything else when equal. Where
// Match has an array of objects under a key, Value's array under that key is
// merged into the element's the same way, so Laneway can own one entry in an
// array nested in an entry of another (such as one hook in a group of
// Claude Code hooks).
type Entry struct {
	At    []string        `json:"at"`
	Match json.RawMessage `json:"match,omitempty"`
	Value json.RawMessage `json:"value"`
}

// File is what merging blocks makes of one file.
type File struct {
	// Path is the file, relative to the Worktree's root.
	Path string
	// Old is the file before, nil if it doesn't exist; New is it after.
	Old, New []byte
}

// Created reports whether the file didn't exist; Changed whether it did and
// its content changes.
func (f File) Created() bool { return f.Old == nil && f.New != nil }
func (f File) Changed() bool { return f.Old != nil && !bytes.Equal(f.Old, f.New) }

// Validate checks a block without reading its file.
func (b Block) Validate() error {
	if b.Path == "" || path.IsAbs(b.Path) || !fs.ValidPath(b.Path) || b.Path == "." {
		return fmt.Errorf("file block path %q must be a file inside the Worktree, such as \"docs/AGENTS.md\"", b.Path)
	}
	n := 0
	for _, set := range []bool{b.Markdown != nil, b.RemoveMarkdown, b.JSON != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("file block for %s must set exactly one of markdown, remove_markdown and json", b.Path)
	}
	if b.JSON == nil {
		return nil
	}
	if len(b.JSON.New) > 0 {
		if v, err := parseValue(b.JSON.New); err != nil {
			return fmt.Errorf("file block for %s: json.new isn't valid JSON: %w", b.Path, err)
		} else if _, ok := v.(*object); !ok {
			return fmt.Errorf("file block for %s: json.new must be an object", b.Path)
		}
	}
	for i, e := range b.JSON.Entries {
		where := fmt.Sprintf("file block for %s: json.entries[%d]", b.Path, i)
		if len(e.At) == 0 {
			return fmt.Errorf("%s: at must name at least one key", where)
		}
		value, err := parseValue(e.Value)
		if err != nil {
			return fmt.Errorf("%s: value isn't valid JSON: %w", where, err)
		}
		if len(e.Match) > 0 {
			match, err := parseValue(e.Match)
			if err != nil {
				return fmt.Errorf("%s: match isn't valid JSON: %w", where, err)
			}
			if _, ok := match.(*object); !ok {
				return fmt.Errorf("%s: match must be an object", where)
			}
			if _, ok := value.(*object); !ok {
				return fmt.Errorf("%s: value must be an object when match is set", where)
			}
		}
	}
	return nil
}

// Merge merges blocks into the files of the Worktree at root, in memory, and
// returns the files they name, in the order first named, with the warnings
// of the blocks that change them. A file only named by RemoveMarkdown blocks
// that leave it as it is isn't returned. Several blocks may name one file;
// they merge in order. It writes nothing.
func Merge(root string, blocks []Block) ([]File, []string, error) {
	var files []*File
	byPath := map[string]*File{}
	named := map[string]bool{}
	var warnings []string
	for _, b := range blocks {
		if err := b.Validate(); err != nil {
			return nil, nil, err
		}
		f, ok := byPath[b.Path]
		if !ok {
			old, err := readFile(root, b.Path)
			if err != nil {
				return nil, nil, err
			}
			f = &File{Path: b.Path, Old: old, New: old}
			byPath[b.Path] = f
			files = append(files, f)
		}
		next, err := b.merge(f.New)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", b.Path, err)
		}
		changed := (f.New == nil) != (next == nil) || !bytes.Equal(f.New, next)
		if changed && b.Warning != "" {
			warnings = append(warnings, b.Warning)
		}
		named[b.Path] = named[b.Path] || changed || !b.RemoveMarkdown
		f.New = next
	}
	var out []File
	for _, f := range files {
		if named[f.Path] {
			out = append(out, *f)
		}
	}
	return out, warnings, nil
}

// Result is what Apply did.
type Result struct {
	// Files are the files the blocks name, as Merge returns them.
	Files []File
	// Warnings are the warnings of the blocks that changed their file.
	Warnings []string
}

// Apply merges blocks into the files of the Worktree at root and writes the
// files whose content changes. A block that can't be merged leaves every
// file untouched.
func Apply(root string, blocks []Block) (Result, error) {
	files, warnings, err := Merge(root, blocks)
	if err != nil {
		return Result{}, err
	}
	for _, f := range files {
		if f.Created() || f.Changed() {
			if err := writeFile(filepath.Join(root, filepath.FromSlash(f.Path)), f.New); err != nil {
				return Result{Files: files, Warnings: warnings}, err
			}
		}
	}
	return Result{Files: files, Warnings: warnings}, nil
}

// merge returns the file's content with b merged into old (nil: the file
// doesn't exist; the result is nil if it still doesn't).
func (b Block) merge(old []byte) ([]byte, error) {
	switch {
	case b.Markdown != nil:
		doc, err := mergeBlock(string(old), *b.Markdown)
		return []byte(doc), err
	case b.RemoveMarkdown:
		if old == nil {
			return nil, nil
		}
		doc, ok, err := removeBlock(string(old))
		if err != nil || !ok {
			return old, err
		}
		return []byte(doc), nil
	default:
		return b.JSON.merge(old)
	}
}

// merge merges j into the JSON object in old (nil: a new file), returning
// old as it was, formatting and all, if that changes nothing.
func (j JSON) merge(old []byte) ([]byte, error) {
	doc := newObject()
	switch {
	case old == nil && len(j.New) > 0:
		v, err := parseValue(j.New)
		if err != nil {
			return nil, err
		}
		doc = v.(*object)
	case len(bytes.TrimSpace(old)) > 0:
		var err error
		if doc, err = parseObject(old); err != nil {
			return nil, fmt.Errorf("it isn't valid JSON (%v); fix it and run this again", err)
		}
	}
	changed := false
	for _, e := range j.Entries {
		c, err := e.merge(doc)
		if err != nil {
			return nil, err
		}
		changed = changed || c
	}
	if !changed && old != nil {
		return old, nil
	}
	return encodeObject(doc, indentOf(old)), nil
}

// merge sets e in doc, reporting whether that changed it.
func (e Entry) merge(doc *object) (bool, error) {
	value, err := parseValue(e.Value)
	if err != nil {
		return false, err
	}
	parent := doc
	for i, key := range e.At[:len(e.At)-1] {
		if parent, err = objectAt(parent, key, strings.Join(e.At[:i+1], ".")); err != nil {
			return false, err
		}
	}
	key := e.At[len(e.At)-1]
	if len(e.Match) == 0 {
		if old, ok := parent.get(key); ok && equalJSON(old, value) {
			return false, nil
		}
		parent.set(key, value)
		return true, nil
	}
	match, err := parseValue(e.Match)
	if err != nil {
		return false, err
	}
	list, err := arrayAt(parent, key, strings.Join(e.At, "."))
	if err != nil {
		return false, err
	}
	if changed, found := mergeElement(list, match, value); found {
		return changed, nil
	}
	parent.set(key, append(list, value))
	return true, nil
}

// mergeElement merges value into the first element of list that match
// finds (see Entry), reporting whether it found one and changed it.
func mergeElement(list []any, match, value any) (changed, found bool) {
	for i, el := range list {
		if !matches(el, match) {
			continue
		}
		el, ok := el.(*object)
		v, vok := value.(*object)
		m, mok := match.(*object)
		if !ok || !vok || !mok {
			if equalJSON(list[i], value) {
				return false, true
			}
			list[i] = value
			return true, true
		}
		return mergeObject(el, m, v), true
	}
	return false, false
}

// mergeObject sets value's keys in el, merging arrays that match has an
// array for, and reports whether that changed el.
func mergeObject(el, match, value *object) bool {
	changed := false
	for _, k := range value.keys {
		v := value.vals[k]
		mList, mok := match.vals[k].([]any)
		vList, vok := v.([]any)
		elList, elok := el.vals[k].([]any)
		if mok && vok && elok && len(mList) == len(vList) {
			for i := range mList {
				c, found := mergeElement(elList, mList[i], vList[i])
				if !found {
					elList, c = append(elList, vList[i]), true
				}
				changed = changed || c
			}
			el.set(k, elList)
			continue
		}
		if old, ok := el.get(k); ok && equalJSON(old, v) {
			continue
		}
		el.set(k, v)
		changed = true
	}
	return changed
}

// matches reports whether v matches m (see Entry).
func matches(v, m any) bool {
	switch m := m.(type) {
	case *object:
		o, ok := v.(*object)
		if !ok {
			return false
		}
		for _, k := range m.keys {
			ov, ok := o.get(k)
			if !ok || !matches(ov, m.vals[k]) {
				return false
			}
		}
		return true
	case []any:
		list, ok := v.([]any)
		if !ok {
			return false
		}
		for _, mi := range m {
			found := false
			for _, item := range list {
				if matches(item, mi) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	return equalJSON(v, m)
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

// readFile returns the file at root/rel, or nil if it doesn't exist.
func readFile(root, rel string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if data == nil && err == nil {
		data = []byte{}
	}
	return data, err
}

// writeFile writes data to path, through a symlink if path is one.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
