package cli

import (
	"context"
	"fmt"
)

func runOpen(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "open")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	info, err := c.Worktree(ctx, env.Dir)
	if err != nil {
		return err
	}
	if info.URL == "" {
		return fmt.Errorf("Worktree %q of Project %q has no Domain yet", info.Worktree.Name, info.Project.Name)
	}
	fmt.Fprintf(env.Stdout, "Opening %s\n", info.URL)
	return platformOf(env).OpenURL(ctx, info.URL)
}
