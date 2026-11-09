package sdk

import (
	"regexp"
	"strings"
)

// The SDK's own reader of .env files, so that it builds outside Laneway's
// module. It reads values the way Laneway's env file editor does (and
// vlucas/phpdotenv, the parser Laravel uses): the last occurrence of a key
// wins, single quotes are fully literal, double quotes take \" \\ \$ \n \t
// escapes and may span lines, and an unquoted value ends at " #".

var envAssignment = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_.]*)\s*=\s*(.*)$`)

// parseEnv reads the keys a .env file sets. It never fails: lines it doesn't
// understand are skipped.
func parseEnv(data []byte) map[string]string {
	values := map[string]string{}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		m := envAssignment.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		key, rest := m[1], m[2]
		if q := firstByte(rest); q == '"' || q == '\'' {
			// A quoted value continues over the following lines until its
			// closing quote.
			for closingQuote(rest, q) < 0 && i+1 < len(lines) {
				i++
				rest += "\n" + lines[i]
			}
			if c := closingQuote(rest, q); c >= 0 {
				rest = rest[:c+1]
			}
		} else if c := strings.Index(rest, " #"); c >= 0 {
			rest = rest[:c]
		}
		values[key] = decodeEnv(rest)
	}
	return values
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

// decodeEnv is the value v, as written, reads as; ${VAR} inside double
// quotes is not expanded.
func decodeEnv(v string) string {
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
