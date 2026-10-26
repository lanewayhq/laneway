package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/dotenv"
)

func runLink(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "link")
	name := fs.String("name", "", "Project name, instead of `name` in portby.toml or the directory name")
	yes := fs.Bool("yes", false, "apply the Env Patch to the Worktrees' .env files without asking")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	req := api.LinkRequest{Dir: env.Dir, Name: *name}
	if !*yes {
		req.Preview = true
		preview, err := c.Link(ctx, req)
		if err != nil {
			return err
		}
		if len(preview.EnvPatch) > 0 {
			printEnvPatch(env.Stdout, preview.EnvPatch)
			if !confirm(env, "Apply these changes? [y/N] ") {
				return errors.New("nothing was changed; run `portby link --yes` to apply the Env Patch without asking")
			}
		}
		req.Preview = false
	}
	res, err := c.Link(ctx, req)
	if err != nil {
		return err
	}
	if *yes {
		printEnvPatch(env.Stdout, res.EnvPatch)
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

// printEnvPatch shows how each env file changes, as a diff of the keys
// Portby owns.
func printEnvPatch(w io.Writer, patches []api.EnvFilePatch) {
	for _, p := range patches {
		worktree := "the main Worktree"
		if !p.Worktree.Main {
			worktree = fmt.Sprintf("Worktree %q", p.Worktree.Name)
		}
		fmt.Fprintf(w, "Env Patch for %s: %s\n", worktree, p.Path)
		switch {
		case p.Create && p.CopyFrom != "":
			fmt.Fprintf(w, "  (new file, copied from %s)\n", p.CopyFrom)
		case p.Create:
			fmt.Fprintln(w, "  (new file)")
		case p.Backup != "":
			fmt.Fprintf(w, "  (the original is kept as %s)\n", filepath.Base(p.Backup))
		}
		for _, c := range p.Changes {
			if !c.Added {
				fmt.Fprintf(w, "  - %s=%s\n", c.Key, dotenv.Quote(c.Old))
			}
			fmt.Fprintf(w, "  + %s=%s\n", c.Key, dotenv.Quote(c.New))
		}
	}
}

// confirm asks a yes/no question on stdout and reads the answer from stdin.
// Anything but y or yes, including no input at all, is no.
func confirm(env Env, question string) bool {
	fmt.Fprint(env.Stdout, question)
	if env.Stdin == nil {
		fmt.Fprintln(env.Stdout)
		return false
	}
	answer, err := bufio.NewReader(env.Stdin).ReadString('\n')
	if err != nil && answer == "" {
		fmt.Fprintln(env.Stdout)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	}
	return false
}
