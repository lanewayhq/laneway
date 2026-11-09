package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/plugin"
)

// runPluginUpdate installs what the source of a Plugin added by hand has
// now, such as its GitHub repository's latest release, after showing the
// new version and hash and what it may do. Without a name, it does so for
// every Plugin added by hand.
//
// A Plugin added by hand is pinned to the hash it had when added: nothing
// else picks up a change to its source.
func runPluginUpdate(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "plugin update [name...]")
	yes := fs.Bool("yes", false, "update without asking to confirm the new version")
	// The names may come before or after the flags.
	if err := fs.Parse(args); err != nil {
		return parseFlags(fs, args)
	}
	var names []string
	rest := fs.Args()
	for len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		names, rest = append(names, rest[0]), rest[1:]
	}
	if err := parseFlags(fs, rest); err != nil {
		return err
	}
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	if names == nil {
		plugins, err := c.Plugins(ctx)
		if err != nil {
			return err
		}
		for _, p := range plugins {
			if p.Source != plugin.CatalogSource {
				names = append(names, p.Name)
			}
		}
		if len(names) == 0 {
			fmt.Fprintln(env.Stdout, "No Plugins added by hand are installed.")
			return nil
		}
	}
	var declined []string
	for _, name := range names {
		preview, err := c.UpdatePlugin(ctx, name, api.UpdatePluginRequest{Preview: true})
		if err != nil {
			return err
		}
		if preview.UpToDate {
			fmt.Fprintf(env.Stdout, "Plugin %s is up to date: %s, SHA-256 %s\n", name, preview.Plugin.Version, orNone(preview.Plugin.SHA256))
			continue
		}
		if err := checkPluginName(preview.Plugin); err != nil {
			return err
		}
		printPlugin(env.Stdout, api.AddPluginResponse{Plugin: preview.Plugin, Replaces: &preview.Was})
		fmt.Fprintf(env.Stdout, "  SHA-256 %s becomes %s\n", orNone(preview.Was.SHA256), orNone(preview.Plugin.SHA256))
		if !*yes && !confirm(env, "Update it? [y/N] ") {
			declined = append(declined, name)
			continue
		}
		res, err := c.UpdatePlugin(ctx, name, api.UpdatePluginRequest{})
		if err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "Updated Plugin %s to %s, pinned to SHA-256 %s\n", name, res.Plugin.Version, orNone(res.Plugin.SHA256))
	}
	if len(declined) > 0 {
		return fmt.Errorf("%s wasn't updated; run `lane plugin update --yes %s` to update without asking",
			andList(declined), strings.Join(declined, " "))
	}
	return nil
}

// orNone is sum, a hash, or "(none)" for a Plugin without a module.
func orNone(sum string) string {
	if sum == "" {
		return "(none)"
	}
	return sum
}
