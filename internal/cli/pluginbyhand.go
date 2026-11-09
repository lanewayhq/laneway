package cli

import (
	"context"
	"fmt"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/client"
)

// updateByHand installs what the sources of the Plugins names, added by hand
// (from a directory or a GitHub repository), have now, such as a
// repository's latest release. A Plugin added by hand is pinned to the hash
// it had when added, and nothing vouches for a new one but the developer,
// so each update shows the new version, hash and what it may do, and needs
// confirming unless yes.
func updateByHand(ctx context.Context, env Env, c *client.Client, names []string, yes bool) error {
	var declined []string
	for _, name := range names {
		preview, err := c.UpdateHandAdded(ctx, name, api.UpdateHandAddedRequest{Preview: true})
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
		fmt.Fprintf(env.Stdout, "  SHA-256 %s → %s\n", orNone(preview.Was.SHA256), orNone(preview.Plugin.SHA256))
		if !yes && !confirm(env, "Update it? [y/N] ") {
			declined = append(declined, name)
			continue
		}
		res, err := c.UpdateHandAdded(ctx, name, api.UpdateHandAddedRequest{Digest: preview.Digest})
		if err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "Updated Plugin %s %s → %s, pinned to SHA-256 %s\n", name, res.Was.Version, res.Plugin.Version, orNone(res.Plugin.SHA256))
	}
	switch len(declined) {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("Plugin %s wasn't updated; run `lane plugin update --yes %s` to update it without asking", declined[0], declined[0])
	}
	return fmt.Errorf("Plugins %s weren't updated; run `lane plugin update --yes` to update without asking", andList(declined))
}

// orNone is sum, a hash, or "(none)" for a Plugin without a module.
func orNone(sum string) string {
	if sum == "" {
		return "(none)"
	}
	return sum
}
