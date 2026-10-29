package cli

import (
	"context"
	"fmt"

	"github.com/mrsafalpiya/portby.dev/internal/api"
)

// worktreeCommands are the `portby worktree <name>` subcommands.
var worktreeCommands = map[string]func(ctx context.Context, env Env, args []string) error{
	"add":   runWorktreeAdd,
	"alias": runWorktreeAlias,
	"ready": runWorktreeReady,
}

func runWorktree(ctx context.Context, env Env, args []string) error {
	if len(args) == 0 {
		return usageError{"missing subcommand; use `portby worktree add <name>`, `portby worktree ready` or `portby worktree alias <name>`"}
	}
	run, ok := worktreeCommands[args[0]]
	if !ok {
		return usageError{fmt.Sprintf("unknown subcommand %q; use `portby worktree add <name>`, `portby worktree ready` or `portby worktree alias <name>`", args[0])}
	}
	return run(ctx, env, args[1:])
}

func runWorktreeAlias(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "worktree alias <name>")
	remove := fs.Bool("remove", false, "take the Alias off instead of adding it")
	worktree := worktreeFlag(fs, env)
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
		res, err := c.RemoveAlias(ctx, env.Dir, worktree(), name)
		if err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "Removed the Alias %s from %s%s\n", res.Domain, describeWorktree(res.Worktree.Worktree), urlMovedText(res))
		return nil
	}
	res, err := c.AddAlias(ctx, api.AliasRequest{Dir: env.Dir, Worktree: worktree(), Name: name})
	if err != nil {
		return err
	}
	switch {
	case res.Existed:
		fmt.Fprintf(env.Stdout, "%s is already an Alias of %s; its URL is %s\n",
			res.Domain, describeWorktree(res.Worktree.Worktree), res.Worktree.URL)
	case res.Moved:
		fmt.Fprintf(env.Stdout, "Added the Alias %s to %s%s\n", res.Domain, describeWorktree(res.Worktree.Worktree), urlMovedText(res))
	default:
		fmt.Fprintf(env.Stdout, "Added the Alias %s to %s: https://%s/\n", res.Domain, describeWorktree(res.Worktree.Worktree), res.Domain)
	}
	return nil
}

// urlMovedText is the text that ends the message about an Alias change:
// where the Worktree's URL is now if the change moved its Primary Domain,
// else nothing.
func urlMovedText(res api.AliasResponse) string {
	if !res.Moved {
		return ""
	}
	return "; its URL is now " + res.Worktree.URL
}

// describeWorktree names a Worktree for people.
func describeWorktree(w api.Worktree) string {
	if w.Main {
		return "the main Worktree"
	}
	return fmt.Sprintf("Worktree %q", w.Name)
}
