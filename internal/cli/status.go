package cli

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"text/tabwriter"
)

func runStatus(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "status")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	projects, err := c.Projects(ctx)
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		fmt.Fprintln(env.Stdout, "No Projects linked yet. Run `portby link` inside a git repository.")
		return nil
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROJECT\tDOMAIN\tPATH\tSTATE\tPROCESSES")
	for _, p := range projects {
		// The main Worktree is the Project's own row; the others are
		// indented under it.
		for _, w := range p.Worktrees {
			name := p.Name
			if !w.Main {
				name = "  " + w.Name
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s", name, w.Domain, w.Path, cmp.Or(w.State, "-"))
			if len(w.Processes) > 0 {
				var procs []string
				for _, proc := range w.Processes {
					procs = append(procs, proc.Name+":"+proc.State)
				}
				fmt.Fprintf(tw, "\t%s", strings.Join(procs, ","))
			}
			fmt.Fprintln(tw)
		}
	}
	return tw.Flush()
}
