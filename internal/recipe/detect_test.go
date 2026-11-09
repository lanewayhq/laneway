package recipe_test

import (
	"reflect"
	"testing"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/detect"
	"github.com/lanewayhq/laneway/internal/recipe"
)

// ruleRecipe is a test Recipe detected by the rule it declares, adding
// nothing.
type ruleRecipe struct {
	name string
	rule detect.Rule
}

func (r ruleRecipe) Name() string           { return r.name }
func (r ruleRecipe) Detection() detect.Rule { return r.rule }
func (r ruleRecipe) Defaults(string) (recipe.Result, error) {
	return recipe.Result{Layer: config.Layer{Source: "the " + r.name + " Recipe"}}, nil
}

// detectRecipe resolves a Project with the given files against one Recipe
// declaring rule, returning the Recipe's name and the rule that matched ("",
// "" if it didn't detect the Project).
func detectRecipe(t *testing.T, files map[string]string, rule detect.Rule) (name, matched string) {
	t.Helper()
	res, err := recipe.Resolve(recipe.Input{Engines: firstPartyEngines(), Dir: project(t, files), Recipes: []recipe.Recipe{ruleRecipe{"stack", rule}}})
	if err != nil {
		t.Fatal(err)
	}
	return res.Recipe, res.RecipeRule
}

func TestARecipeDeclaringAFileRuleDetectsAProjectWithThatFile(t *testing.T) {
	name, matched := detectRecipe(t, map[string]string{"artisan": "#!/usr/bin/env php\n"}, detect.Rule{File: "artisan"})

	if name != "stack" || matched != "artisan exists" {
		t.Errorf("Recipe %q matched by %q, want stack matched by %q", name, matched, "artisan exists")
	}
}

func TestARecipeDeclaringAllOfAFileAndAGemDetectsOnlyAProjectWithBoth(t *testing.T) {
	rule := detect.Rule{All: []detect.Rule{{File: "config/application.rb"}, {Gem: "rails"}}}
	app := "module Shop\n  class Application < Rails::Application\n  end\nend\n"
	gemfile := "source \"https://rubygems.org\"\n\ngem \"rails\", \"~> 8.1\"\n"
	for _, tc := range []struct {
		name    string
		files   map[string]string
		matched string // "" for not detected
	}{
		{"both", map[string]string{"config/application.rb": app, "Gemfile": gemfile}, "config/application.rb exists and Gemfile declares rails"},
		{"only the file", map[string]string{"config/application.rb": app}, ""},
		{"only the gem", map[string]string{"Gemfile": gemfile}, ""},
		{"the gem commented out", map[string]string{"config/application.rb": app, "Gemfile": "# gem \"rails\"\n"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, matched := detectRecipe(t, tc.files, rule); matched != tc.matched {
				t.Errorf("matched by %q, want %q", matched, tc.matched)
			}
		})
	}
}

func TestARecipeDeclaringAnyOfTwoRulesNamesTheOneTheProjectMatched(t *testing.T) {
	rule := detect.Rule{Any: []detect.Rule{{Gem: "rails"}, {Gem: "railties"}}}
	for _, tc := range []struct {
		gemfile string
		matched string // "" for not detected
	}{
		{"gem \"rails\"\n", "Gemfile declares rails"},
		{"gem \"railties\"\ngem \"actionpack\"\n", "Gemfile declares railties"},
		{"gem \"railties\"\ngem \"rails\"\n", "Gemfile declares rails"},
		{"gem \"sinatra\"\n", ""},
	} {
		if _, matched := detectRecipe(t, map[string]string{"Gemfile": tc.gemfile}, rule); matched != tc.matched {
			t.Errorf("Gemfile %q: matched by %q, want %q", tc.gemfile, matched, tc.matched)
		}
	}
}

// ruleIntegration is a test Integration detected by the rule it declares,
// adding nothing.
type ruleIntegration struct {
	name string
	rule detect.Rule
}

func (i ruleIntegration) Name() string           { return i.name }
func (i ruleIntegration) Detection() detect.Rule { return i.rule }
func (i ruleIntegration) Defaults(string) (config.Layer, error) {
	return config.Layer{Source: "the " + i.name + " Integration"}, nil
}

