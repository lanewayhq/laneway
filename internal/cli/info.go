package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/client"
)

// infoNotServed is `lane info`'s exit code when the directory isn't in a
// Linked Project's git repository or no daemon is running, so Laneway has
// nothing to say about it (the same as `lane worktree ready`'s 3). Agents
// and scripts depend on it, so it doesn't change. Other errors exit 1, usage
// errors 2.
const infoNotServed = 3

// runInfo is `lane info [--json]`: everything about the current Worktree.
// With --json it prints api.Info, the stable contract agents and scripts
// read; without it, the same for people.
func runInfo(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "info")
	asJSON := fs.Bool("json", false, "print the details as JSON (a stable format for scripts and agents)")
	worktree := worktreeFlag(fs, env)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	info, err := c.Info(ctx, env.Dir, worktree())
	var apiErr *api.Error
	switch {
	case errors.Is(err, client.ErrDaemonNotRunning),
		errors.As(err, &apiErr) && (apiErr.Code == api.CodeNotLinked || apiErr.Code == api.CodeNotGitRepo):
		return exitCode{infoNotServed, err.Error()}
	case err != nil:
		return err
	}
	if *asJSON {
		return printInfoJSON(env, info)
	}
	return printInfo(env, info)
}

// printInfo prints info for people: everything --json has, as text.
func printInfo(env Env, info api.Info) error {
	worktree := fmt.Sprintf("Worktree %q", info.Worktree.Name)
	if info.Worktree.Main {
		worktree = "the main Worktree"
	}
	fmt.Fprintf(env.Stdout, "%s of %q\n", worktree, info.Project.Name)
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  Path\t%s\n", info.Worktree.Path)
	fmt.Fprintf(tw, "  URL\t%s\n", cmp.Or(info.URL, "- (Laneway hasn't discovered this Worktree yet)"))
	if info.Domain != "" && len(info.Aliases) > 0 {
		// The URL is the newest Alias's; the Domain still identifies the
		// Worktree.
		fmt.Fprintf(tw, "  Domain\t%s\n", info.Domain)
	}
	if len(info.Aliases) > 0 {
		var urls []string
		for _, a := range info.Aliases {
			urls = append(urls, a.URL)
		}
		fmt.Fprintf(tw, "  Aliases\t%s\n", strings.Join(urls, " "))
	}
	if s := info.Setup; s != nil {
		state := s.State
		switch {
		case s.State == api.SetupFailed:
			state = fmt.Sprintf("preparing it failed at step %q: %s (run `lane worktree ready --retry` once it is fixed)", s.Step, s.Error)
		case s.Step != "":
			state += ": " + s.Step
		}
		fmt.Fprintf(tw, "  Readiness\t%s\n", state)
		for _, w := range s.Warnings {
			// A warning's later lines line up under its first.
			fmt.Fprintf(tw, "  \twarning: %s\n", strings.ReplaceAll(w, "\n", "\n  \t"))
		}
	}
	fmt.Fprintf(tw, "  State\t%s\n", cmp.Or(info.State, "-"))
	detected := cmp.Or(info.Recipe, "-")
	if info.RecipeRule != "" {
		detected += ", because " + info.RecipeRule
	}
	fmt.Fprintf(tw, "  Recipe\t%s\n", detected)
	for i, in := range info.IntegrationRules {
		label := ""
		if i == 0 {
			label = "Integrations"
		}
		fmt.Fprintf(tw, "  %s\t%s, because %s\n", label, in.Name, in.Rule)
	}
	if info.ConfigError != "" {
		fmt.Fprintf(tw, "  Config\t%s\n", info.ConfigError)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if len(info.Processes) > 0 {
		fmt.Fprintln(env.Stdout, "\nProcesses")
		tw = tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  NAME\tSTART MODE\tSTATE\tURL")
		for _, p := range info.Processes {
			url := cmp.Or(p.URL, "-")
			if p.Primary {
				url += "  (primary)"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", p.Name, cmp.Or(p.StartMode, "-"), p.State, url)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	if db := info.Database; db != nil {
		fmt.Fprintln(env.Stdout)
		tw = tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
		where := ""
		if db.Service != "" {
			where = " on " + db.Service
		}
		fmt.Fprintf(tw, "Database\t%s %s%s\n", db.Engine, db.Name, where)
		if db.Test != "" {
			fmt.Fprintf(tw, "Test Database\t%s\n", db.Test)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	if len(info.Buckets) > 0 {
		fmt.Fprintln(env.Stdout)
		tw = tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
		for i, b := range info.Buckets {
			label, access := "", ""
			if i == 0 {
				label = "Buckets"
			}
			if b.Public {
				access = " (public)"
			}
			fmt.Fprintf(tw, "%s\t%s on %s%s\n", label, b.Name, b.Service, access)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	if len(info.Services) > 0 {
		fmt.Fprintln(env.Stdout, "\nServices")
		tw = tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  NAME\tADDRESS\tUSER\tPASSWORD\tSTATE")
		for _, s := range info.Services {
			addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", s.Name, addr, cmp.Or(s.User, "-"), s.Password, s.State)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		for _, s := range info.Services {
			if s.Error != "" {
				fmt.Fprintf(env.Stdout, "  %s: %s\n", s.Name, s.Error)
			}
		}
	}
	return nil
}

func printInfoJSON(env Env, info api.Info) error {
	enc := json.NewEncoder(env.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(info); err != nil {
		return fmt.Errorf("printing the Worktree's details: %w", err)
	}
	return nil
}
