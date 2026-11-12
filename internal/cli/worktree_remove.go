package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
)

// runWorktreeRemove is `lane worktree remove [name] [--delete-branch]
// [--unlock] [--force]`: Remove a linked Worktree (the current one unless
// name names another) after saying what would be lost. A locked Worktree
// (`git worktree lock`, which agents and harnesses use while they work in
// one) is refused unless --unlock says to unlock it first. On a terminal it asks first
// (unless --force). Otherwise (a script, an agent) it goes ahead only when
// nothing is lost: the Worktree is clean, and its commits not on the
// Default Branch stay on its branch.
//
// With --merged it Removes every merged, clean Worktree of the current
// Project (of every Project with --all) instead; see removeMerged.
func runWorktreeRemove(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "worktree remove [name]")
	deleteBranch := fs.Bool("delete-branch", false, "also delete the branch when it has commits not on the Default Branch")
	force := fs.Bool("force", false, "remove it without asking, even if commits or changes are lost")
	unlock := fs.Bool("unlock", false, "unlock it first if it is locked (git worktree lock)")
	merged := fs.Bool("merged", false, "remove every merged, clean Worktree of the current Project instead")
	all := fs.Bool("all", false, "with --merged, those of every Project")
	// The name may come before or after the flags.
	if err := fs.Parse(args); err != nil {
		return parseFlags(fs, args)
	}
	var name string
	rest := fs.Args()
	if len(rest) > 0 {
		name, rest = rest[0], rest[1:]
	}
	if err := parseFlags(fs, rest); err != nil {
		return err
	}
	if strings.ContainsRune(name, filepath.Separator) || name == "." || name == ".." {
		if !filepath.IsAbs(name) {
			name = filepath.Join(env.Dir, name)
		}
	}
	switch {
	case *merged && name != "":
		return usageError{"--merged removes every merged, clean Worktree, so it takes no name"}
	case *merged && *deleteBranch:
		return usageError{"--merged removes merged Worktrees, whose branches are deleted anyway, so it takes no --delete-branch"}
	case *merged && *unlock:
		return usageError{"--merged leaves locked Worktrees alone, so it takes no --unlock; Remove each with `lane worktree remove <name> --unlock`"}
	case *all && !*merged:
		return usageError{"--all goes with --merged: `lane worktree remove --merged --all`"}
	}
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	if *merged {
		return removeMerged(ctx, env, c, *all, *force)
	}
	preview, err := c.RemoveWorktree(ctx, api.RemoveRequest{Dir: env.Dir, Worktree: name, Preview: true})
	if err != nil {
		return err
	}
	who := fmt.Sprintf("Worktree %q", preview.Worktree.Name)
	if preview.Locked && !*unlock {
		return api.LockedError(preview.Worktree.Name, preview.LockReason)
	}
	printRemoval(env.Stdout, preview, *deleteBranch)
	if preview.Locked {
		reason := ""
		if preview.LockReason != "" {
			reason = ": " + preview.LockReason
		}
		fmt.Fprintf(env.Stdout, "It is locked%s. It is unlocked first (--unlock).\n", reason)
	}
	// Commits are lost when no branch is left to keep them.
	loses := !preview.Clean || (!preview.Merged && (preview.Branch == "" || *deleteBranch))
	if !*force {
		switch {
		case env.Terminal:
			verb := "Remove"
			if preview.Locked {
				verb = "Unlock and remove"
			}
			question := fmt.Sprintf("%s %s? [y/N] ", verb, who)
			if !preview.Clean {
				question = fmt.Sprintf("%s %s and discard its changes? [y/N] ", verb, who)
			}
			if !confirm(env, question) {
				return errors.New("nothing was removed")
			}
		case loses:
			return errors.New("nothing was removed, because removing it loses what is listed above; run it again with --force to remove it anyway")
		}
	}

	req := api.RemoveRequest{Dir: env.Dir, Worktree: preview.Worktree.Path, Branch: preview.Branch, DeleteBranch: *deleteBranch, Unlock: *unlock}
	for _, cm := range preview.UnmergedCommits {
		req.UnmergedCommits = append(req.UnmergedCommits, cm.Hash)
	}
	for _, f := range preview.ChangedFiles {
		req.ChangedFiles = append(req.ChangedFiles, f.Path)
	}
	res, err := c.RemoveWorktree(ctx, req)
	if err != nil {
		return err
	}
	switch {
	case res.BranchDeleted:
		fmt.Fprintf(env.Stdout, "Removed %s and its branch %s\n", who, res.Branch)
	case res.Branch != "":
		fmt.Fprintf(env.Stdout, "Removed %s; its branch %s is kept\n", who, res.Branch)
	default:
		fmt.Fprintf(env.Stdout, "Removed %s\n", who)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(env.Stderr, "lane worktree remove: %s\n", w)
	}
	return nil
}

