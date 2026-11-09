package runtimes

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// composerJSON is the part of composer.json Check reads.
type composerJSON struct {
	Require map[string]string `json:"require"`
}

// packageJSON is the part of package.json Check reads.
type packageJSON struct {
	Engines map[string]string `json:"engines"`
}

// Check compares the Runtimes found for the Project in dir with what its
// files declare: composer.json `require.php` and `ext-*` for PHP,
// package.json `engines.node` for Node, and the Gemfile's `ruby` for Ruby.
// For a PHP Project, it also checks PHP loads the driver for database, the
// Engine of the Project's database (such as pdo_mysql for mysql); "" for
// none. It only reads files.
func Check(dir string, found []Runtime, database string) []Warning {
	var warnings []Warning
	byName := map[string]Runtime{}
	for _, r := range found {
		byName[r.Name] = r
	}
	var composer composerJSON
	if ok, w := readManifest(dir, "composer.json", &composer); ok {
		warnings = append(warnings, checkPHP(byName[PHP], composer, database)...)
	} else if w != nil {
		warnings = append(warnings, *w)
	}
	var pkg packageJSON
	if ok, w := readManifest(dir, "package.json", &pkg); ok {
		warnings = append(warnings, checkNode(byName[Node], pkg)...)
	} else if w != nil {
		warnings = append(warnings, *w)
	}
	if gemfile, ok := readGemfile(dir); ok {
		warnings = append(warnings, checkRuby(byName[Ruby], gemfile)...)
	}
	return warnings
}

// readManifest decodes dir/name into v. It reports whether the file exists
// and could be read, and a warning if it exists but couldn't.
func readManifest(dir, name string, v any) (bool, *Warning) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err == nil {
		err = json.Unmarshal(data, v)
	}
	if err != nil {
		return false, &Warning{Runtime: runtimeOf(name), Message: fmt.Sprintf("can't read %s: %v", name, err)}
	}
	return true, nil
}

func runtimeOf(manifest string) string {
	if manifest == "package.json" {
		return Node
	}
	return PHP
}

func checkPHP(php Runtime, composer composerJSON, database string) []Warning {
	constraint := composer.Require["php"]
	if php.Version == "" && php.Path != "" {
		return []Warning{{Runtime: PHP, Message: fmt.Sprintf("can't run PHP at %s: %s", php.Path, php.Error)}}
	}
	if php.Version == "" {
		return []Warning{{Runtime: PHP,
			Message: "PHP was not found through mise or on PATH, but this Project's composer.json needs it",
			Fix:     installHint(PHP, constraint)}}
	}
	var warnings []Warning
	if constraint != "" {
		ok, err := Satisfies(php.Version, constraint, Composer)
		switch {
		case err != nil:
			warnings = append(warnings, Warning{Runtime: PHP,
				Message: fmt.Sprintf("can't check PHP %s against require.php %q in composer.json: %v", php.Version, constraint, err)})
		case !ok:
			warnings = append(warnings, Warning{Runtime: PHP,
				Message: fmt.Sprintf("PHP %s doesn't satisfy require.php %q in composer.json", php.Version, constraint),
				Fix:     installHint(PHP, constraint)})
		}
	}
	loaded := map[string]bool{}
	for _, ext := range php.Extensions {
		loaded[ext] = true
	}
	var missing []string
	for _, key := range slices.Sorted(maps.Keys(composer.Require)) {
		ext, ok := strings.CutPrefix(strings.ToLower(key), "ext-")
		if ok && !loaded[ext] && !loaded[extensionAliases[ext]] {
			missing = append(missing, ext)
		}
	}
	if len(missing) > 0 {
		warnings = append(warnings, Warning{Runtime: PHP,
			Message: fmt.Sprintf("composer.json requires ext-%s, but PHP %s (%s) doesn't load %s",
				strings.Join(missing, ", ext-"), php.Version, php.Path, strings.Join(missing, ", ")),
			Fix: fmt.Sprintf("install or enable the %s extension(s) for that PHP", strings.Join(missing, ", "))})
	}
	// The ext-* warning already names a driver composer.json requires.
	if driver := databaseDrivers[database]; driver != "" && !loaded[driver] && !slices.Contains(missing, driver) {
		warnings = append(warnings, Warning{Runtime: PHP,
			Message: fmt.Sprintf("the Project's database is %s, but PHP %s (%s) doesn't load %s, the driver PHP needs for it",
				database, php.Version, php.Path, driver),
			Fix: fmt.Sprintf("install or enable the %s extension for that PHP", driver)})
	}
	return warnings
}

// databaseDrivers are the PHP extensions PHP needs to reach a database of
// each Engine.
var databaseDrivers = map[string]string{
	"mysql":    "pdo_mysql",
	"mariadb":  "pdo_mysql",
	"postgres": "pdo_pgsql",
	"sqlite":   "pdo_sqlite",
}

// extensionAliases are other names composer.json uses for an extension.
var extensionAliases = map[string]string{"opcache": "zend-opcache", "zend-opcache": "opcache"}

