package config

import (
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Encode writes l as TOML that ParseLayer reads back to the same Layer
// (ignoring Source and Warnings). Unset keys are left out.
func Encode(w io.Writer, l Layer) error {
	var b strings.Builder
	if l.Name != nil {
		fmt.Fprintf(&b, "name = %s\n", quote(*l.Name))
	}
	if l.StartTimeout != nil {
		fmt.Fprintf(&b, "start_timeout = %s\n", quote(l.StartTimeout.String()))
	}
	for _, name := range slices.Sorted(maps.Keys(l.Processes)) {
		p := l.Processes[name]
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "[processes.%s]\n", key(name))
		if p.Command != nil {
			fmt.Fprintf(&b, "command = %s\n", quote(*p.Command))
		}
		if p.Primary != nil {
			fmt.Fprintf(&b, "primary = %t\n", *p.Primary)
		}
		if p.HTTP != nil {
			fmt.Fprintf(&b, "http = %t\n", *p.HTTP)
		}
		if p.Disabled != nil {
			fmt.Fprintf(&b, "disabled = %t\n", *p.Disabled)
		}
	}
	if len(l.Env) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[env]\n")
		for _, k := range slices.Sorted(maps.Keys(l.Env)) {
			fmt.Fprintf(&b, "%s = %s\n", key(k), quote(l.Env[k]))
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// key writes a TOML key, quoted unless it's a valid bare key.
func key(k string) string {
	if bareKey.MatchString(k) {
		return k
	}
	return quote(k)
}

// quote writes s as a TOML basic string.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
