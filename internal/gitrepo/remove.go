package gitrepo

import "context"

// RemoveWorktree runs `git worktree remove` on the linked Worktree at dir, of
// the repository whose main Worktree is at mainDir. Git refuses a Worktree
// with changed files unless discard is set; ignored files go either way.
func RemoveWorktree(ctx context.Context, mainDir, dir string, discard bool) error {
	args := []string{"worktree", "remove"}
	if discard {
		args = append(args, "--force")
	}
	_, err := run(ctx, mainDir, append(args, "--", dir)...)
	return err
}

// DeleteBranch deletes the local branch, whether or not git thinks it is
// merged: the caller has checked against the Default Branch, which git's
// own check (HEAD and the upstream) doesn't know about.
func DeleteBranch(ctx context.Context, mainDir, branch string) error {
	_, err := run(ctx, mainDir, "branch", "--delete", "--force", "--", branch)
	return err
}
