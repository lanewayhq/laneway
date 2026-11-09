// Command procfile is a test Plugin: a Recipe for Projects with a Procfile,
// which declares their Processes one `name: command` per line. The web
// Process is the Primary Process.
package main

import (
	"bufio"
	"bytes"
	"strings"

	"github.com/lanewayhq/laneway/sdk"
)

func main() {
	sdk.Run(sdk.Plugin{Recipes: map[string]sdk.Recipe{"procfile": procfile}})
}

func procfile(p sdk.Project) (sdk.RecipeResult, error) {
	data, err := p.ReadFile("Procfile")
	if err != nil {
		return sdk.RecipeResult{}, err
	}
	layer := sdk.Layer{Processes: map[string]sdk.Process{}}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		name, command, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		layer.Processes[name] = sdk.Process{Command: strings.TrimSpace(command), Primary: name == "web", HTTP: name == "web"}
	}
	return sdk.RecipeResult{Layer: layer}, nil
}
