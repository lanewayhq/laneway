package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mrsafalpiya/portby.dev/internal/api"
)

// control returns the run func of `portby up`, `down` or `restart`.
func control(action string) func(ctx context.Context, env Env, args []string) error {
	return func(ctx context.Context, env Env, args []string) error {
		fs := newFlags(env, action+" [process]")
		worktree := fs.String("worktree", "", "act on this Worktree of the Project (git's name, Domain or path) instead of the current one")
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
		// A relative path is relative to where the command runs.
		if w := *worktree; strings.ContainsRune(w, filepath.Separator) || w == "." || w == ".." {
			if !filepath.IsAbs(w) {
				w = filepath.Join(env.Dir, w)
			}
			*worktree = filepath.Clean(w)
		}
		c, err := apiClient(env)
		if err != nil {
			return err
		}
		w, err := c.Control(ctx, action, api.ControlRequest{Dir: env.Dir, Worktree: *worktree, Process: process})
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
