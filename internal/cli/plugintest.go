package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lanewayhq/laneway/internal/textdiff"
)

// runPluginTest runs `lane plugin test [dir] [--update]`: the Plugin in dir
// (the working directory by default) against its test cases, comparing what
// it returns with their golden files, or with --update rewriting them. It
// needs no daemon.
func runPluginTest(_ context.Context, env Env, args []string) error {
	fs := newFlags(env, "plugin test [dir]")
	update := fs.Bool("update", false, "rewrite the golden files with what the Plugin returns now")
	// The directory may come before or after the flags.
	if err := fs.Parse(args); err != nil {
		return parseFlags(fs, args)
	}
	src := env.Dir
	if fs.NArg() > 0 {
		src = fs.Arg(0)
		if err := parseFlags(fs, fs.Args()[1:]); err != nil {
			return err
		}
		if !filepath.IsAbs(src) {
			src = filepath.Join(env.Dir, src)
		}
	}
	h, err := openPlugins(env)
	if err != nil {
		return err
	}
	defer h.Close()
	cases, err := h.Test(src)
	if err != nil {
		return err
	}
	failed := 0
	for _, c := range cases {
		golden, err := filepath.Rel(src, c.Golden)
		if err != nil {
			golden = c.Golden
		}
		golden = filepath.ToSlash(golden)
		switch {
		case c.Err != nil:
			failed++
			fmt.Fprintf(env.Stdout, "FAIL %s: %s\n", c.Name, c.Err)
		case c.Passed():
			fmt.Fprintf(env.Stdout, "ok   %s\n", c.Name)
		case *update:
			if err := c.WriteGolden(); err != nil {
				return err
			}
			fmt.Fprintf(env.Stdout, "updated %s\n", golden)
		case c.Want == nil:
			failed++
			fmt.Fprintf(env.Stdout, "FAIL %s: %s doesn't exist yet\n", c.Name, golden)
		default:
			failed++
			fmt.Fprintf(env.Stdout, "FAIL %s: the Plugin returned something other than %s holds:\n", c.Name, golden)
			diff := textdiff.Unified(golden, "what the Plugin returned", string(c.Want), string(c.Got))
			fmt.Fprint(env.Stdout, indent(diff, "    "))
		}
	}
	if failed > 0 {
		hint := "; if what the Plugin returns now is right, run `lane plugin test --update` to rewrite the golden files"
		return fmt.Errorf("%d of %d test cases failed%s", failed, len(cases), hint)
	}
	return nil
}

// indent prefixes every line of s.
func indent(s, prefix string) string {
	if s == "" {
		return ""
	}
	return prefix + strings.ReplaceAll(strings.TrimSuffix(s, "\n"), "\n", "\n"+prefix) + "\n"
}