func checkNode(node Runtime, pkg packageJSON) []Warning {
	constraint := pkg.Engines["node"]
	if node.Version == "" && node.Path != "" {
		return []Warning{{Runtime: Node, Message: fmt.Sprintf("can't run Node at %s: %s", node.Path, node.Error)}}
	}
	if node.Version == "" {
		return []Warning{{Runtime: Node,
			Message: "Node was not found through mise or on PATH, but this Project has a package.json",
			Fix:     installHint(Node, constraint)}}
	}
	if constraint == "" {
		return nil
	}
	ok, err := Satisfies(node.Version, constraint, NPM)
	switch {
	case err != nil:
		return []Warning{{Runtime: Node,
			Message: fmt.Sprintf("can't check Node %s against engines.node %q in package.json: %v", node.Version, constraint, err)}}
	case !ok:
		return []Warning{{Runtime: Node,
			Message: fmt.Sprintf("Node %s doesn't satisfy engines.node %q in package.json", node.Version, constraint),
			Fix:     installHint(Node, constraint)}}
	}
	return nil
}

// gemfileRuby matches the Gemfile's `ruby` line, such as ruby "~> 3.3" or
// ruby file: ".ruby-version".
var gemfileRuby = regexp.MustCompile(`^\s*ruby\b(.*)$`)

// rubyArg is one argument of the Gemfile's `ruby` line: a quoted version
// requirement, or an option such as file: ".ruby-version" or engine: "jruby".
var rubyArg = regexp.MustCompile(`(?:(\w+):\s*)?["']([^"']+)["']`)

// gemfile is the part of a Gemfile Check reads.
type gemfile struct {
	// ruby is the `ruby` line's arguments as written, such as "~> 3.3";
	// "" without one.
	ruby string
	// constraint is what they ask for in Composer's syntax (see
	// rubyConstraint).
	constraint string
}

// readGemfile reads dir's Gemfile, reporting whether there is one.
func readGemfile(dir string) (gemfile, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "Gemfile"))
	if err != nil {
		return gemfile{}, false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		m := gemfileRuby.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var reqs []string
		for _, arg := range rubyArg.FindAllStringSubmatch(m[1], -1) {
			switch arg[1] {
			case "":
				reqs = append(reqs, arg[2])
			case "file":
				// The version in that file, such as .ruby-version.
				if v, err := os.ReadFile(filepath.Join(dir, arg[2])); err == nil {
					reqs = append(reqs, strings.TrimPrefix(strings.TrimSpace(string(v)), "ruby-"))
				}
			}
		}
		return gemfile{ruby: strings.TrimSpace(m[1]), constraint: rubyConstraint(reqs)}, true
	}
	return gemfile{}, true
}

// rubyConstraint is RubyGems version requirements, all of which must hold,
// in Composer's syntax, which reads the same apart from the pessimistic
// operator: RubyGems' ~> 3.3 is Composer's ~3.3 (>=3.3 <4.0), and ~> 3.3.5
// is ~3.3.5 (>=3.3.5 <3.4).
func rubyConstraint(reqs []string) string {
	for i, r := range reqs {
		r = strings.TrimSpace(r)
		if rest, ok := strings.CutPrefix(r, "~>"); ok {
			r = "~" + strings.TrimSpace(rest)
		}
		reqs[i] = strings.ReplaceAll(r, " ", "")
	}
	return strings.Join(reqs, " ")
}

func checkRuby(ruby Runtime, g gemfile) []Warning {
	if ruby.Version == "" && ruby.Path != "" {
		return []Warning{{Runtime: Ruby, Message: fmt.Sprintf("can't run Ruby at %s: %s", ruby.Path, ruby.Error)}}
	}
	if ruby.Version == "" {
		return []Warning{{Runtime: Ruby,
			Message: "Ruby was not found through mise or on PATH, but this Project has a Gemfile",
			Fix:     installHint(Ruby, g.constraint)}}
	}
	if g.constraint == "" {
		return nil
	}
	ok, err := Satisfies(ruby.Version, g.constraint, Composer)
	switch {
	case err != nil:
		return []Warning{{Runtime: Ruby,
			Message: fmt.Sprintf("can't check Ruby %s against ruby %s in the Gemfile: %v", ruby.Version, g.ruby, err)}}
	case !ok:
		return []Warning{{Runtime: Ruby,
			Message: fmt.Sprintf("Ruby %s doesn't satisfy ruby %s in the Gemfile", ruby.Version, g.ruby),
			Fix:     installHint(Ruby, g.constraint)}}
	}
	return nil
}

// installHint suggests how to get a Runtime matching constraint with mise.
func installHint(runtime, constraint string) string {
	return fmt.Sprintf("install a matching version with mise: `mise use %s@%s` (Laneway never installs Runtimes)",
		runtime, suggestedVersion(constraint))
}

// commandRuntimes are the Runtimes commands belong to, for NotFound.
var commandRuntimes = map[string]string{
	"php": PHP, "node": Node, "npm": Node, "npx": Node,
	"ruby": Ruby, "gem": Ruby, "bundle": Ruby, "bundler": Ruby,
}

// NotFound is the error for a command that isn't on the resolved PATH of the
// Project in dir. For a Runtime's own command it says how to install a
// version matching the Project's constraint with mise.
func NotFound(dir, command string) error {
	msg := command + " was not found through mise or on PATH"
	runtime, ok := commandRuntimes[command]
	if !ok {
		return errors.New(msg)
	}
	var constraint string
	switch runtime {
	case PHP:
		var composer composerJSON
		if ok, _ := readManifest(dir, "composer.json", &composer); ok {
			constraint = composer.Require["php"]
		}
	case Node:
		var pkg packageJSON
		if ok, _ := readManifest(dir, "package.json", &pkg); ok {
			constraint = pkg.Engines["node"]
		}
	case Ruby:
		g, _ := readGemfile(dir)
		constraint = g.constraint
	}
	return fmt.Errorf("%s; %s", msg, installHint(runtime, constraint))
}
