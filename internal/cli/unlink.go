package cli

import (
	"context"
	"fmt"
)

// runUnlink is `portby unlink`: Unlink the Project the current directory is
// in. Its databases become Orphans, so it can be undone by linking again
// within the grace period.
func runUnlink(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "unlink")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	res, err := c.Unlink(ctx, env.Dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Unlinked Project %q (%s): its Processes are stopped and its Domains are gone. Env files were left as they are.\n",
		res.Project.Name, res.Project.Path)
	if len(res.Orphans) == 0 {
		return nil
	}
	fmt.Fprintln(env.Stdout, "Its databases are Orphans now and will be dropped in 24 hours. Link the Project again before then to keep them, or run `portby gc` to drop them now:")
	printOrphans(env.Stdout, res.Orphans)
	return nil
}
