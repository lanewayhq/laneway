package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/lanewayhq/laneway/internal/gitrepo"
	"github.com/lanewayhq/laneway/internal/plugin"
)

// runGenerate is `lane generate <plugin>`: it runs the Plugin's Generators
// on the current Worktree and merges the file blocks they return into its
// files. Like `init`, it works on the Worktree's files only, so it doesn't
// need the daemon.
func runGenerate(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "generate <plugin>")
	if err := fs.Parse(args); err != nil {
		return parseFlags(fs, args)
	}
	if fs.NArg() != 1 {
		return usageError{"name one Plugin: `lane generate <plugin>`"}
	}
	return generate(ctx, env, fs.Arg(0), "lane generate")
}

// generate runs the Generators of the Plugin name on the current Worktree
// and reports each file it merged into. command names the command for
// errors.
func generate(ctx context.Context, env Env, name, command string) error {
	checkout, err := gitrepo.Resolve(ctx, env.Dir)
	if errors.Is(err, gitrepo.ErrNotGitRepo) {
		return fmt.Errorf("%s is not inside a git repository; run `%s` from a git repository", env.Dir, command)
	}
	if err != nil {
		return err
	}
	plugins, err := openPlugins(env)
	if err != nil {
		return err
	}
	defer plugins.Close()
	res, err := plugins.Generate(name, checkout.Dir)
	if errors.Is(err, plugin.ErrUnknownPlugin) {
		return fmt.Errorf("no Plugin named %s is installed or built in; add it with `lane plugin add <dir|github.com/<owner>/<repo>>`", name)
	}
	for _, f := range res.Files {
		switch {
		case f.Created():
			fmt.Fprintf(env.Stdout, "Created %s\n", f.Path)
		case f.Changed():
			fmt.Fprintf(env.Stdout, "Updated %s\n", f.Path)
		default:
			fmt.Fprintf(env.Stdout, "%s is up to date\n", f.Path)
		}
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(env.Stderr, "warning: %s\n", w)
	}
	return err
}
