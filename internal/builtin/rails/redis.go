package main

import (
	"go.yaml.in/yaml/v3"

	"github.com/lanewayhq/laneway/sdk"
)

// redis adds Redis, at its Engine's default version, when the app uses it in
// development: for Sidekiq, or for Action Cable on its redis adapter. Both
// reach it through REDIS_URL, which Sidekiq reads by default and a new Rails
// app's config/cable.yml passes to Action Cable.
func redis(l *sdk.Layer, p sdk.Project) {
	if !p.Gems()["sidekiq"] && cableAdapter(p) != "redis" {
		return
	}
	if l.Services == nil {
		l.Services = map[string]string{}
	}
	l.Services["redis"] = ""
	l.Env["REDIS_URL"] = "redis://:{redis_password}@{redis_host}:{redis_port}"
}

// cableAdapter is the Action Cable adapter config/cable.yml gives the
// development environment; "" if it gives none or can't be read.
func cableAdapter(p sdk.Project) string {
	data, err := p.ReadFile("config/cable.yml")
	if err != nil {
		return ""
	}
	var doc map[string]any
	if yaml.Unmarshal(withoutERB(data), &doc) != nil {
		return ""
	}
	dev, _ := doc["development"].(map[string]any)
	adapter, _ := dev["adapter"].(string)
	return adapter
}
