package plugins_test

import (
	"reflect"
	"testing"

	"github.com/lanewayhq/laneway/internal/recipe"
)

// viteCommand is the vite Integration's Process in a Project using
// laravel-vite-plugin: Vite run with the
// config Laneway generates around the Project's (spike #2), on the port
// Laneway picked, after removing a public/hot an unclean exit left behind.
const viteCommand = "rm -f public/hot; LARAVEL_BYPASS_ENV_CHECK=1 node_modules/.bin/vite --config {vite_config} --host 127.0.0.1 --port {port} --strictPort"

func TestALaravelProjectUsingLaravelVitePluginGetsALazyViteProcessServedAtItsOwnDomain(t *testing.T) {
	res := resolveLaravel(t, "laravel-vite")

	want := recipe.Process{Name: "vite", Command: viteCommand, HTTP: true, StartMode: recipe.Lazy}
	if got := process(t, res, "vite"); !reflect.DeepEqual(got, want) {
		t.Errorf("vite =\n  %+v\nwant\n  %+v", got, want)
	}
	if web := process(t, res, "web"); !web.Primary {
		t.Errorf("web = %+v, want it still the Primary Process", web)
	}
}

func TestAViteProcessIsLaravelsOnlyWhenPackageJSONUsesLaravelVitePlugin(t *testing.T) {
	for _, tc := range []struct {
		name, packageJSON string
		command           string // "" for no vite Process
	}{
		{"in devDependencies", `{"devDependencies": {"laravel-vite-plugin": "^3.1", "vite": "^8.0.0"}}`, viteCommand},
		{"in dependencies", `{"dependencies": {"laravel-vite-plugin": "^3.1"}}`, viteCommand},
		{"vite alone", `{"devDependencies": {"vite": "^8.0.0"}}`, plainViteCommand},
		{"vite in dependencies", `{"dependencies": {"vite": "^8.0.0"}}`, plainViteCommand},
		{"mentioned only in a script", `{"scripts": {"x": "echo laravel-vite-plugin vite"}}`, ""},
		{"not JSON", `laravel-vite-plugin vite`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := resolveDir(t, laravelProject(t, map[string]string{".env": "DB_CONNECTION=sqlite\n", "package.json": tc.packageJSON}))

			var got string
			for _, p := range res.Processes {
				if p.Name == "vite" {
					got = p.Command
				}
			}
			if got != tc.command {
				t.Errorf("the vite Process's command = %q, want %q (Processes %+v)", got, tc.command, res.Processes)
			}
		})
	}
	res := resolveDir(t, laravelProject(t, map[string]string{".env": "DB_CONNECTION=sqlite\n"}))
	for _, p := range res.Processes {
		if p.Name == "vite" {
			t.Errorf("without package.json, there is a vite Process: %+v", p)
		}
	}
	if res.Integrations != nil {
		t.Errorf("without package.json, Integrations = %q, want none", res.Integrations)
	}
}

// plainViteCommand is the vite Integration's Process outside a
// laravel-vite-plugin Project: Vite alone, with Laneway's config and port.
const plainViteCommand = "node_modules/.bin/vite --config {vite_config} --host 127.0.0.1 --port {port} --strictPort"

func TestAProjectUsingViteGetsTheViteIntegrationWhateverItsStack(t *testing.T) {
	dir := project(t, map[string]string{
		"package.json":   `{"devDependencies": {"vite": "^8.0.0"}}`,
		"laneway.toml":   "[processes.web]\ncommand = \"serve --port={port}\"\n",
		"vite.config.js": "export default {};\n",
	})

	res := resolveDir(t, dir)

	if res.Recipe != "" || !reflect.DeepEqual(res.Integrations, []string{"vite"}) {
		t.Errorf("Recipe %q, Integrations %q; want no Recipe and the vite Integration", res.Recipe, res.Integrations)
	}
	want := recipe.Process{Name: "vite", Command: plainViteCommand, HTTP: true, StartMode: recipe.Lazy}
	if got := process(t, res, "vite"); !reflect.DeepEqual(got, want) {
		t.Errorf("vite =\n  %+v\nwant\n  %+v", got, want)
	}
	if web := process(t, res, "web"); !web.Primary {
		t.Errorf("web = %+v, want it still the Primary Process: an Integration's Process never takes the Domain", web)
	}
}

func TestAProjectWhoseOnlyProcessIsVitesServesViteAtItsDomain(t *testing.T) {
	res := resolveDir(t, project(t, map[string]string{"package.json": `{"devDependencies": {"vite": "^8.0.0"}}`}))

	want := []recipe.Process{{Name: "vite", Command: plainViteCommand, Primary: true, HTTP: true, StartMode: recipe.Lazy}}
	if !reflect.DeepEqual(res.Processes, want) {
		t.Errorf("Processes =\n  %+v\nwant\n  %+v", res.Processes, want)
	}
}
