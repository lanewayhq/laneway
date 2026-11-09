package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// harnesses are the Harnesses `lane harness add` knows, each the name of
// the built-in Plugin whose Generator sets it up.
var harnesses = []string{"claude", "t3"}

// runHarness is `lane harness add <harness>`, a shortcut for `lane generate
// <harness>` kept for the Harness Generators built into Laneway.
func runHarness(ctx context.Context, env Env, args []string) error {
	names := strings.Join(harnesses, "|")
	if len(args) == 0 || args[0] != "add" {
		return usageError{fmt.Sprintf("use `lane harness add <%s>`", names)}
	}
	fs := newFlags(env, "harness add <"+names+">")
	if err := fs.Parse(args[1:]); err != nil {
		return parseFlags(fs, args[1:])
	}
	if fs.NArg() != 1 {
		return usageError{fmt.Sprintf("name one Harness: `lane harness add <%s>`", names)}
	}
	name := fs.Arg(0)
	if !slices.Contains(harnesses, name) {
		return usageError{fmt.Sprintf("unknown Harness %q; use one of: %s", name, strings.Join(harnesses, ", "))}
	}
	return generate(ctx, env, name, "lane harness add")
}
