package recipe_test

import (
	"reflect"
	"testing"

	"github.com/lanewayhq/laneway/internal/recipe"
)

// viteCommand is the Laravel Recipe's vite Process: Vite run with the
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

func TestALaravelProjectGetsAViteProcessOnlyWhenItsPackageJSONUsesLaravelVitePlugin(t *testing.T) {
	for _, tc := range []struct {
		name, packageJSON string
		vite              bool
	}{
		{"in devDependencies", `{"devDependencies": {"laravel-vite-plugin": "^3.1", "vite": "^8.0.0"}}`, true},
		{"in dependencies", `{"dependencies": {"laravel-vite-plugin": "^3.1"}}`, true},
		{"vite alone", `{"devDependencies": {"vite": "^8.0.0"}}`, false},
		{"mentioned only in a script", `{"scripts": {"x": "echo laravel-vite-plugin"}}`, false},
		{"not JSON", `laravel-vite-plugin`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := resolveDir(t, laravelProject(t, map[string]string{".env": "DB_CONNECTION=sqlite\n", "package.json": tc.packageJSON}))

			var got bool
			for _, p := range res.Processes {
				got = got || p.Name == "vite"
			}
			if got != tc.vite {
				t.Errorf("a vite Process = %v, want %v (Processes %+v)", got, tc.vite, res.Processes)
			}
		})
	}
	res := resolveDir(t, laravelProject(t, map[string]string{".env": "DB_CONNECTION=sqlite\n"}))
	for _, p := range res.Processes {
		if p.Name == "vite" {
			t.Errorf("without package.json, there is a vite Process: %+v", p)
		}
	}
}
