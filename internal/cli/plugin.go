package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/paths"
	"github.com/lanewayhq/laneway/internal/plugin"
)

// pluginCommands are the `lane plugin <name>` subcommands.
var pluginCommands = map[string]func(ctx context.Context, env Env, args []string) error{
	"add":  runPluginAdd,
	"list": runPluginList,
}

const pluginUsage = "use `lane plugin add <dir>` or `lane plugin list`"

func runPlugin(ctx context.Context, env Env, args []string) error {
	if len(args) == 0 {
		return usageError{"missing subcommand; " + pluginUsage}
	}
	run, ok := pluginCommands[args[0]]
	if !ok {
		return usageError{fmt.Sprintf("unknown subcommand %q; %s", args[0], pluginUsage)}
	}
	return run(ctx, env, args[1:])
}

func runPluginAdd(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "plugin add <dir>")
	yes := fs.Bool("yes", false, "add the Plugin without asking to confirm what it may do")
	// The directory may come before or after the flags.
	if err := fs.Parse(args); err != nil {
		return parseFlags(fs, args)
	}
	if fs.NArg() == 0 {
		return usageError{"missing the Plugin's directory, such as `lane plugin add ./my-plugin`"}
	}
	src := fs.Arg(0)
	if err := parseFlags(fs, fs.Args()[1:]); err != nil {
		return err
	}
	if !filepath.IsAbs(src) {
		src = filepath.Join(env.Dir, src)
	}
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	req := api.AddPluginRequest{Source: src}
	if !*yes {
		req.Preview = true
		preview, err := c.AddPlugin(ctx, req)
		if err != nil {
			return err
		}
		printPlugin(env.Stdout, preview)
		if !confirm(env, "Add it? [y/N] ") {
			return errors.New("nothing was added; run `lane plugin add --yes <dir>` to add a Plugin without asking")
		}
		req.Preview = false
	}
	res, err := c.AddPlugin(ctx, req)
	if err != nil {
		return err
	}
	if *yes {
		printPlugin(env.Stdout, res)
	}
	fmt.Fprintf(env.Stdout, "Added Plugin %s %s\n", res.Plugin.Name, res.Plugin.Version)
	return nil
}

// printPlugin shows what a Plugin adds and what it may do, for the developer
// to confirm before it's added.
func printPlugin(w io.Writer, res api.AddPluginResponse) {
	p := res.Plugin
	fmt.Fprintf(w, "Plugin %s %s, from %s\n", p.Name, p.Version, p.Source)
	if res.Replaces != nil {
		fmt.Fprintf(w, "  replaces the installed %s %s, from %s\n", res.Replaces.Name, res.Replaces.Version, res.Replaces.Source)
	}
	for _, r := range p.Recipes {
		fmt.Fprintf(w, "  adds the Recipe %s, for Projects where %s\n", r.Name, r.Detects)
	}
	for _, in := range p.Integrations {
		fmt.Fprintf(w, "  adds the Integration %s, for Projects where %s\n", in.Name, in.Detects)
	}
	if len(p.Effects) == 0 {
		fmt.Fprintln(w, "  may only return config for the Projects it detects")
	}
	for _, e := range p.Effects {
		if e.Kind == "http" && len(p.HTTPHosts) > 0 {
			fmt.Fprintf(w, "  may %s, to %s\n", e.Description, strings.Join(p.HTTPHosts, ", "))
			continue
		}
		fmt.Fprintf(w, "  may %s\n", e.Description)
	}
}

func runPluginList(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "plugin list")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	plugins, err := c.Plugins(ctx)
	if err != nil {
		return err
	}
	if len(plugins) == 0 {
		fmt.Fprintln(env.Stdout, "No Plugins are installed. Add one with `lane plugin add <dir>`.")
		return nil
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PLUGIN\tVERSION\tSOURCE\tADDS")
	for _, p := range plugins {
		var adds []string
		for _, r := range p.Recipes {
			adds = append(adds, "Recipe "+r.Name)
		}
		for _, in := range p.Integrations {
			adds = append(adds, "Integration "+in.Name)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Name, p.Version, p.Source, strings.Join(adds, ", "))
	}
	return tw.Flush()
}

// openPlugins opens the installed Plugins, for commands that resolve a
// Project's config without the daemon. The caller closes the Host.
func openPlugins(env Env) (*plugin.Host, error) {
	p, err := paths.FromEnv(env.Getenv)
	if err != nil {
		return nil, err
	}
	return plugin.Open(plugin.Options{Dir: p.Plugins(), CacheDir: p.PluginCache()})
}
