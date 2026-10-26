package cli

import (
	"context"
	"fmt"

	"github.com/mrsafalpiya/portby.dev/internal/api"
)

// worktreeCommands are the `portby worktree <name>` subcommands.
var worktreeCommands = map[string]func(ctx context.Context, env Env, args []string) error{
	"add":   runWorktreeAdd,
	"ready": runWorktreeReady,
}

func runWorktree(ctx context.Context, env Env, args []string) error {
	if len(args) == 0 {
		return usageError{"missing subcommand; use `portby worktree add <name>` or `portby worktree ready`"}
	}
	run, ok := worktreeCommands[args[0]]
	if !ok {
		return usageError{fmt.Sprintf("unknown subcommand %q; use `portby worktree add <name>` or `portby worktree ready`", args[0])}
	}
	return run(ctx, env, args[1:])
}

// worktreeName names a Worktree for people.
func worktreeName(w api.Worktree) string {
	if w.Main {
		return "the main Worktree"
	}
	return fmt.Sprintf("Worktree %q", w.Name)
}
