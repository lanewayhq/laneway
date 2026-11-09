package recipe_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/detect"
	"github.com/lanewayhq/laneway/internal/dotenv"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/runtimes"
)

// project writes files (name → content) into a fresh Project directory.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func php(version string, extensions ...string) runtimes.Runtime {
	return runtimes.Runtime{Name: runtimes.PHP, Version: version, Path: "/opt/php/bin/php", Extensions: extensions}
}

func node(version string) runtimes.Runtime {
	return runtimes.Runtime{Name: runtimes.Node, Version: version, Path: "/opt/node/bin/node"}
}

var (
	noPHP  = runtimes.Runtime{Name: runtimes.PHP}
	noNode = runtimes.Runtime{Name: runtimes.Node}
)

// resolveWith resolves the Project in dir checking the Runtimes found.
func resolveWith(t *testing.T, dir string, found ...runtimes.Runtime) recipe.Resolved {
	t.Helper()
	res, err := recipe.Resolve(recipe.Input{Engines: firstPartyEngines(), Dir: dir, Runtimes: found})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// assertRuntimeWarning asserts there is exactly one Runtime warning, about
// runtime, and that its message and fix contain the given substrings.
func assertRuntimeWarning(t *testing.T, res recipe.Resolved, runtime string, message, fix []string) {
	t.Helper()
	if len(res.RuntimeWarnings) != 1 {
		t.Fatalf("RuntimeWarnings = %+v, want one about %s", res.RuntimeWarnings, runtime)
	}
	w := res.RuntimeWarnings[0]
	if w.Runtime != runtime {
		t.Errorf("warning is about %q, want %q: %+v", w.Runtime, runtime, w)
	}
	for _, s := range message {
		if !strings.Contains(w.Message, s) {
			t.Errorf("message %q doesn't contain %q", w.Message, s)
		}
	}
	for _, s := range fix {
		if !strings.Contains(w.Fix, s) {
			t.Errorf("fix %q doesn't contain %q", w.Fix, s)
		}
	}
}

func TestAPHPOutsideComposerRequirePHPIsARuntimeWarning(t *testing.T) {
	dir := project(t, map[string]string{"composer.json": `{"require": {"php": "^8.2"}}`})

	res := resolveWith(t, dir, php("8.1.12"), noNode)

	assertRuntimeWarning(t, res, runtimes.PHP,
		[]string{"PHP 8.1.12", `"^8.2"`, "composer.json", "require.php"},
		[]string{"mise use php@8.2"})
}

func TestANodeOutsidePackageEnginesIsARuntimeWarning(t *testing.T) {
	dir := project(t, map[string]string{"package.json": `{"engines": {"node": ">=22"}}`})

	res := resolveWith(t, dir, noPHP, node("20.11.1"))

	assertRuntimeWarning(t, res, runtimes.Node,
		[]string{"Node 20.11.1", `">=22"`, "package.json", "engines.node"},
		[]string{"mise use node@22"})
}

// Constraints are read the way Composer (require.php) and npm (engines.node)
// read them.
func TestRuntimeConstraintsFollowComposerAndNpmSyntax(t *testing.T) {
	tests := []struct {
		constraint string
		version    string
		satisfied  bool
	}{
		// Composer
		{"^8.2", "8.2.0", true},
		{"^8.2", "8.4.12", true},
		{"^8.2", "9.0.0", false},
		{"^8.2", "8.1.31", false},
		{"^0.3", "0.4.0", false},
		{"~8.2", "8.9.1", true},    // Composer: <9.0
		{"~8.2.1", "8.3.0", false}, // <8.3
		{"~8.2.1", "8.2.0", false},
		{">=8.1 <8.4", "8.3.9", true},
		{">=8.1 <8.4", "8.4.0", false},
		{">=8.1,<8.4", "8.4.0", false},
		{">= 8.1", "8.1.0", true},
		{"^8.1 || ^9.0", "9.2.0", true},
		{"^7.4|^8.0", "8.3.0", true},
		{"^7.4|^8.0", "7.3.0", false},
		{"8.2.*", "8.2.27", true},
		{"8.2.*", "8.3.0", false},
		{"8.2", "8.2.1", false}, // a bare version is exact for Composer
		{">8.2", "8.2.1", true},
		{"!=8.3.0", "8.3.0", false},
		{"8.1 - 8.3", "8.3.20", true},
		{"8.1 - 8.3", "8.4.0", false},
		{"*", "5.6.0", true},
		{"^8.3@dev", "8.3.0", true},
		{"^8.4", "8.4.0RC1", true},
		{"^8.4", "8.5.0-dev", true},
	}
	for _, tt := range tests {
		t.Run("composer "+tt.constraint+" "+tt.version, func(t *testing.T) {
			dir := project(t, map[string]string{"composer.json": `{"require": {"php": "` + tt.constraint + `"}}`})
			res := resolveWith(t, dir, php(tt.version), noNode)
			if got := len(res.RuntimeWarnings) == 0; got != tt.satisfied {
				t.Errorf("PHP %s satisfies %q: got %v, want %v (warnings %+v)", tt.version, tt.constraint, got, tt.satisfied, res.RuntimeWarnings)
			}
		})
	}

	npm := []struct {
		constraint string
		version    string
		satisfied  bool
	}{
		{">=18", "v26.12.0", true},
		{">=18", "16.20.2", false},
		{"^20 || ^22", "22.11.0", true},
		{"^20 || ^22", "21.0.0", false},
		{"~20.10", "20.11.0", false}, // npm: <20.11
		{"~20.10", "20.10.5", true},
		{"20", "20.18.0", true}, // a bare version is 20.x for npm
		{"20.x", "21.0.0", false},
		{">=18.0.0 <21", "20.9.0", true},
		{">20", "20.5.0", false}, // npm: >=21
		{"<=20", "20.5.0", true}, // npm: <21
		{"18 - 20", "20.19.0", true},
		{"18 - 20", "21.1.0", false},
		{"*", "8.0.0", true},
	}
	for _, tt := range npm {
		t.Run("npm "+tt.constraint+" "+tt.version, func(t *testing.T) {
			dir := project(t, map[string]string{"package.json": `{"engines": {"node": "` + tt.constraint + `"}}`})
			res := resolveWith(t, dir, noPHP, node(tt.version))
			if got := len(res.RuntimeWarnings) == 0; got != tt.satisfied {
				t.Errorf("Node %s satisfies %q: got %v, want %v (warnings %+v)", tt.version, tt.constraint, got, tt.satisfied, res.RuntimeWarnings)
			}
		})
	}
}

func TestAnExtensionComposerRequiresButPHPDoesNotLoadIsARuntimeWarning(t *testing.T) {
	dir := project(t, map[string]string{"composer.json": `{"require": {
		"php": "^8.2", "ext-mbstring": "*", "ext-intl": "*", "ext-zend-opcache": "*", "ext-pdo_pgsql": "*", "laravel/framework": "^12.0"}}`})

	res := resolveWith(t, dir, php("8.4.1", "core", "mbstring", "zend-opcache", "pdo_pgsql"), noNode)

	assertRuntimeWarning(t, res, runtimes.PHP,
		[]string{"intl", "ext-intl", "composer.json", "8.4.1"},
		[]string{"intl"})
}

func TestAMissingRuntimeTheProjectNeedsSuggestsInstallingItWithMise(t *testing.T) {
	t.Run("php", func(t *testing.T) {
		dir := project(t, map[string]string{"composer.json": `{"require": {"php": "^8.3"}}`})
		res := resolveWith(t, dir, noPHP, noNode)
		assertRuntimeWarning(t, res, runtimes.PHP,
			[]string{"PHP", "not found", "composer.json"},
			[]string{"mise use php@8.3", "never installs"})
	})
	t.Run("node", func(t *testing.T) {
		dir := project(t, map[string]string{"package.json": `{"name": "shop"}`})
		res := resolveWith(t, dir, noPHP, noNode)
		assertRuntimeWarning(t, res, runtimes.Node,
			[]string{"Node", "not found", "package.json"},
			[]string{"mise use node@latest"})
	})
}

func TestRuntimesMatchingTheProjectsConstraintsGiveNoWarnings(t *testing.T) {
	dir := project(t, map[string]string{
		"composer.json": `{"require": {"php": "^8.2", "ext-mbstring": "*"}}`,
		"package.json":  `{"engines": {"node": ">=20"}}`,
	})

	res := resolveWith(t, dir, php("8.4.1", "mbstring"), node("v22.11.0"))

	if len(res.RuntimeWarnings) != 0 {
		t.Errorf("RuntimeWarnings = %+v, want none", res.RuntimeWarnings)
	}
}

func TestRuntimesAProjectDoesNotUseAreNotChecked(t *testing.T) {
	res := resolveWith(t, project(t, nil), noPHP, noNode)

	if len(res.RuntimeWarnings) != 0 {
		t.Errorf("RuntimeWarnings = %+v, want none", res.RuntimeWarnings)
	}
}

func TestWithoutResolvedRuntimesNothingIsChecked(t *testing.T) {
	dir := project(t, map[string]string{"composer.json": `{"require": {"php": "^8.3"}}`})

	res, err := recipe.Resolve(recipe.Input{Engines: firstPartyEngines(), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.RuntimeWarnings) != 0 {
		t.Errorf("RuntimeWarnings = %+v, want none", res.RuntimeWarnings)
	}
}

func TestAConstraintThatCannotBeReadIsARuntimeWarning(t *testing.T) {
	dir := project(t, map[string]string{"package.json": `{"engines": {"node": "lts/iron"}}`})

	res := resolveWith(t, dir, noPHP, node("20.0.0"))

	assertRuntimeWarning(t, res, runtimes.Node, []string{"can't check", `"lts/iron"`}, nil)
}

func TestAManifestThatIsNotJSONIsARuntimeWarning(t *testing.T) {
	dir := project(t, map[string]string{"composer.json": `{"require": `})

	res := resolveWith(t, dir, php("8.4.0"), noNode)

	assertRuntimeWarning(t, res, runtimes.PHP, []string{"composer.json"}, nil)
}

func ruby(version string) runtimes.Runtime {
	return runtimes.Runtime{Name: runtimes.Ruby, Version: version, Path: "/opt/ruby/bin/ruby"}
}

var noRuby = runtimes.Runtime{Name: runtimes.Ruby}

func TestAMissingRubyAProjectWithAGemfileNeedsSuggestsTheVersionItsGemfileAsksFor(t *testing.T) {
	dir := project(t, map[string]string{"Gemfile": "source \"https://rubygems.org\"\n\nruby \"3.4.7\"\n\ngem \"rails\"\n"})

	res := resolveWith(t, dir, noPHP, noNode, noRuby)

	assertRuntimeWarning(t, res, runtimes.Ruby,
		[]string{"Ruby was not found through mise or on PATH", "Gemfile"},
		[]string{"mise use ruby@3.4"})
}

func TestARubyOutsideTheGemfilesRubyIsARuntimeWarning(t *testing.T) {
	dir := project(t, map[string]string{"Gemfile": "ruby \"~> 3.4\"\ngem \"rails\"\n"})

	res := resolveWith(t, dir, noPHP, noNode, ruby("3.3.6"))

	assertRuntimeWarning(t, res, runtimes.Ruby,
		[]string{"Ruby 3.3.6", `"~> 3.4"`, "Gemfile"},
		[]string{"mise use ruby@3.4"})
}

// The Gemfile's ruby is read the way Bundler reads it: RubyGems version
// requirements, all of which must hold, or the version in a file.
func TestTheGemfilesRubyFollowsRubyGemsSyntax(t *testing.T) {
	tests := []struct {
		ruby, rubyVersion string // the Gemfile's ruby arguments, and .ruby-version
		version           string
		satisfied         bool
	}{
		{`"3.4.7"`, "", "3.4.7", true},
		{`"3.4.7"`, "", "3.4.6", false},
		{`"~> 3.3"`, "", "3.9.1", true},
		{`"~> 3.3"`, "", "4.0.0", false},
		{`"~> 3.3.5"`, "", "3.3.9", true},
		{`"~> 3.3.5"`, "", "3.4.0", false},
		{`">= 3.1", "< 4"`, "", "3.4.1", true},
		{`">= 3.1", "< 4"`, "", "4.0.0", false},
		{`'>= 3.2'`, "", "3.1.6", false},
		{`file: ".ruby-version"`, "ruby-3.4.7\n", "3.4.7", true},
		{`file: ".ruby-version"`, "3.4.7\n", "3.4.1", false},
		{`"3.4.7", engine: "ruby", engine_version: "3.4.7"`, "", "3.4.7", true},
	}
	for _, tt := range tests {
		t.Run(tt.ruby+" "+tt.version, func(t *testing.T) {
			files := map[string]string{"Gemfile": "source \"https://rubygems.org\"\nruby " + tt.ruby + "\ngem \"rails\"\n"}
			if tt.rubyVersion != "" {
				files[".ruby-version"] = tt.rubyVersion
			}
			res := resolveWith(t, project(t, files), noPHP, noNode, ruby(tt.version))
			if got := len(res.RuntimeWarnings) == 0; got != tt.satisfied {
				t.Errorf("Ruby %s satisfies ruby %s: got %v, want %v (warnings %+v)", tt.version, tt.ruby, got, tt.satisfied, res.RuntimeWarnings)
			}
		})
	}
}

// resolveWithDatabase resolves the Project in dir, whose laneway.toml uses
// the database engine, checking the Runtimes found.
func resolveWithDatabase(t *testing.T, dir, engine string, found ...runtimes.Runtime) recipe.Resolved {
	t.Helper()
	toml := "[database]\nengine = \"" + engine + "\"\n"
	if engine == "sqlite" {
		toml += "path = \"database/database.sqlite\"\n"
	}
	layer, err := config.ParseLayer("laneway.toml", []byte(toml))
	if err != nil {
		t.Fatal(err)
	}
	res, err := recipe.Resolve(recipe.Input{Engines: firstPartyEngines(), Dir: dir, Layers: []config.Layer{layer}, Runtimes: found})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestAPHPWithoutTheDriverForTheProjectsDatabaseIsARuntimeWarning(t *testing.T) {
	dir := project(t, map[string]string{"composer.json": `{"require": {"php": "^8.2", "laravel/framework": "^12.0"}}`})

	res := resolveWithDatabase(t, dir, "mysql", php("8.5.0", "core", "pdo", "pdo_sqlite"), noNode)

	assertRuntimeWarning(t, res, runtimes.PHP,
		[]string{"pdo_mysql", "mysql", "PHP 8.5.0", "/opt/php/bin/php"},
		[]string{"pdo_mysql"})
}

func TestADatabaseDriverComposerRequiresIsWarnedAboutOnce(t *testing.T) {
	dir := project(t, map[string]string{"composer.json": `{"require": {"php": "^8.2", "ext-pdo_mysql": "*"}}`})

	res := resolveWithDatabase(t, dir, "mysql", php("8.5.0", "core", "pdo"), noNode)

	assertRuntimeWarning(t, res, runtimes.PHP, []string{"ext-pdo_mysql", "composer.json"}, []string{"pdo_mysql"})
}

// envStack is a test Recipe, detected by composer.json, that sets the
// database engine from .env's DB_CONNECTION, as the Laravel Recipe does.
type envStack struct{}

func (envStack) Name() string { return "env-stack" }

func (envStack) Detection() detect.Rule { return detect.Rule{File: "composer.json"} }

func (envStack) Defaults(dir string) (recipe.Result, error) {
	env, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		return recipe.Result{}, err
	}
	engine, _ := dotenv.Parse(env).Get("DB_CONNECTION")
	return layered(config.ParseLayer("the env-stack Recipe", []byte("[database]\nengine = \""+engine+"\"\n")))
}

func TestAPHPWithoutTheDriverForTheDatabaseARecipeSetsIsARuntimeWarning(t *testing.T) {
	dir := project(t, map[string]string{
		"composer.json": `{"require": {"php": "^8.2"}}`,
		".env":          "DB_CONNECTION=mysql\n",
	})

	res, err := recipe.Resolve(recipe.Input{Engines: firstPartyEngines(), Dir: dir, Recipes: []recipe.Recipe{envStack{}},
		Runtimes: []runtimes.Runtime{php("8.5.0", "core", "pdo"), noNode}})
	if err != nil {
		t.Fatal(err)
	}

	assertRuntimeWarning(t, res, runtimes.PHP, []string{"pdo_mysql", "mysql", "PHP 8.5.0"}, []string{"pdo_mysql"})
}

// Each database engine needs its PDO extension, and only a PHP Project is
// checked for it.
func TestEachDatabaseEngineNeedsItsPDOExtension(t *testing.T) {
	tests := []struct {
		engine, driver string
	}{
		{"mysql", "pdo_mysql"},
		{"postgres", "pdo_pgsql"},
		{"sqlite", "pdo_sqlite"},
	}
	composer := map[string]string{"composer.json": `{"require": {"php": "^8.2"}}`}
	for _, tt := range tests {
		t.Run(tt.engine, func(t *testing.T) {
			without := resolveWithDatabase(t, project(t, composer), tt.engine, php("8.4.1", "core", "pdo"), noNode)
			assertRuntimeWarning(t, without, runtimes.PHP, []string{tt.driver, tt.engine}, nil)

			with := resolveWithDatabase(t, project(t, composer), tt.engine, php("8.4.1", "core", "pdo", tt.driver), noNode)
			if len(with.RuntimeWarnings) != 0 {
				t.Errorf("with %s loaded: RuntimeWarnings = %+v, want none", tt.driver, with.RuntimeWarnings)
			}

			notPHP := resolveWithDatabase(t, project(t, nil), tt.engine, php("8.4.1", "core"), noNode)
			if len(notPHP.RuntimeWarnings) != 0 {
				t.Errorf("without composer.json: RuntimeWarnings = %+v, want none", notPHP.RuntimeWarnings)
			}
		})
	}
}
