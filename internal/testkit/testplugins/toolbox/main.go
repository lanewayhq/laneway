// Command toolbox is a test Plugin that shows links, badges and actions on
// the Dashboard. Its toolbox Integration detects Projects with a
// toolbox.json and adds nothing to their config; its hello command echoes a
// greeting in the Worktree, and fail fails.
package main

import (
	"errors"

	"github.com/lanewayhq/laneway/sdk"
)

func main() {
	sdk.Run(sdk.Plugin{
		Integrations: map[string]sdk.Integration{
			"toolbox": func(sdk.Project) (sdk.IntegrationResult, error) { return sdk.IntegrationResult{}, nil },
		},
		Commands: map[string]sdk.Command{
			"hello": func(_ sdk.Project, args []string) ([]sdk.Effect, error) {
				return []sdk.Effect{sdk.Exec(append([]string{"sh", "-c", `echo "hello $* from $(basename "$PWD")"`, "hello"}, args...)...)}, nil
			},
			"fail": func(sdk.Project, []string) ([]sdk.Effect, error) {
				return nil, errors.New("the toolbox is locked")
			},
		},
	})
}
