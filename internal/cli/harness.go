package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lanewayhq/laneway/internal/gitrepo"
	"github.com/lanewayhq/laneway/internal/harness"
)

// runHarness is `lane harness add <harness>`. Like `init`, it works on the
// current Worktree's files only, so it doesn't need the daemon.
func runHarness(ctx context.Context, env Env, args []string) error {
	names := strings.Join(harness.Names(), "|")
	if len(args) == 0 || args[0] != "add" {
		return usageError{fmt.Sprintf("use `lane harness add <%s>`", names)}
	}
	fs := newFlags(env, "harness add <"+names+">")
	if err := fs.Parse(args[1:]); err != nil {
		return parseFlags(fs, args[1:])
	}
	if fs.NArg() != 1 {
		return usageError{fmt.Sprintf("name one Harness: `lane harness add <%s>`", names)}
	}
	name := fs.Arg(0)

	checkout, err := gitrepo.Resolve(ctx, env.Dir)
	if errors.Is(err, gitrepo.ErrNotGitRepo) {
		return fmt.Errorf("%s is not inside a git repository; run `lane harness add` from a git repository", env.Dir)
	}
	if err != nil {
		return err
	}
	res, err := harness.Add(checkout.Dir, name)
	if errors.Is(err, harness.ErrUnknownHarness) {
		return usageError{fmt.Sprintf("unknown Harness %q; use one of: %s", name, strings.Join(harness.Names(), ", "))}
	}
	for _, c := range res.Changes {
		switch {
		case c.Created:
			fmt.Fprintf(env.Stdout, "Created %s\n", c.Path)
		case c.Changed:
			fmt.Fprintf(env.Stdout, "Updated %s\n", c.Path)
		default:
			fmt.Fprintf(env.Stdout, "%s is up to date\n", c.Path)
		}
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(env.Stderr, "warning: %s\n", w)
	}
	return err
}
