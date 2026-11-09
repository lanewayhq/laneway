package recipe

import (
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/service"
)

// railsRedis adds Redis when the app uses it in development: for Sidekiq,
// or for Action Cable on its redis adapter. Both reach it through
// REDIS_URL, which Sidekiq reads by default and a new Rails app's
// config/cable.yml passes to Action Cable.
func railsRedis(l *config.Layer, dir string) {
	if !gemfileDeclares(dir, "sidekiq") && railsCableAdapter(dir) != "redis" {
		return
	}
	if l.Services == nil {
		l.Services = map[string]string{}
	}
	l.Services[service.Redis.Name] = service.Redis.DefaultVersion
	l.Env["REDIS_URL"] = "redis://:{redis_password}@{redis_host}:{redis_port}"
}

// railsCableAdapter is the Action Cable adapter config/cable.yml gives the
// development environment; "" if it gives none or can't be read.
func railsCableAdapter(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "config", "cable.yml"))
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
