package cli

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"text/tabwriter"

	"github.com/lanewayhq/laneway/internal/api"
)

// runServices is `lane services [ls|up|down] [name]`.
func runServices(ctx context.Context, env Env, args []string) error {
	action := "ls"
	if len(args) > 0 {
		action, args = args[0], args[1:]
	}
	fs := newFlags(env, "services "+action)
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
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	switch action {
	case "ls":
		if name != "" {
			return usageError{fmt.Sprintf("unexpected argument %q", name)}
		}
		services, err := c.Services(ctx)
		if err != nil {
			return err
		}
		if len(services) == 0 {
			fmt.Fprintln(env.Stdout, "No Services yet. A Project declares the ones it uses in the [services] table of laneway.toml, such as redis = \"7\".")
			return nil
		}
		return printServices(env, services)
	case "up", "down":
		services, err := c.ControlServices(ctx, action, api.ServiceRequest{Name: name})
		for _, s := range services {
			fmt.Fprintf(env.Stdout, "%s is %s at %s\n", s.Name, s.State, hostPort(s))
		}
		return err
	default:
		return usageError{fmt.Sprintf("unknown subcommand %q; use `lane services`, `lane services up [name]` or `lane services down [name]`", action)}
	}
}

func printServices(env Env, services []api.Service) error {
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ENGINE\tVERSION\tHOST\tPORT\tSTATE")
	for _, s := range services {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", s.Engine, s.Version, s.Host, s.Port, s.State)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, s := range services {
		if s.Error != "" {
			fmt.Fprintf(env.Stdout, "%s: %s\n", s.Name, s.Error)
		}
	}
	return nil
}

func hostPort(s api.Service) string {
	return net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
}
