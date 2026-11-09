// Command tasks is a test Plugin that adds commands, run as `lane tasks
// <command>`, each returning effects that show what Laneway does with them.
package main

import (
	"errors"
	"strings"

	"github.com/lanewayhq/plugins/sdk"
)

func main() {
	sdk.Run(sdk.Plugin{Commands: map[string]sdk.Command{
		// greet runs echo with the command's arguments.
		"greet": func(_ sdk.Project, args []string) ([]sdk.Effect, error) {
			return []sdk.Effect{sdk.Exec(append([]string{"echo", "hello"}, args...)...)}, nil
		},
		// marker reads the Worktree's marker file, which shows which
		// Worktree the module was given.
		"marker": func(p sdk.Project, _ []string) ([]sdk.Effect, error) {
			data, err := p.ReadFile("marker")
			if err != nil {
				return nil, err
			}
			return []sdk.Effect{sdk.Exec("echo", "marker: "+strings.TrimSpace(string(data)))}, nil
		},
		"where": func(sdk.Project, []string) ([]sdk.Effect, error) {
			return []sdk.Effect{sdk.Exec("sh", "-c", `echo "dir: $(pwd)"; echo "worktree: $LANEWAY_WORKTREE"`)}, nil
		},
		"warn": func(_ sdk.Project, args []string) ([]sdk.Effect, error) {
			return []sdk.Effect{sdk.Warning(strings.Join(args, " "))}, nil
		},
		"stop": func(sdk.Project, []string) ([]sdk.Effect, error) {
			return []sdk.Effect{
				sdk.Exec("sh", "-c", "echo before; exit 3"),
				sdk.Exec("echo", "after"),
			}, nil
		},
		"fail": func(sdk.Project, []string) ([]sdk.Effect, error) {
			return nil, errors.New("tasks can't do that here")
		},
		// env returns an effect kind the manifest doesn't declare.
		"env": func(sdk.Project, []string) ([]sdk.Effect, error) {
			return []sdk.Effect{sdk.Env(map[string]string{"TASKS": "on"}), sdk.Exec("echo", "still ran")}, nil
		},
	}})
}
