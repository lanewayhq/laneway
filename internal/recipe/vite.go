package recipe

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/lanewayhq/laneway/internal/config"
)

// Vite is the vite Integration: a Project whose package.json depends on
// vite, or on laravel-vite-plugin, whatever its Recipe. It adds the vite
// Process: an HTTP Process, served at vite.<domain>, running the Project's
// Vite dev server with live reload through Laneway's proxy.
type Vite struct{}

func (Vite) Name() string { return "vite" }

func (Vite) Detect(dir string) bool {
	deps := packageDependencies(dir)
	return deps["vite"] || deps["laravel-vite-plugin"]
}

func (Vite) Defaults(dir string) (config.Layer, error) {
	command := viteCommand
	if packageDependencies(dir)["laravel-vite-plugin"] {
		command = laravelViteCommand
	}
	return config.Layer{Source: "the vite Integration", Processes: map[string]config.ProcessLayer{
		"vite": {Command: ptr(command), HTTP: ptr(true)},
	}}, nil
}

// viteCommand runs the Project's Vite dev server behind Laneway (spike #2):
//
//   - --config {vite_config} is a config Laneway writes around the Project's
//     own vite.config.*, so HMR goes through vite.<domain> over wss and
//     laravel-vite-plugin writes https://vite.<domain> to public/hot. No
//     committed file changes.
//   - The vite binary directly, not `npm run dev`, which may run more.
//   - --host/--port/--strictPort: listen where Laneway proxies to.
const viteCommand = "node_modules/.bin/vite --config {vite_config} --host 127.0.0.1 --port {port} --strictPort"

// laravelViteCommand is viteCommand for a Project using laravel-vite-plugin:
//
//   - LARAVEL_BYPASS_ENV_CHECK: the plugin refuses to serve when CI is set.
//   - rm -f public/hot: the plugin deletes it on a normal exit only; one a
//     killed Vite left behind would point pages at a dead server.
const laravelViteCommand = "rm -f public/hot; LARAVEL_BYPASS_ENV_CHECK=1 " + viteCommand

// packageDependencies are the packages dir's package.json depends on, in
// dependencies or devDependencies; none without a readable package.json.
func packageDependencies(dir string) map[string]bool {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Dependencies    map[string]any `json:"dependencies"`
		DevDependencies map[string]any `json:"devDependencies"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return nil
	}
	deps := map[string]bool{}
	for name := range pkg.Dependencies {
		deps[name] = true
	}
	for name := range pkg.DevDependencies {
		deps[name] = true
	}
	return deps
}
