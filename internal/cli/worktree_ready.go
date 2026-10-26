package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/client"
)

// Exit codes of `portby worktree ready`. Harness hooks and scripts depend on
// them, so they don't change. Usage errors exit with exitUsage (2).
const (
	// readyFailed: preparing the Worktree Failed. The failed step, its error
	// and its last lines of output are on stderr.
	readyFailed = 1
	// readyNothingToWaitFor: the directory isn't in a Linked Project's git
	// repository, or no daemon is running, so there is nothing to wait for.
	readyNothingToWaitFor = 3
	// readyGaveUp: no answer: --timeout passed, it was interrupted, the
	// Worktree was removed or the daemon stopped while waiting, or another
	// error (on stderr).
	readyGaveUp = 4
)

// defaultReadyTimeout is how long `portby worktree ready` waits by default.
const defaultReadyTimeout = 10 * time.Minute

func runWorktreeReady(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "worktree ready")
	retry := fs.Bool("retry", false, "if preparing the Worktree failed, prepare it again (from the first step) and wait")
	timeout := fs.Duration("timeout", defaultReadyTimeout, "give up after this long (exit 4); 0 waits for ever")
	worktree := worktreeFlag(fs, env)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	c, err := apiClient(env)
	if err != nil {
		return exitCode{readyGaveUp, err.Error()}
	}
	req := api.ReadyRequest{Dir: env.Dir, Worktree: worktree(), Retry: *retry}
	info, err := c.WorktreeReady(ctx, req)
	if err == nil && !setupDone(info) {
		fmt.Fprintf(env.Stderr, "Waiting for %s of %q to be prepared…\n", describeWorktree(info.Worktree), info.Project.Name)
		req.Retry, req.Wait = false, true
		info, err = c.WorktreeReady(ctx, req)
	}
	if err != nil {
		return readyError(ctx, *timeout, err)
	}
	for _, w := range info.Worktree.Setup.Warnings {
		fmt.Fprintf(env.Stderr, "portby worktree: warning: %s\n", w)
	}
	if setup := info.Worktree.Setup; setup.State == api.SetupFailed {
		var b strings.Builder
		fmt.Fprintf(&b, "preparing %s of %q failed at step %q: %s\n",
			describeWorktree(info.Worktree), info.Project.Name, setup.Step, setup.Error)
		for _, line := range setup.Output {
			fmt.Fprintf(&b, "  %s\n", line)
		}
		b.WriteString("Fix the problem, then run `portby worktree ready --retry`.")
		return exitCode{readyFailed, b.String()}
	}
	fmt.Fprintf(env.Stdout, "%s of %q is Ready: %s\n", describeWorktree(info.Worktree), info.Project.Name, info.URL)
	return nil
}

func setupDone(info api.WorktreeInfo) bool {
	s := info.Worktree.Setup
	return s != nil && (s.State == api.SetupReady || s.State == api.SetupFailed)
}

// readyError maps an error from the API to its exit code.
func readyError(ctx context.Context, timeout time.Duration, err error) error {
	var apiErr *api.Error
	switch {
	case errors.Is(err, client.ErrDaemonNotRunning):
		return exitCode{readyNothingToWaitFor, err.Error()}
	case errors.As(err, &apiErr) && (apiErr.Code == api.CodeNotLinked || apiErr.Code == api.CodeNotGitRepo):
		return exitCode{readyNothingToWaitFor, err.Error()}
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return exitCode{readyGaveUp, fmt.Sprintf("the Worktree wasn't Ready after %s; "+
			"see how far it got with `portby status`, or wait longer with --timeout", timeout)}
	default:
		return exitCode{readyGaveUp, err.Error()}
	}
}
