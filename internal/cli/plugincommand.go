package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/plugin"
)

// installedPlugin is the installed Plugin called name, read from the
// Plugins' directory rather than asked of the daemon, so `lane <typo>` says
// it's unknown even when the daemon isn't running.
func installedPlugin(env Env, name string) (plugin.Plugin, bool) {
	h, err := openPlugins(env)
	if err != nil {
		return plugin.Plugin{}, false
	}
	defer h.Close()
	p, ok := h.Plugin(name)
	return p, ok && len(p.Commands) > 0
}

// pluginCommand runs `lane <plugin> [--worktree <name>] <command> [args...]`:
// the daemon runs the Plugin's command in the Worktree and applies the
// effects it returns, and this prints what they did.
func pluginCommand(p plugin.Plugin) func(ctx context.Context, env Env, args []string) error {
	return func(ctx context.Context, env Env, args []string) error {
		fs := newFlags(env, p.Name)
		worktree := worktreeFlag(fs, env)
		// Only --worktree may come before the command: everything after
		// is the command's.
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
		if len(args) == 0 {
			printPluginUsage(env.Stderr, p)
			return exitCode{code: exitUsage}
		}
		if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
			printPluginUsage(env.Stdout, p)
			return nil
		}
		if !hasCommand(p, args[0]) {
			return usageError{fmt.Sprintf("Plugin %s has no command %q; `lane %s --help` lists its commands", p.Name, args[0], p.Name)}
		}
		c, err := apiClient(env)
		if err != nil {
			return err
		}
		res, err := c.RunPluginCommand(ctx, p.Name, args[0], api.PluginCommandRequest{Dir: env.Dir, Worktree: worktree(), Args: args[1:]})
		if err != nil {
			return err
		}
		for _, e := range res.Effects {
			io.WriteString(env.Stdout, e.Output)
		}
		for _, w := range res.Warnings {
			fmt.Fprintf(env.Stderr, "lane %s: warning: %s\n", p.Name, w)
		}
		if res.Error != "" {
			return fmt.Errorf("%s: %s", args[0], res.Error)
		}
		return nil
	}
}

func hasCommand(p plugin.Plugin, name string) bool {
	for _, c := range p.Commands {
		if c.Name == name {
			return true
		}
	}
	return false
}

// printPluginUsage lists the Plugin's commands, for `lane <plugin> --help`.
func printPluginUsage(w io.Writer, p plugin.Plugin) {
	fmt.Fprintf(w, "Usage: lane %s [--worktree <name>] <command> [args...]\n\n", p.Name)
	fmt.Fprintf(w, "Commands from Plugin %s %s:\n", p.Name, p.Version)
	width := 0
	for _, c := range p.Commands {
		width = max(width, len(c.Name))
	}
	for _, c := range p.Commands {
		fmt.Fprintf(w, "  %-*s  %s\n", width, c.Name, c.Summary)
	}
}
