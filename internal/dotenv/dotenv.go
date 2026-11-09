// Package dotenv edits .env files in place. It changes only the values of the
// keys it is told to Set and keeps every other byte: comments, blank lines,
// ordering, `export` prefixes, inline comments, line endings and lines it
// doesn't understand.
//
// It follows the grammar of vlucas/phpdotenv, the parser Laravel uses, which
// differs from shell and godotenv rules: the last occurrence of a key wins,
// single quotes are fully literal, double quotes take \" \\ \$ escapes and
// may span lines, and an unquoted value ends at " #".
package dotenv

import (
	"os"
	"regexp"
	"slices"
	"strings"
)

// File is a parsed .env file.
type File struct {
	entries []entry
	newline string // the file's line ending, for lines Set appends
}

// entry is one logical line: a KEY=value assignment (which may span several
// physical lines when quoted), a comment, a blank line or anything else.
type entry struct {
	raw    string // the entry's text, without its line ending
	eol    string // "\n", "\r\n", or "" for a last line without one
	key    string // "" unless the entry is an assignment
	prefix string // "KEY=" or "export KEY = ", as written
	value  string // the value as written, quotes included
	suffix string // an inline comment with its leading space
}

var assignment = regexp.MustCompile(`^(\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_.]*)\s*=\s*)(.*)$`)

// Parse reads a .env file. It never fails: lines it doesn't understand are
// kept as they are.
func Parse(data []byte) *File {
	f := &File{newline: "\n"}
	lines := splitLines(string(data))
	if len(lines) > 0 && lines[0].eol == "\r\n" {
		f.newline = "\r\n"
	}
	for i := 0; i < len(lines); i++ {
		m := assignment.FindStringSubmatch(lines[i].text)
		if m == nil {
			f.entries = append(f.entries, entry{raw: lines[i].text, eol: lines[i].eol})
			continue
		}
		e := entry{key: m[2], prefix: m[1], eol: lines[i].eol}
		rest := m[3]
		switch q := firstByte(rest); {
		case q == '"' || q == '\'':
			// A quoted value continues over the following lines until its
			// closing quote.
			for closingQuote(rest, q) < 0 && i+1 < len(lines) {
				rest += e.eol
				i++
				rest += lines[i].text
				e.eol = lines[i].eol
			}
			if c := closingQuote(rest, q); c >= 0 {
				e.value, e.suffix = rest[:c+1], rest[c+1:]
			} else {
				e.value = rest // unterminated: kept verbatim
			}
		default:
			if c := strings.Index(rest, " #"); c >= 0 {
				e.value = strings.TrimRight(rest[:c], " \t")
				e.suffix = rest[len(e.value):]
			} else {
				e.value = rest
			}
		}
		e.raw = e.prefix + e.value + e.suffix
		f.entries = append(f.entries, e)
	}
	return f
}

type line struct{ text, eol string }

func splitLines(s string) []line {
	var lines []line
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			lines = append(lines, line{text: s})
			break
		}
		l := line{text: s[:i], eol: "\n"}
		if strings.HasSuffix(l.text, "\r") {
			l.text, l.eol = l.text[:len(l.text)-1], "\r\n"
		}
		lines = append(lines, l)
		s = s[i+1:]
	}
	return lines
}

func firstByte(s string) byte {
	if s == "" {
		return 0
	}
	return s[0]
}

// closingQuote is the index of the quote closing a value that starts with
// q, or -1.
func closingQuote(s string, q byte) int {
	for i := 1; i < len(s); i++ {
		if q == '"' && s[i] == '\\' {
			i++
			continue
		}
		if s[i] == q {
			return i
		}
	}
	return -1
}

// Get returns the value of key's last occurrence, as phpdotenv reads it,
// except that ${VAR} inside double quotes is not expanded.
func (f *File) Get(key string) (string, bool) {
	for i := len(f.entries) - 1; i >= 0; i-- {
		if f.entries[i].key == key {
			return decode(f.entries[i].value), true
		}
	}
	return "", false
}

// Environ is the env file at path as KEY=value environment entries, sorted
// by key, each value as Get reads it: what a command started after `set -a;
// . ./.env` sees. A missing or unreadable file sets nothing.
func Environ(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	f := Parse(data)
	var env []string
	seen := map[string]bool{}
	for i := len(f.entries) - 1; i >= 0; i-- {
		if k := f.entries[i].key; k != "" && !seen[k] {
			seen[k] = true
			env = append(env, k+"="+decode(f.entries[i].value))
		}
	}
	slices.Sort(env)
	return env
}

// Set gives key the value, written so phpdotenv reads back exactly value.
// Every occurrence is rewritten in place, keeping its `export` prefix and
// inline comment. A missing key is appended at the end of the file.
func (f *File) Set(key, value string) {
	q := Quote(value)
	found := false
	for i := range f.entries {
		if e := &f.entries[i]; e.key == key {
			e.value = q
			e.raw = e.prefix + e.value + e.suffix
			found = true
		}
	}
	if found {
		return
	}
	e := entry{raw: key + "=" + q, key: key, prefix: key + "=", value: q, eol: f.newline}
	if n := len(f.entries); n > 0 && f.entries[n-1].eol == "" {
		// Keep the file's missing final newline missing.
		f.entries[n-1].eol, e.eol = f.newline, ""
	}
	f.entries = append(f.entries, e)
}

// Bytes is the file's content.
func (f *File) Bytes() []byte {
	var b strings.Builder
	for _, e := range f.entries {
		b.WriteString(e.raw)
		b.WriteString(e.eol)
	}
	return []byte(b.String())
}

var safeUnquoted = regexp.MustCompile(`^[A-Za-z0-9_./:@+,=-]*$`)

// Quote writes v in the form phpdotenv reads back exactly: bare when that's
// unambiguous, else in single quotes (fully literal, so $, {, #, spaces and
// " need no escaping), else, for values with ' or a newline, in double
// quotes with \ " and $ escaped.
func Quote(v string) string {
	if safeUnquoted.MatchString(v) {
		return v
	}
	if !strings.ContainsAny(v, "'\n") {
		return "'" + v + "'"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`)
	return `"` + r.Replace(v) + `"`
}

func decode(v string) string {
	switch firstByte(v) {
	case '\'':
		return strings.TrimSuffix(v[1:], "'")
	case '"':
		inner := strings.TrimSuffix(v[1:], `"`)
		var b strings.Builder
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\\' && i+1 < len(inner) {
				switch inner[i+1] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				default:
					b.WriteByte(inner[i+1])
				}
				i++
				continue
			}
			b.WriteByte(inner[i])
		}
		return b.String()
	}
	return strings.TrimSpace(v)
}
