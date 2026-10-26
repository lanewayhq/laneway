// Package cli implements the `portby` command line. Every command except
// `daemon` is a thin client of the daemon's API: it gathers input from the
// working directory and flags, calls the API through package client, and
// prints the result.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"

	"github.com/mrsafalpiya/portby.dev/internal/client"
	"github.com/mrsafalpiya/portby.dev/internal/paths"
	"github.com/mrsafalpiya/portby.dev/internal/platform"
)

// Env is everything a CLI invocation reads from or writes to the outside
// world, so tests can run it in-process.
type Env struct {
	// Args are the arguments after the program name.
	Args []string
	// Dir is the working directory commands act on.
	Dir    string
	Getenv func(string) string
	Stdout io.Writer
	Stderr io.Writer
	// Platform does everything OS-specific (install, doctor, open). If nil,
	// the real one for this machine is detected.
	Platform platform.Platform
}

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// command is one `portby <name>` subcommand.
type command struct {
	summary string
	run     func(ctx context.Context, env Env, args []string) error
}

var commands = map[string]command{
	"daemon":  {"Run the Portby daemon in the foreground", runDaemon},
	"doctor":  {"Check that Portby is set up and working", runDoctor},
	"down":    {"Stop the current Worktree's Processes (or one Process)", control("down")},
	"init":    {"Write the resolved config to portby.toml", runInit},
	"install": {"Set up Portby on this machine (service, ports 80/443, CA trust)", runInstall},
	"link":    {"Link the current git repository as a Project", runLink},
	"logs":    {"Show (or follow with -f) Process output", runLogs},
	"open":    {"Open the current Worktree's Domain in the browser", runOpen},
	"restart": {"Restart the current Worktree's Processes (or one Process)", control("restart")},
	"status":  {"List Linked Projects and their Processes", runStatus},
	"up":      {"Start the current Worktree's Processes (or one Process)", control("up")},
}

// usageError makes Run print the error and exit with exitUsage.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// Run executes one CLI invocation and returns its exit code.
func Run(ctx context.Context, env Env) int {
	if len(env.Args) == 0 || env.Args[0] == "help" || env.Args[0] == "-h" || env.Args[0] == "--help" {
		printUsage(env.Stdout)
		return exitOK
	}
	cmd, ok := commands[env.Args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "portby: unknown command %q\n\n", env.Args[0])
		printUsage(env.Stderr)
		return exitUsage
	}
	err := cmd.run(ctx, env, env.Args[1:])
	var uerr usageError
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, flag.ErrHelp):
		return exitOK
	case errors.As(err, &uerr):
		fmt.Fprintf(env.Stderr, "portby %s: %s\n", env.Args[0], uerr.msg)
		return exitUsage
	default:
		fmt.Fprintf(env.Stderr, "portby %s: %s\n", env.Args[0], err)
		return exitError
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: portby <command> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "  %-8s %s\n", name, commands[name].summary)
	}
}

// newFlags returns a FlagSet for a subcommand that reports errors through
// Run rather than printing them itself.
func newFlags(env Env, name string) *flag.FlagSet {
	fs := flag.NewFlagSet("portby "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {
		fmt.Fprintf(env.Stdout, "Usage: portby %s [flags]\n", name)
		fs.SetOutput(env.Stdout)
		fs.PrintDefaults()
		fs.SetOutput(io.Discard)
	}
	return fs
}

// parseFlags parses args and rejects positional arguments. With -h it prints
// the command's usage.
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{err.Error()}
	}
	if fs.NArg() > 0 {
		return usageError{fmt.Sprintf("unexpected argument %q", fs.Arg(0))}
	}
	return nil
}

// apiClient returns a client for the daemon serving this user's socket.
func apiClient(env Env) (*client.Client, error) {
	p, err := paths.FromEnv(env.Getenv)
	if err != nil {
		return nil, err
	}
	return client.New(p.Socket), nil
}

// platformOf returns the Platform commands act on.
func platformOf(env Env) platform.Platform {
	if env.Platform != nil {
		return env.Platform
	}
	return platform.Detect(env.Getenv)
}
