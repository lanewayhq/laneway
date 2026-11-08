package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
)

// runGC is `lane gc [--yes]`: drop Orphans and leftover parallel test
// copies now. On a terminal it shows them and asks first (unless --yes);
// otherwise (a script, an agent) it drops them at once and says what it
// dropped.
func runGC(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "gc")
	yes := fs.Bool("yes", false, "drop the databases without asking")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	preview, err := c.GC(ctx, api.GCRequest{Preview: true})
	if err != nil {
		return err
	}
	for _, e := range preview.Errors {
		fmt.Fprintf(env.Stderr, "lane gc: %s\n", e)
	}
	if len(preview.Orphans) == 0 {
		fmt.Fprintln(env.Stdout, "Nothing to drop: there are no Orphans or leftover parallel test copies.")
		return nil
	}
	if !*yes && env.Terminal {
		fmt.Fprintln(env.Stdout, "These databases will be dropped, and their data lost:")
		printOrphans(env.Stdout, preview.Orphans)
		if !confirm(env, fmt.Sprintf("Drop these %d databases? [y/N] ", len(preview.Orphans))) {
			return errors.New("nothing was dropped; run `lane gc --yes` to drop them without asking")
		}
	}
	req := api.GCRequest{}
	for _, o := range preview.Orphans {
		req.Databases = append(req.Databases, api.OrphanRef{Service: o.Service, Database: o.Database})
	}
	res, err := c.GC(ctx, req)
	if err != nil {
		return err
	}
	for _, o := range res.Orphans {
		fmt.Fprintf(env.Stdout, "Dropped %s (%s)\n", o.Database, o.Service)
	}
	if len(res.Errors) > 0 {
		return errors.New(strings.Join(res.Errors, "\nlane gc: "))
	}
	return nil
}

// printOrphans shows Orphans as a table.
func printOrphans(w io.Writer, orphans []api.Orphan) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  DATABASE\tSERVICE\tPROJECT\tWORKTREE\tKIND\tORPHAN SINCE")
	for _, o := range orphans {
		worktree, since := o.Worktree, "-"
		if worktree == "" {
			worktree = "-"
		}
		if !o.Since.IsZero() {
			since = o.Since.Local().Format(time.DateTime)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n", o.Database, o.Service, o.Project, worktree, o.Kind, since)
	}
	tw.Flush()
}
