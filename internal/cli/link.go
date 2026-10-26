package cli

import (
	"context"
	"fmt"

	"github.com/mrsafalpiya/portby.dev/internal/api"
)

func runLink(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "link")
	name := fs.String("name", "", "Project name, instead of `name` in portby.toml or the directory name")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	res, err := c.Link(ctx, api.LinkRequest{Dir: env.Dir, Name: *name})
	if err != nil {
		return err
	}
	if res.AlreadyLinked {
		fmt.Fprintf(env.Stdout, "This repository is already Linked as Project %q (%s)\n", res.Project.Name, res.Project.Path)
	} else {
		fmt.Fprintf(env.Stdout, "Linked Project %q (%s)\n", res.Project.Name, res.Project.Path)
	}
	if res.Worktree.Main {
		fmt.Fprintln(env.Stdout, "You're in the main Worktree.")
	} else {
		fmt.Fprintf(env.Stdout, "You're in Worktree %q (%s)\n", res.Worktree.Name, res.Worktree.Path)
	}
	return nil
}
