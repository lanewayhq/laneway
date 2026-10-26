package cli

import (
	"context"
	"fmt"

	"github.com/mrsafalpiya/portby.dev/internal/api"
)

// worktreeCommands are the `portby worktree <name>` subcommands.
var worktreeCommands = map[string]func(ctx context.Context, env Env, args []string) error{
	"alias": runWorktreeAlias,
}

func runWorktree(ctx context.Context, env Env, args []string) error {
	if len(args) == 0 {
		return usageError{"missing subcommand; use `portby worktree alias <name>`"}
	}
	run, ok := worktreeCommands[args[0]]
	if !ok {
		return usageError{fmt.Sprintf("unknown subcommand %q; use `portby worktree alias <name>`", args[0])}
	}
	return run(ctx, env, args[1:])
}

func runWorktreeAlias(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "worktree alias <name>")
	remove := fs.Bool("remove", false, "take the Alias off instead of adding it")
	// The name may come before or after the flags.
	if err := fs.Parse(args); err != nil {
		return parseFlags(fs, args)
	}
	if fs.NArg() == 0 {
		return usageError{"missing the Alias name, such as `portby worktree alias api-v2`"}
	}
	name := fs.Arg(0)
	if err := parseFlags(fs, fs.Args()[1:]); err != nil {
		return err
	}
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	if *remove {
		res, err := c.RemoveAlias(ctx, env.Dir, name)
		if err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "Removed the Alias %s from %s\n", res.Domain, describeWorktree(res.Worktree.Worktree))
		return nil
	}
	res, err := c.AddAlias(ctx, api.AliasRequest{Dir: env.Dir, Name: name})
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Added the Alias %s to %s: https://%s/\n", res.Domain, describeWorktree(res.Worktree.Worktree), res.Domain)
	return nil
}

// describeWorktree names a Worktree for people.
func describeWorktree(w api.Worktree) string {
	if w.Main {
		return "the main Worktree"
	}
	return fmt.Sprintf("Worktree %q", w.Name)
}
