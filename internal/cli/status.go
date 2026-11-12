package cli

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/lanewayhq/laneway/internal/api"
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
		fmt.Fprintln(env.Stdout, "No Projects linked yet. Run `lane link` inside a git repository.")
		printPluginUpdates(ctx, env, c)
		return nil
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROJECT\tDOMAIN\tPATH\tREADINESS\tSTATE\tBRANCH\tMERGE\tPROCESSES")
	for _, p := range projects {
		// The main Worktree is the Project's own row; the others are
		// indented under it.
		for _, w := range p.Worktrees {
			name := p.Name
			if !w.Main {
				name = "  " + w.Name
			}
			setup := "-"
			if w.Setup != nil {
				setup = w.Setup.State
			}
			branch, merge := mergeColumns(p, w)
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s", name, w.Domain, w.Path, setup, cmp.Or(w.State, "-"), branch, merge)
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
	if err := tw.Flush(); err != nil {
		return err
	}
	// Below the table, which scripts read by column.
	for _, p := range projects {
		for _, w := range p.Warnings {
			fmt.Fprintf(env.Stdout, "\nwarning: Project %q: %s\n", p.Name, w)
		}
	}
	printPluginUpdates(ctx, env, c)
	return nil
}

// mergeColumns are the BRANCH and MERGE columns of `lane status` for the
// Worktree w of the Project p. Neither has spaces, so scripts can read the
// table by column. The main Worktree is on the Default Branch. A linked
// Worktree is "merged" or "unmerged:<commits>", then ",changed:<files>" if
// it has changed files.
func mergeColumns(p api.Project, w api.Worktree) (branch, merge string) {
	if w.Main {
		return cmp.Or(p.DefaultBranch, "-"), "default-branch"
	}
	m := w.Merge
	if m == nil {
		return "-", "-"
	}
	branch = cmp.Or(m.Branch, "(detached)")
	if m.Error != "" {
		return branch, "unknown"
	}
	merge = "merged"
	if m.UnmergedCount > 0 {
		merge = fmt.Sprintf("unmerged:%d", m.UnmergedCount)
	}
	if m.ChangedCount > 0 {
		merge += fmt.Sprintf(",changed:%d", m.ChangedCount)
	}
	return branch, merge
}