func TestEachIntegrationNamesTheRuleTheProjectMatched(t *testing.T) {
	dir := project(t, map[string]string{
		"package.json": `{"dependencies": {"tailwindcss": "^4"}, "devDependencies": {"vite": "^8.0.0"}}`,
	})
	res, err := recipe.Resolve(recipe.Input{Engines: firstPartyEngines(), Dir: dir, Integrations: []recipe.Integration{
		ruleIntegration{"vite", detect.Rule{Any: []detect.Rule{{Package: "laravel-vite-plugin"}, {Package: "vite"}}}},
		ruleIntegration{"tailwind", detect.Rule{Package: "tailwindcss"}},
		ruleIntegration{"webpack", detect.Rule{Package: "webpack"}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{"tailwind": "package.json depends on tailwindcss", "vite": "package.json depends on vite"}
	if !reflect.DeepEqual(res.Integrations, []string{"tailwind", "vite"}) || !reflect.DeepEqual(res.IntegrationRules, want) {
		t.Errorf("Integrations %q matched by %q, want tailwind and vite matched by %q", res.Integrations, res.IntegrationRules, want)
	}
}

func TestARecipeDeclaringAComposerRuleDetectsAProjectRequiringThatPackage(t *testing.T) {
	rule := detect.Rule{Composer: "laravel/framework"}
	for _, tc := range []struct {
		composerJSON string
		matched      string // "" for not detected
	}{
		{`{"require": {"php": "^8.4", "laravel/framework": "^13.0"}}`, "composer.json requires laravel/framework"},
		{`{"require-dev": {"laravel/framework": "^13.0"}}`, "composer.json requires laravel/framework"},
		{`{"require": {"symfony/http-kernel": "^8.0"}, "suggest": {"laravel/framework": "for Laravel"}}`, ""},
		{`laravel/framework`, ""},
	} {
		if _, matched := detectRecipe(t, map[string]string{"composer.json": tc.composerJSON}, rule); matched != tc.matched {
			t.Errorf("composer.json %s: matched by %q, want %q", tc.composerJSON, matched, tc.matched)
		}
	}
}

func TestAnEmptyRuleDetectsNothing(t *testing.T) {
	files := map[string]string{"artisan": "", "Gemfile": "gem \"rails\"\n"}
	for _, tc := range []struct {
		name string
		rule detect.Rule
	}{
		{"no rule", detect.Rule{}},
		{"all of none", detect.Rule{All: []detect.Rule{}}},
		{"any of none", detect.Rule{Any: []detect.Rule{}}},
		{"all of an empty rule", detect.Rule{All: []detect.Rule{{}}}},
		{"all of a matching rule and an empty one", detect.Rule{All: []detect.Rule{{File: "artisan"}, {}}}},
	} {
		if name, matched := detectRecipe(t, files, tc.rule); name != "" {
			t.Errorf("%s: detected the Project as %q matched by %q, want not detected", tc.name, name, matched)
		}
	}
}

func TestRulesNestAndNameOnlyWhatMatched(t *testing.T) {
	rule := detect.Rule{Any: []detect.Rule{
		{All: []detect.Rule{{File: "artisan"}, {Composer: "laravel/framework"}}},
		{All: []detect.Rule{{File: "bin/console"}, {Any: []detect.Rule{{Composer: "symfony/framework-bundle"}, {Composer: "symfony/symfony"}}}}},
	}}
	for _, tc := range []struct {
		name    string
		files   map[string]string
		matched string // "" for not detected
	}{
		{"the first branch", map[string]string{"artisan": "", "composer.json": `{"require": {"laravel/framework": "^13.0"}}`},
			"artisan exists and composer.json requires laravel/framework"},
		{"the second branch", map[string]string{"bin/console": "", "composer.json": `{"require": {"symfony/symfony": "^7.0"}}`},
			"bin/console exists and composer.json requires symfony/symfony"},
		{"half of each", map[string]string{"artisan": "", "composer.json": `{"require": {"symfony/symfony": "^7.0"}}`}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, matched := detectRecipe(t, tc.files, rule); matched != tc.matched {
				t.Errorf("matched by %q, want %q", matched, tc.matched)
			}
		})
	}
}
