// Command vite is the vite Plugin, built into Laneway: the vite Integration,
// for a Project whose package.json depends on vite, or on
// laravel-vite-plugin, whatever its Recipe. It adds the vite Process: an HTTP
// Process, served at vite.<domain>, running the Project's Vite dev server
// with live reload through Laneway's proxy.
package main

import "github.com/lanewayhq/laneway/sdk"

func main() {
	sdk.Run(sdk.Plugin{Integrations: map[string]sdk.Integration{"vite": integration}})
}

func integration(p sdk.Project) (sdk.IntegrationResult, error) {
	command := viteCommand
	if p.Packages()["laravel-vite-plugin"] {
		command = laravelViteCommand
	}
	return sdk.IntegrationResult{Layer: sdk.Layer{Processes: map[string]sdk.Process{
		"vite": {Command: command, HTTP: true},
	}}}, nil
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
