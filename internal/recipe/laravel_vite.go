package recipe

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// viteCommand runs the Project's Vite dev server behind Portby (spike #2):
//
//   - --config {vite_config} is a config Portby writes around the Project's
//     own vite.config.*, so HMR goes through vite.<domain> over wss and
//     laravel-vite-plugin writes https://vite.<domain> to public/hot. No
//     committed file changes.
//   - The vite binary directly, not `npm run dev`, which may run more.
//   - --host/--port/--strictPort: listen where Portby proxies to.
//   - LARAVEL_BYPASS_ENV_CHECK: the plugin refuses to serve when CI is set.
//   - rm -f public/hot: the plugin deletes it on a normal exit only; one a
//     killed Vite left behind would point pages at a dead server.
const viteCommand = "rm -f public/hot; LARAVEL_BYPASS_ENV_CHECK=1 node_modules/.bin/vite --config {vite_config} --host 127.0.0.1 --port {port} --strictPort"

// usesLaravelVitePlugin reports whether dir's package.json depends on
// laravel-vite-plugin.
func usesLaravelVitePlugin(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Dependencies    map[string]any `json:"dependencies"`
		DevDependencies map[string]any `json:"devDependencies"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return false
	}
	_, dep := pkg.Dependencies["laravel-vite-plugin"]
	_, dev := pkg.DevDependencies["laravel-vite-plugin"]
	return dep || dev
}
