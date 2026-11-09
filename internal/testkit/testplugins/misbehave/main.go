// Command misbehave is a test Plugin whose Recipe does what the Project's
// misbehave file says, to test how Laneway contains a Plugin:
//
//   - "hang": never answers
//   - "fail": writes to stderr and exits with status 3
//   - "write": tries to write the file written-by-plugin in the Project,
//     and sets WRITE_RESULT in the Env Patch to what happened
//   - anything else: writes it to stderr and answers with an empty layer
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/lanewayhq/plugins/sdk"
)

func main() {
	sdk.Run(sdk.Plugin{Recipes: map[string]sdk.Recipe{"misbehave": misbehave}})
}

func misbehave(p sdk.Project) (sdk.RecipeResult, error) {
	what, err := p.ReadFile("misbehave")
	if err != nil {
		return sdk.RecipeResult{}, err
	}
	switch strings.TrimSpace(string(what)) {
	case "hang":
		for {
		}
	case "fail":
		fmt.Fprintln(os.Stderr, "misbehave: something broke")
		os.Exit(3)
	case "write":
		result := "written"
		if err := os.WriteFile("written-by-plugin", []byte("x"), 0o644); err != nil {
			result = err.Error()
		}
		return sdk.RecipeResult{Layer: sdk.Layer{Env: map[string]string{"WRITE_RESULT": result}}}, nil
	}
	fmt.Fprintln(os.Stderr, strings.TrimSpace(string(what)))
	return sdk.RecipeResult{}, nil
}
