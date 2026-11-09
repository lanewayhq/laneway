package recipe_test

import (
	"reflect"
	"testing"

	"github.com/lanewayhq/laneway/internal/recipe"
)

func TestEachProcfileDevEntryOfARailsAppButTheWebServerIsAProcess(t *testing.T) {
	res := mustResolveDir(t, railsProject(t, map[string]string{"Procfile.dev": `# Run with bin/dev
web: bin/rails server -p 3000
js: yarn build --watch
worker:   bundle exec sidekiq -C config/sidekiq.yml

`}))

	want := []recipe.Process{
		{Name: "js", Command: "set -a; [ ! -f .env ] || . ./.env; set +a; yarn build --watch"},
		{Name: "web", Command: "set -a; [ ! -f .env ] || . ./.env; set +a; exec bin/rails server -b 127.0.0.1 -p {port}", Primary: true, HTTP: true},
		{Name: "worker", Command: "set -a; [ ! -f .env ] || . ./.env; set +a; bundle exec sidekiq -C config/sidekiq.yml"},
	}
	if !reflect.DeepEqual(res.Processes, want) {
		t.Errorf("Processes =\n  %+v\nwant\n  %+v", res.Processes, want)
	}
}

func TestARailsServerUnderAnotherNameInProcfileDevIsNotASecondProcess(t *testing.T) {
	res := mustResolveDir(t, railsProject(t, map[string]string{"Procfile.dev": "app: bundle exec rails s -p 3000\ncss: bin/rails dartsass:watch\n"}))

	var names []string
	for _, p := range res.Processes {
		names = append(names, p.Name)
	}
	if want := []string{"css", "web"}; !reflect.DeepEqual(names, want) {
		t.Errorf("Processes = %q, want %q", names, want)
	}
}

func TestTheTailwindWatcherOfARailsAppKeepsRunningWithoutATerminal(t *testing.T) {
	res := mustResolveDir(t, railsProject(t, map[string]string{"Procfile.dev": "web: bin/rails server\ncss: bin/rails tailwindcss:watch\n"}))

	// tailwindcss:watch stops when its stdin closes, and a Process's
	// stdin is empty; [always] keeps it watching.
	want := "set -a; [ ! -f .env ] || . ./.env; set +a; bin/rails tailwindcss:watch[always]"
	if got := process(t, res, "css").Command; got != want {
		t.Errorf("css = %q, want %q", got, want)
	}
}

func TestARailsAppWithoutProcfileDevRunsOnlyItsServer(t *testing.T) {
	res := mustResolveDir(t, railsProject(t, nil))

	if len(res.Processes) != 1 || res.Processes[0].Name != "web" {
		t.Errorf("Processes = %+v, want only web", res.Processes)
	}
}
