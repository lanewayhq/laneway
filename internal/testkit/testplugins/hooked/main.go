// Command hooked is a test Plugin that hooks Preparing, Ready and Worktree
// removed. Its Recipe detects Projects with a hooked file and adds nothing.
// Each hook does what the mounted Worktree's hooks.json says for its event:
//
//	{"ready": {"effects": [{"kind": "exec", "command": "touch ran"}], "error": "…"}}
//
// returning the effects, or failing with the error. In effects' strings,
// {event}, {project}, {project_path}, {worktree}, {worktree_path}, {main},
// {domain} and {url} become the event's values.
package main

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/lanewayhq/laneway/sdk"
)

func main() {
	sdk.Run(sdk.Plugin{
		Recipes: map[string]sdk.Recipe{"hooked": func(sdk.Project) (sdk.RecipeResult, error) { return sdk.RecipeResult{}, nil }},
		Hooks: map[string]sdk.Hook{
			sdk.EventPreparing:       hook,
			sdk.EventReady:           hook,
			sdk.EventWorktreeRemoved: hook,
		},
	})
}

type script struct {
	Effects []sdk.Effect `json:"effects"`
	Error   string       `json:"error"`
}

func hook(p sdk.Project, ev sdk.Event) ([]sdk.Effect, error) {
	data, err := p.ReadFile("hooks.json")
	if err != nil {
		return nil, nil
	}
	var scripts map[string]script
	if err := json.Unmarshal(data, &scripts); err != nil {
		return nil, err
	}
	s := scripts[ev.Name]
	if s.Error != "" {
		return nil, errors.New(s.Error)
	}
	w := ev.Worktree
	if w == nil {
		w = &sdk.EventWorktree{}
	}
	r := strings.NewReplacer("{event}", ev.Name, "{project}", ev.Project.Name, "{project_path}", ev.Project.Path,
		"{worktree}", w.Name, "{worktree_path}", w.Path, "{main}", strconv.FormatBool(w.Main),
		"{domain}", w.Domain, "{url}", w.URL)
	for i, e := range s.Effects {
		e.Command, e.Message = r.Replace(e.Command), r.Replace(e.Message)
		if e.Env != nil {
			env := map[string]string{}
			for k, v := range e.Env {
				env[k] = r.Replace(v)
			}
			e.Env = env
		}
		s.Effects[i] = e
	}
	return s.Effects, nil
}
