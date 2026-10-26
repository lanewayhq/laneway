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
	"os"
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
	// Environ returns the whole environment, for commands Portby runs
	// (`portby exec`) and Runtime resolution. If nil, os.Environ.
	Environ func() []string
	// Stdin is where commands read answers to questions, and what `portby
	// exec` passes to its command; nil reads as empty, which answers no.
	Stdin  io.Reader
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
	"daemon":   {"Run the Portby daemon in the foreground", runDaemon},
	"doctor":   {"Check that Portby is set up and working", runDoctor},
	"exec":     {"Run a command in the current Worktree with its Runtimes", runExec},
	"init":     {"Write the resolved config to portby.toml", runInit},
	"install":  {"Set up Portby on this machine (service, ports 80/443, CA trust)", runInstall},
	"link":     {"Link the current git repository as a Project", runLink},
	"logs":     {"Show (or follow with -f) Process output", runLogs},
	"open":     {"Open the current Worktree's Domain in the browser", runOpen},
	"status":   {"List Linked Projects and their Processes", runStatus},
	"worktree": {"Manage the current Worktree (alias <name> [--remove])", runWorktree},
}

// usageError makes Run print the error and exit with exitUsage.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// exitCode makes Run exit with code, printing msg first unless it is empty
// (a command `portby exec` ran has already said why it failed).
type exitCode struct {
	code int
	msg  string
}

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d: %s", e.code, e.msg) }

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
	var xerr exitCode
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &xerr):
		if xerr.msg != "" {
			fmt.Fprintf(env.Stderr, "portby %s: %s\n", env.Args[0], xerr.msg)
		}
		return xerr.code
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

// environOf returns the environment commands run with.
func environOf(env Env) []string {
	if env.Environ != nil {
		return env.Environ()
	}
	return os.Environ()
}

// platformOf returns the Platform commands act on.
func platformOf(env Env) platform.Platform {
	if env.Platform != nil {
		return env.Platform
	}
	return platform.Detect(env.Getenv)
}
