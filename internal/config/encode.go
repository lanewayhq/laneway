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
	if l.IdleTimeout != nil {
		fmt.Fprintf(&b, "idle_timeout = %s\n", quote(l.IdleTimeout.String()))
	}
	if l.WorktreeRoot != nil {
		fmt.Fprintf(&b, "worktree_root = %s\n", quote(*l.WorktreeRoot))
	}
	if l.InheritSetup != nil {
		fmt.Fprintf(&b, "inherit_setup = %t\n", *l.InheritSetup)
	}
	for _, steps := range []struct {
		key   string
		steps []string
	}{{"copy", l.Copy}, {"on_create", l.OnCreate}, {"on_remove", l.OnRemove}} {
		if steps.steps == nil {
			continue
		}
		quoted := make([]string, len(steps.steps))
		for i, s := range steps.steps {
			quoted[i] = quote(s)
		}
		fmt.Fprintf(&b, "%s = [%s]\n", steps.key, strings.Join(quoted, ", "))
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
		if p.StartMode != nil {
			fmt.Fprintf(&b, "start_mode = %s\n", quote(*p.StartMode))
		}
		if p.HTTP != nil {
			fmt.Fprintf(&b, "http = %t\n", *p.HTTP)
		}
		if p.StaticDir != nil {
			fmt.Fprintf(&b, "static_dir = %s\n", quote(*p.StaticDir))
		}
		if p.Disabled != nil {
			fmt.Fprintf(&b, "disabled = %t\n", *p.Disabled)
		}
	}
	if db := l.Database; db != nil {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[database]\n")
		for _, kv := range []struct {
			key   string
			value *string
		}{{"engine", db.Engine}, {"path", db.Path}, {"worktree", db.Worktree}, {"test", db.Test}} {
			if kv.value != nil {
				fmt.Fprintf(&b, "%s = %s\n", kv.key, quote(*kv.value))
			}
		}
	}
	for _, env := range []struct {
		table     string
		templates map[string]string
	}{{"env", l.Env}, {"linked_env", l.LinkedEnv}} {
		if len(env.templates) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "[%s]\n", env.table)
		for _, k := range slices.Sorted(maps.Keys(env.templates)) {
			fmt.Fprintf(&b, "%s = %s\n", key(k), quote(env.templates[k]))
		}
	}
	if len(l.Services) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[services]\n")
		for _, k := range slices.Sorted(maps.Keys(l.Services)) {
			fmt.Fprintf(&b, "%s = %s\n", key(k), quote(l.Services[k]))
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
