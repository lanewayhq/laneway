package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lanewayhq/laneway/internal/dotenv"

	"github.com/lanewayhq/laneway/internal/runtimes"
)

// exitNotFound is the shell's exit code for a command that wasn't found.
const exitNotFound = 127

// runExec runs a command in the current Worktree's directory with the
// Runtimes resolved for it, the way its Processes get them: through mise,
// else from PATH. Input and output are the CLI's own, and the exit code is
// the command's.
func runExec(ctx context.Context, env Env, args []string) error {
	// Only --worktree may come before the command (or the -- before it):
	// everything after belongs to the command.
	fs := newFlags(env, "exec [--worktree <name>] [--] <command> [args...]")
	worktree := worktreeFlag(fs, env)
	if len(args) > 0 && (args[0] == "--worktree" || strings.HasPrefix(args[0], "--worktree=")) {
		n := 2
		if strings.Contains(args[0], "=") {
			n = 1
		}
		if err := parseFlags(fs, args[:min(n, len(args))]); err != nil {
			return err
		}
		args = args[min(n, len(args)):]
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		usage := "Usage: lane exec [--worktree <name>] [--] <command> [args...]"
		if len(args) == 0 {
			return usageError{"missing command; " + usage}
		}
		fmt.Fprintln(env.Stdout, usage)
		return nil
	}
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	info, err := c.Worktree(ctx, env.Dir, worktree())
	if err != nil {
		return err
	}
	dir := info.Worktree.Path
	vars := environOf(env)
	if info.ExportEnv {
		// The stack doesn't read .env itself; its Processes load it, and
		// so does what it runs here.
		vars = append(vars, dotenv.Environ(filepath.Join(dir, ".env"))...)
	}
	rt := runtimes.Environ(ctx, dir, vars)
	for _, w := range rt.Warnings {
		fmt.Fprintf(env.Stderr, "lane exec: warning: %s\n", w)
	}
	path, err := rt.LookPath(args[0])
	if err != nil {
		return exitCode{exitNotFound, runtimes.NotFound(dir, args[0]).Error()}
	}
	// Not CommandContext: Ctrl-C reaches the command from the terminal, and
	// the command decides when it is done (an interactive `php artisan
	// tinker` handles it itself).
	cmd := exec.Command(path, args[1:]...)
	cmd.Args[0] = args[0]
	cmd.Dir = dir
	cmd.Env = rt.Vars
	cmd.Stdin, cmd.Stdout, cmd.Stderr = env.Stdin, env.Stdout, env.Stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			code = 128 + int(ws.Signal())
		}
		return exitCode{code: code}
	}
	return err
}
