package cli

import (
	"context"
	"fmt"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/client"
)

func runLogs(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "logs [process]")
	follow := fs.Bool("f", false, "keep printing new output as it is written")
	worktree := worktreeFlag(fs, env)
	// The Process name may come before or after the flags.
	if err := fs.Parse(args); err != nil {
		return parseFlags(fs, args)
	}
	var process string
	if fs.NArg() > 0 {
		process = fs.Arg(0)
		if err := parseFlags(fs, fs.Args()[1:]); err != nil {
			return err
		}
	}
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	req := client.LogsRequest{Dir: env.Dir, Worktree: worktree(), Process: process, Follow: *follow}
	return c.Logs(ctx, req, func(l api.LogLine) error {
		if process != "" {
			_, err := fmt.Fprintln(env.Stdout, l.Text)
			return err
		}
		_, err := fmt.Fprintf(env.Stdout, "%s | %s\n", l.Process, l.Text)
		return err
	})
}
