package recipe_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/detect"
	"github.com/lanewayhq/laneway/internal/recipe"
)

// setupStack is a test Recipe, detected by a `setup-stack` file, with setup
// steps of its own.
type setupStack struct{}

func (setupStack) Name() string { return "setup-stack" }

func (setupStack) Detection() detect.Rule { return detect.Rule{File: "setup-stack"} }

func (setupStack) Defaults(dir string) (recipe.Result, error) {
	return layered(config.ParseLayer("the setup-stack Recipe", []byte(`
[worktree]
on_create = ["composer install", "php artisan storage:link"]
on_remove = ["php artisan cache:clear"]
`)))
}

func TestTheRecipesSetupStepsRunFirstThenEachLayersInOrder(t *testing.T) {
	res := mustResolveFixture(t, "setup-inherited", setupStack{})

	wantCreate := []string{"composer install", "php artisan storage:link", "php artisan migrate", "./scripts/seed-my-data"}
	if !reflect.DeepEqual(res.OnCreate, wantCreate) {
		t.Errorf("OnCreate = %q, want %q", res.OnCreate, wantCreate)
	}
	wantRemove := []string{"php artisan cache:clear", "./scripts/forget-worktree"}
	if !reflect.DeepEqual(res.OnRemove, wantRemove) {
		t.Errorf("OnRemove = %q, want %q", res.OnRemove, wantRemove)
	}
}

func TestAProjectCanTurnOffInheritingTheRecipesSetupSteps(t *testing.T) {
	res := mustResolveFixture(t, "setup-not-inherited", setupStack{})

	if want := []string{"make setup"}; !reflect.DeepEqual(res.OnCreate, want) {
		t.Errorf("OnCreate = %q, want %q", res.OnCreate, want)
	}
	if len(res.OnRemove) != 0 {
		t.Errorf("OnRemove = %q, want none", res.OnRemove)
	}
}

func TestEveryLayersCopyPathsAreCopiedOnce(t *testing.T) {
	res := mustResolveFixture(t, "setup-copy")

	if want := []string{"AGENTS.md", "CLAUDE.md", ".claude/skills", "notes/todo.md"}; !reflect.DeepEqual(res.Copy, want) {
		t.Errorf("Copy = %q, want %q", res.Copy, want)
	}
}

func TestTheWorktreeRootIsNextToTheMainWorktreeUnlessConfigured(t *testing.T) {
	if got := mustResolveFixture(t, "committed-only").WorktreeRootOf("/code/shop"); got != "/code/shop.worktrees" {
		t.Errorf("default worktree root = %q, want /code/shop.worktrees", got)
	}
	res := mustResolveFixture(t, "setup-worktree-root")
	if got := res.WorktreeRootOf("/code/shop"); got != "/code/trees" {
		t.Errorf("worktree root with worktree.root = \"../trees\" = %q, want /code/trees (relative to the main Worktree)", got)
	}
	res.WorktreeRoot = "/srv/trees"
	if got := res.WorktreeRootOf("/code/shop"); got != "/srv/trees" {
		t.Errorf("worktree root with an absolute worktree.root = %q, want /srv/trees", got)
	}
}

func TestSetupConfigWrittenAsALayerResolvesToItself(t *testing.T) {
	for _, fixture := range []string{"setup-inherited", "setup-not-inherited", "setup-worktree-root", "setup-copy"} {
		t.Run(fixture, func(t *testing.T) {
			res := mustResolveFixture(t, fixture, setupStack{})

			var buf strings.Builder
			if err := config.Encode(&buf, res.Layer()); err != nil {
				t.Fatal(err)
			}
			written, err := config.ParseLayer("written", []byte(buf.String()))
			if err != nil {
				t.Fatalf("reading back:\n%s\n%v", buf.String(), err)
			}
			dir := filepath.Join("testdata", fixture)
			for _, recipes := range [][]recipe.Recipe{nil, {setupStack{}}} {
				again, err := recipe.Resolve(recipe.Input{Engines: firstPartyEngines(), Dir: dir, Layers: []config.Layer{written}, Recipes: recipes})
				if err != nil {
					t.Fatal(err)
				}
				again.Recipe, again.RecipeRule, again.Warnings = res.Recipe, res.RecipeRule, res.Warnings
				if !reflect.DeepEqual(again, res) {
					t.Errorf("with Recipes %v, the written config\n%s\nresolves to\n  %+v\nwant\n  %+v", recipes, buf.String(), again, res)
				}
			}
		})
	}
}

func TestATopLevelWorktreeKeyNamesWhereItMoved(t *testing.T) {
	for _, tc := range []struct{ toml, want string }{
		{`on_create = ["composer install"]`, "on_create moved into the [worktree] table: write it as on_create under [worktree]"},
		{`on_remove = ["true"]`, "write it as on_remove under [worktree]"},
		{`inherit_setup = false`, "write it as inherit_setup under [worktree]"},
		{`copy = ["AGENTS.md"]`, "write it as copy under [worktree]"},
		{`worktree_root = "../trees"`, "worktree_root moved into the [worktree] table: write it as root under [worktree]"},
	} {
		_, err := config.ParseLayer("/code/shop/laneway.toml", []byte(tc.toml))
		if err == nil || !strings.Contains(err.Error(), "/code/shop/laneway.toml") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one naming the file and %q", tc.toml, err, tc.want)
		}
	}
}

func TestInvalidSetupConfigNamesTheFileAndTheKey(t *testing.T) {
	for _, tc := range []struct{ toml, want string }{
		{"[worktree]\non_create = [\"composer install\", \" \"]", "worktree.on_create[1] is empty"},
		{"[worktree]\non_remove = [\"\"]", "worktree.on_remove[0] is empty"},
		{"[worktree]\non_create = \"composer install\"", "worktree.on_create"},
		{"[worktree]\ninherit_setup = \"no\"", "worktree.inherit_setup"},
		{"[worktree]\nroot = \"\"", "worktree.root is empty"},
		{"[worktree]\ncopy = [\"AGENTS.md\", \"\"]", "worktree.copy[1]"},
		{"[worktree]\ncopy = [\"../shared/.env\"]", "worktree.copy[0]"},
		{"[worktree]\ncopy = [\"/etc/hosts\"]", "worktree.copy[0]"},
		{"[worktree]\ncopy = \"AGENTS.md\"", "worktree.copy"},
		{`worktree = "../trees"`, "worktree: type mismatch"},
	} {
		_, err := config.ParseLayer("/code/shop/laneway.toml", []byte(tc.toml))
		if err == nil || !strings.Contains(err.Error(), "/code/shop/laneway.toml") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one naming the file and %q", tc.toml, err, tc.want)
		}
	}
}