// printRemoval says what removing the Worktree, as the preview p shows it,
// loses: its unmerged commits, its changes, and what goes with it.
func printRemoval(w io.Writer, p api.RemoveResponse, deleteBranch bool) {
	on := "not on any branch"
	if p.Branch != "" {
		on = "on the branch " + p.Branch
	}
	fmt.Fprintf(w, "Worktree %q of %s is at %s, %s.\n", p.Worktree.Name, p.Project, p.Worktree.Path, on)
	if p.Merged {
		fmt.Fprintf(w, "It is merged into the Default Branch %s", p.DefaultBranch)
		if p.Branch != "" && p.Branch != p.DefaultBranch {
			fmt.Fprintf(w, ", so its branch %s is deleted with it", p.Branch)
		}
		fmt.Fprintln(w, ".")
	}
	if !p.Merged {
		fmt.Fprintf(w, "%s not on the Default Branch %s:\n", count(len(p.UnmergedCommits), "commit is", "commits are"), p.DefaultBranch)
		for _, c := range p.UnmergedCommits {
			fmt.Fprintf(w, "  %s %s (%s)\n", c.Hash[:min(len(c.Hash), 7)], c.Subject, ago(c.Time))
		}
		switch {
		case p.Branch == "":
			fmt.Fprintln(w, "No branch keeps them, so they become unreachable.")
		case deleteBranch:
			fmt.Fprintf(w, "The branch %s is deleted too (--delete-branch), so they become unreachable.\n", p.Branch)
		default:
			fmt.Fprintf(w, "The branch %s is kept, so they stay reachable; --delete-branch deletes it too.\n", p.Branch)
		}
		fmt.Fprintln(w, "A squash-merged branch also shows up here.")
	}
	if !p.Clean {
		fmt.Fprintf(w, "%s uncommitted changes, which are discarded:\n", count(len(p.ChangedFiles), "file has", "files have"))
		for _, f := range p.ChangedFiles {
			fmt.Fprintf(w, "  %s %s\n", f.Status, f.Path)
		}
		fmt.Fprintln(w, "Ignored files, such as .env, are deleted too.")
	}
	gone := "Its Processes stop"
	if len(p.Domains) > 0 {
		gone += ", its Domains (" + strings.Join(p.Domains, ", ") + ") are freed"
	}
	if len(p.Orphans) > 0 {
		var names []string
		for _, o := range p.Orphans {
			names = append(names, o.Name)
		}
		gone += fmt.Sprintf(", and its %s (%s) become Orphans, kept for 24 hours", orphansNoun(p.Orphans), strings.Join(names, ", "))
	}
	fmt.Fprintln(w, gone+".")
}

// count is "1 <one>" or "<n> <many>".
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ago is how long ago t was, roughly, such as "5 minutes ago".
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return count(int(d/time.Minute), "minute", "minutes") + " ago"
	case d < 48*time.Hour:
		return count(int(d/time.Hour), "hour", "hours") + " ago"
	}
	return count(int(d/(24*time.Hour)), "day", "days") + " ago"
}
