package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/client"
)

// runPluginUpdate is `lane plugin update [name] [--yes]`: it updates the
// installed Catalog Plugins (or only name) to the latest release the
// Catalog lists that every version pin allows. An update that asks for
// capabilities the installed version doesn't have needs confirming first.
func runPluginUpdate(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "plugin update [name]")
	yes := fs.Bool("yes", false, "update without asking to confirm new capabilities")
	// The name may come before or after the flags.
	if err := fs.Parse(args); err != nil {
		return parseFlags(fs, args)
	}
	var name string
	if fs.NArg() > 0 {
		name = fs.Arg(0)
		if err := parseFlags(fs, fs.Args()[1:]); err != nil {
			return err
		}
	}
	if fs.NArg() > 0 {
		return usageError{"name at most one Plugin: `lane plugin update [name]`"}
	}
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	preview, err := c.UpdatePlugins(ctx, api.UpdatePluginsRequest{Name: name, Preview: true})
	if err != nil {
		return err
	}
	printUpdateWarnings(env, preview.Warnings)
	if len(preview.Updates) == 0 {
		if name != "" {
			fmt.Fprintf(env.Stdout, "Plugin %s is up to date.\n", name)
		} else {
			fmt.Fprintln(env.Stdout, "Every Plugin from the Catalog is up to date.")
		}
		return nil
	}
	if needsConfirming(preview.Updates) && !*yes {
		printUpdates(env.Stdout, preview.Updates)
		if !confirm(env, "Update? [y/N] ") {
			return errors.New("nothing was updated; run `lane plugin update --yes` to update without asking")
		}
	}
	res, err := c.UpdatePlugins(ctx, api.UpdatePluginsRequest{Name: name, Confirmed: preview.Updates})
	if err != nil {
		return err
	}
	printUpdateWarnings(env, res.Warnings)
	for _, u := range res.Updates {
		fmt.Fprintf(env.Stdout, "Updated Plugin %s %s → %s\n", u.Name, u.From, u.To)
	}
	printOutOfDate(env.Stdout, res.OutOfDate)
	return nil
}

// printOutOfDate tells the developer which generated files an update left
// out of date, and how to update them, one line per Worktree and Plugin.
func printOutOfDate(w io.Writer, files []api.OutOfDateFile) {
	if len(files) == 0 {
		return
	}
	fmt.Fprintln(w, "Generated files are out of date; Laneway doesn't change them itself.")
	type key struct{ worktree, plugin string }
	var order []key
	paths := map[key][]string{}
	for _, f := range files {
		k := key{f.Worktree, f.Plugin}
		if _, ok := paths[k]; !ok {
			order = append(order, k)
		}
		paths[k] = append(paths[k], f.Path)
	}
	for _, k := range order {
		fmt.Fprintf(w, "  In %s, run `lane generate %s` to update %s\n", k.worktree, k.plugin, andList(paths[k]))
	}
}

// needsConfirming reports whether any update asks for new capabilities.
func needsConfirming(updates []api.PluginUpdate) bool {
	for _, u := range updates {
		if len(u.NewEffects) > 0 || len(u.NewHTTPHosts) > 0 {
			return true
		}
	}
	return false
}

// printUpdates shows each update and the capabilities it adds, for the
// developer to confirm.
func printUpdates(w io.Writer, updates []api.PluginUpdate) {
	for _, u := range updates {
		fmt.Fprintf(w, "Plugin %s %s → %s\n", u.Name, u.From, u.To)
		for _, e := range u.NewEffects {
			if e.Kind == "http" && len(u.NewHTTPHosts) > 0 {
				continue // shown with its hosts below
			}
			fmt.Fprintf(w, "  may now %s\n", e.Description)
		}
		if len(u.NewHTTPHosts) > 0 {
			fmt.Fprintf(w, "  may now send HTTP requests to %s\n", strings.Join(u.NewHTTPHosts, ", "))
		}
	}
}

// printUpdateWarnings shows the daemon's warnings on stderr.
func printUpdateWarnings(env Env, warnings []string) {
	for _, w := range warnings {
		fmt.Fprintf(env.Stderr, "warning: %s\n", w)
	}
}

// printPluginUpdates shows the installed Plugins' available updates, for
// `lane status`. Status doesn't fail when they can't be listed.
func printPluginUpdates(ctx context.Context, env Env, c *client.Client) {
	plugins, err := c.Plugins(ctx)
	if err != nil {
		return
	}
	var updates []api.PluginUpdate
	for _, p := range plugins {
		if p.Update != nil {
			updates = append(updates, *p.Update)
		}
	}
	if len(updates) == 0 {
		return
	}
	fmt.Fprintln(env.Stdout)
	fmt.Fprintln(env.Stdout, "Plugin updates are available; run `lane plugin update`:")
	for _, u := range updates {
		fmt.Fprintf(env.Stdout, "  %s %s → %s\n", u.Name, u.From, u.To)
	}
}
