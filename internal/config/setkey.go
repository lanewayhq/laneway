package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// SetString sets the top-level key to the string value in the TOML file at
// path, keeping everything else in it as it was: other keys, tables,
// comments and blank lines. An existing `key = …` line is replaced (with
// its trailing comment kept); otherwise the key goes after the last
// top-level key, or before the first table if there is none, since a key
// after a [table] header belongs to that table. A missing file, and its
// directories, are created.
//
// It refuses to write when the file isn't valid TOML, or when the result
// wouldn't read back as the same keys with key set, such as a key written
// in a form it doesn't recognise.
func SetString(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	before := map[string]any{}
	if _, err := toml.Decode(string(data), &before); err != nil {
		return decodeError(path, err)
	}
	out := setTopLevel(string(data), key, key+" = "+quote(value))
	after := map[string]any{}
	before[key] = value
	if _, err := toml.Decode(out, &after); err != nil || !reflect.DeepEqual(before, after) {
		return fmt.Errorf("%s: couldn't set %s without changing the rest of the file; set %s = %s at the top of it by hand",
			path, key, key, quote(value))
	}
	return writeAtomic(path, []byte(out))
}

// setTopLevel is content with the top-level key's line replaced by line,
// or line inserted where a top-level key belongs.
func setTopLevel(content, key, line string) string {
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	keyLine := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
	var sc tomlScanner
	found, foundEnd, foundComment := -1, -1, ""
	lastEnd, header := -1, -1
	start := -1 // the line the current top-level key/value began on
	for i, l := range lines {
		if sc.clean() {
			trimmed := strings.TrimSpace(l)
			if strings.HasPrefix(trimmed, "[") {
				header = i
				break
			}
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				start = i
			}
		}
		comment := sc.scan(l)
		if start >= 0 && sc.clean() {
			if keyLine.MatchString(lines[start]) {
				found, foundEnd, foundComment = start, i, ""
				if start == i {
					foundComment = comment
				}
			}
			lastEnd, start = i, -1
		}
	}
	line += "\n"
	switch {
	case found >= 0:
		if foundComment != "" {
			line = strings.TrimSuffix(line, "\n") + " " + foundComment + "\n"
		}
		return join(lines[:found], line, lines[foundEnd+1:])
	case lastEnd >= 0:
		if !strings.HasSuffix(lines[lastEnd], "\n") {
			line = "\n" + line
		}
		return join(lines[:lastEnd+1], line, lines[lastEnd+1:])
	case header >= 0:
		// Above the comments that introduce the first table.
		at := header
		for at > 0 && strings.HasPrefix(strings.TrimSpace(lines[at-1]), "#") {
			at--
		}
		return join(lines[:at], line+"\n", lines[at:])
	default:
		if len(lines) > 0 && !strings.HasSuffix(lines[len(lines)-1], "\n") {
			line = "\n" + line
		}
		return join(lines, line, nil)
	}
}

func join(head []string, line string, tail []string) string {
	return strings.Join(head, "") + line + strings.Join(tail, "")
}

// tomlScanner follows TOML's strings and brackets across lines, enough to
// tell where a value that spans lines (an array, a multi-line string) ends.
type tomlScanner struct {
	multiline string // the open multi-line string's quotes, if any
	depth     int    // open [ and {
}

// clean reports whether the scanner is between values: not in a string or
// an array or inline table.
func (s *tomlScanner) clean() bool { return s.multiline == "" && s.depth <= 0 }

// scan follows one line and returns its comment, if it has one.
func (s *tomlScanner) scan(line string) string {
	for i := 0; i < len(line); i++ {
		if s.multiline != "" {
			if strings.HasPrefix(line[i:], s.multiline) {
				i += len(s.multiline) - 1
				s.multiline = ""
			} else if s.multiline == `"""` && line[i] == '\\' {
				i++
			}
			continue
		}
		switch c := line[i]; c {
		case '#':
			return strings.TrimRight(line[i:], "\r\n")
		case '[', '{':
			s.depth++
		case ']', '}':
			s.depth--
		case '"', '\'':
			if q := strings.Repeat(string(c), 3); strings.HasPrefix(line[i:], q) {
				s.multiline = q
				i += 2
				continue
			}
			for i++; i < len(line) && line[i] != c && line[i] != '\n'; i++ {
				if c == '"' && line[i] == '\\' {
					i++
				}
			}
		}
	}
	return ""
}

// writeAtomic writes data to path through a temporary file renamed over
// it, keeping path's mode, so a reader never sees half a file.
func writeAtomic(path string, data []byte) error {
	mode := fs.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
