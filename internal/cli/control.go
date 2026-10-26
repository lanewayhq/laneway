package cli

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mrsafalpiya/portby.dev/internal/api"
)

// control returns the run func of `portby up`, `down` or `restart`.
func control(action string) func(ctx context.Context, env Env, args []string) error {
	return func(ctx context.Context, env Env, args []string) error {
		fs := newFlags(env, action+" [process]")
		worktree := worktreeFlag(fs, env)
		// The Process name may come before or after the flags.
		if err := fs.Parse(args); err != nil {
			return parseFlags(fs, args)
		}
		var process string
		if fs.NArg() > 0 {
			process = fs.Arg(0)
			if err := parseFlags(fs, fs.Args()[1:]); err != nil {
				return err
			}
		}
		c, err := apiClient(env)
		if err != nil {
			return err
		}
		w, err := c.Control(ctx, action, api.ControlRequest{Dir: env.Dir, Worktree: worktree(), Process: process})
		if err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "%s is %s", w.Domain, w.State)
		if len(w.Processes) > 0 {
			var procs []string
			for _, p := range w.Processes {
				procs = append(procs, p.Name+":"+p.State)
			}
			fmt.Fprintf(env.Stdout, " (%s)", strings.Join(procs, ","))
		}
		fmt.Fprintln(env.Stdout)
		return nil
	}
}

// worktreeFlag adds --worktree to fs, for a command that acts on the current
// directory's Worktree unless it names another. The func returns its value
// once fs is parsed, a relative path made absolute against env.Dir: git's
// name for a Worktree of the current Project (or the Project's name for its
// main Worktree), a Domain or Alias, or a path.
func worktreeFlag(fs *flag.FlagSet, env Env) func() string {
	w := fs.String("worktree", "", "act on this Worktree (git's name, Domain or path) instead of the current one")
	return func() string {
		if strings.ContainsRune(*w, filepath.Separator) || *w == "." || *w == ".." {
			if !filepath.IsAbs(*w) {
				return filepath.Join(env.Dir, *w)
			}
			return filepath.Clean(*w)
		}
		return *w
	}
}
