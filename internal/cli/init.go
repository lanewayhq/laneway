package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mrsafalpiya/portby.dev/internal/config"
	"github.com/mrsafalpiya/portby.dev/internal/gitrepo"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
)

// runInit writes the resolved config to portby.toml at the root of the
// current Worktree. It works on files only, so it doesn't need the daemon.
// portby.local.toml is left out: personal overrides don't belong in the
// committed file.
func runInit(ctx context.Context, env Env, args []string) error {
	flags := newFlags(env, "init")
	force := flags.Bool("force", false, "overwrite an existing "+config.FileName)
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	checkout, err := gitrepo.Resolve(ctx, env.Dir)
	if errors.Is(err, gitrepo.ErrNotGitRepo) {
		return fmt.Errorf("%s is not inside a git repository; run `portby init` from a git repository", env.Dir)
	}
	if err != nil {
		return err
	}
	path := filepath.Join(checkout.Dir, config.FileName)

	in := recipe.Input{Dir: checkout.Dir, Recipes: recipe.Builtin}
	committed, err := config.LoadLayer(path)
	switch {
	case err == nil && !*force:
		return fmt.Errorf("%s already exists; run `portby init --force` to replace it with the resolved config", path)
	case err == nil:
		in.Layers = append(in.Layers, committed)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	res, err := recipe.Resolve(in)
	if err != nil {
		return err
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(env.Stderr, "warning: %s (left out of the new file)\n", w)
	}

	var buf bytes.Buffer
	writeInitHeader(&buf, res)
	if err := config.Encode(&buf, res.Layer()); err != nil {
		return err
	}
	if err := writeFile(path, buf.Bytes(), *force); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Wrote %s\n", path)
	return nil
}

func writeInitHeader(buf *bytes.Buffer, res recipe.Resolved) {
	fmt.Fprintf(buf, "# Portby config for this Project, written by `portby init`.\n")
	if res.Recipe != "" {
		fmt.Fprintf(buf, "# These are the %s Recipe's defaults plus your config. Edit them freely.\n", res.Recipe)
	}
	fmt.Fprintf(buf, "# For personal settings, use %s and keep it out of git.\n", config.LocalFileName)
	if len(res.Processes) == 0 {
		buf.WriteString("#\n" +
			"# No Processes yet. Declare the one that serves the Project's Domain, for example:\n" +
			"#\n" +
			"# [processes.web]\n" +
			"# command = \"php -S 127.0.0.1:{port} -t public\"\n" +
			"# primary = true\n")
	}
	buf.WriteString("\n")
}

// writeFile writes data to path. Unless overwrite is set it fails if path
// already exists, even if it appeared since it was checked.
func writeFile(path string, data []byte, overwrite bool) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if overwrite {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
