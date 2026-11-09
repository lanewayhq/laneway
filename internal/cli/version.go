package cli

import (
	"context"
	"fmt"
	"runtime/debug"
)

// version is this build's version. Release builds stamp it in with
// -ldflags "-X github.com/lanewayhq/laneway/internal/cli.version=v1.2.3";
// `go install …@v1.2.3` builds get it from the module's version instead.
var version = ""

// Version is the version `lane version` reports: the stamped-in one, else
// the module version Go recorded, else "dev" for a build from a checkout.
func Version() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func runVersion(_ context.Context, env Env, args []string) error {
	fs := newFlags(env, "version")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "lane %s\n", Version())
	return nil
}
