package cli

import (
	"context"
	"fmt"
)

// runPluginRemove is `lane plugin remove <name>`: the daemon refuses while a
// Linked Project uses the Plugin.
func runPluginRemove(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "plugin remove <name>")
	if err := fs.Parse(args); err != nil {
		return parseFlags(fs, args)
	}
	if fs.NArg() == 0 {
		return usageError{"missing the Plugin's name, such as `lane plugin remove my-plugin`; `lane plugin list` lists them"}
	}
	name := fs.Arg(0)
	if err := parseFlags(fs, fs.Args()[1:]); err != nil {
		return err
	}
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	res, err := c.RemovePlugin(ctx, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Removed Plugin %s %s\n", res.Plugin.Name, res.Plugin.Version)
	for _, s := range res.Kept {
		fmt.Fprintf(env.Stdout, "Kept the data of the Service %s, in the volume %s: adding the Plugin again brings it back\n", s.Name, s.Volume)
	}
	return nil
}
