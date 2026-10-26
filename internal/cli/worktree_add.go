package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mrsafalpiya/portby.dev/internal/gitrepo"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
)

// runWorktreeAdd creates a git worktree under the Project's worktree root
// (worktree_root, by default next to the main Worktree; see
// recipe.DefaultWorktreeRoot). It works on git only, so it doesn't need the
// daemon: the Worktree Watcher finds the new worktree like any other.
func runWorktreeAdd(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "worktree add <name>")
	branch := fs.String("branch", "", "check out this branch, creating it if it doesn't exist (default: a new branch named <name>)")
	// The name may come before or after the flags.
	if err := fs.Parse(args); err != nil {
		return parseFlags(fs, args)
	}
	if fs.NArg() == 0 {
		return usageError{"missing the Worktree name, such as `portby worktree add feature-x`"}
	}
	name := fs.Arg(0)
	if err := parseFlags(fs, fs.Args()[1:]); err != nil {
		return err
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, "-") {
		return usageError{fmt.Sprintf("%q can't be a Worktree name: it names a directory, so no slashes", name)}
	}
	if *branch == "" {
		*branch = name
	}
	checkout, err := gitrepo.Resolve(ctx, env.Dir)
	if errors.Is(err, gitrepo.ErrNotGitRepo) {
		return fmt.Errorf("%s is not inside a git repository; run `portby worktree add` in a Project", env.Dir)
	}
	if err != nil {
		return err
	}
	cfg, err := recipe.ResolveDir(checkout.MainDir)
	if err != nil {
		return err
	}
	dir := filepath.Join(cfg.WorktreeRootOf(checkout.MainDir), name)
	if err := gitrepo.AddWorktree(ctx, checkout.MainDir, dir, *branch); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Created Worktree %q on branch %s at %s\n", name, *branch, dir)
	fmt.Fprintf(env.Stdout, "Portby sets it up once it finds it; run `portby worktree ready` there to wait.\n")
	return nil
}
