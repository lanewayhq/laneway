package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/client"
)

// removeMerged is `lane worktree remove --merged [--all] [--force]`: it
// lists every merged, clean, unlocked Worktree of the current Project (of
// every Project with all), as GET /v1/projects reports them, asks once (unless force) and Removes them. Removing
// them loses nothing, but a fresh Worktree someone is about to work in is
// merged and clean too, so without a terminal it needs force.
//
// The daemon checks each one again as it removes it, so one that changed
// since the listing, or was locked, is skipped and reported, and the
// command fails.
func removeMerged(ctx context.Context, env Env, c *client.Client, all, force bool) error {
	projects, err := c.Projects(ctx)
	if err != nil {
		return err
	}
	scope := "any Project"
	if !all {
		info, err := c.Worktree(ctx, env.Dir, "")
		if err != nil {
			return err
		}
		scope = info.Project.Name
		for _, p := range projects {
			if p.Path == info.Project.Path {
				projects = []api.Project{p}
				break
			}
		}
	}

	type listed struct {
		project string
		w       api.Worktree
	}
	var list []listed
	for _, p := range projects {
		for _, w := range p.Worktrees {
			if m := w.Merge; !w.Main && m != nil && m.Merged && m.Clean && !m.Locked && m.Error == "" {
				list = append(list, listed{p.Name, w})
			}
		}
	}
	if len(list) == 0 {
		fmt.Fprintf(env.Stdout, "No Worktree of %s is merged and clean, so there is nothing to remove.\n", scope)
		return nil
	}

	fmt.Fprintf(env.Stdout, "%s merged into the Default Branch and clean:\n", count(len(list), "Worktree is", "Worktrees are"))
	for _, l := range list {
		on := "not on any branch"
		if l.w.Merge.Branch != "" {
			on = "on the branch " + l.w.Merge.Branch
		}
		fmt.Fprintf(env.Stdout, "  %q of %s, %s, at %s\n", l.w.Name, l.project, on, l.w.Path)
	}
	fmt.Fprintln(env.Stdout, "Removing them deletes their merged branches too. Their Processes stop, their Domains are freed, "+
		"and their databases and Buckets become Orphans, kept for 24 hours.")
	if !force {
		if !env.Terminal {
			return errors.New("nothing was removed, because no one is here to confirm it; run it again with --force to remove them without asking")
		}
		question := "Remove this Worktree? [y/N] "
		if len(list) > 1 {
			question = fmt.Sprintf("Remove these %d Worktrees? [y/N] ", len(list))
		}
		if !confirm(env, question) {
			return errors.New("nothing was removed")
		}
	}

	// The listing is the preview: a merged, clean Worktree loses nothing,
	// so sending only its branch removes it if it is still so.
	var req api.RemoveAllRequest
	for _, l := range list {
		req.Worktrees = append(req.Worktrees, api.RemoveRequest{Dir: l.w.Path, Branch: l.w.Merge.Branch})
	}
	res, err := c.RemoveWorktrees(ctx, req)
	if err != nil {
		return err
	}
	skipped := 0
	for i, r := range res.Results {
		who := fmt.Sprintf("Worktree %q of %s", list[i].w.Name, list[i].project)
		switch {
		case r.Error != nil:
			skipped++
			fmt.Fprintf(env.Stderr, "lane worktree remove: skipped %s: %s\n", who, r.Error.Message)
			continue
		case r.BranchDeleted:
			fmt.Fprintf(env.Stdout, "Removed %s and its branch %s\n", who, r.Branch)
		case r.Branch != "":
			fmt.Fprintf(env.Stdout, "Removed %s; its branch %s is kept\n", who, r.Branch)
		default:
			fmt.Fprintf(env.Stdout, "Removed %s\n", who)
		}
		for _, w := range r.Warnings {
			fmt.Fprintf(env.Stderr, "lane worktree remove: %s\n", w)
		}
	}
	if skipped > 0 {
		return fmt.Errorf("%d of %d Worktrees were not removed, as said above", skipped, len(list))
	}
	return nil
}
