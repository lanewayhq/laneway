// Package detect is detection rules as data: how a Recipe or Integration
// says which Projects it recognises, declared rather than coded, so that
// Laneway can match a Project against Plugins it hasn't fetched yet
// (ADR-0007). It reads the Project's files only.
package detect

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Rule is a detection rule: plain data, as Plugin manifests and the Catalog
// declare it. Exactly one of its fields is set; a rule with none, or an
// empty All or Any, matches no Project.
type Rule struct {
	// File matches when this file, relative to the Project directory,
	// exists.
	File string `json:"file,omitempty"`
	// Composer matches when the Project's composer.json requires this
	// package, in require or require-dev.
	Composer string `json:"composer,omitempty"`
	// Gem matches when the Project's Gemfile declares this gem.
	Gem string `json:"gem,omitempty"`
	// Package matches when the Project's package.json depends on this
	// package, in dependencies or devDependencies.
	Package string `json:"package,omitempty"`
	// All matches when every one of these rules matches.
	All []Rule `json:"all,omitempty"`
	// Any matches when one of these rules matches.
	Any []Rule `json:"any,omitempty"`
}

// Match reports whether the Project in dir matches r and, if it does, the
// rule that matched, described for people, such as "artisan exists".
func (r Rule) Match(dir string) (string, bool) {
	switch {
	case r.File != "":
		fi, err := os.Stat(filepath.Join(dir, r.File))
		if err != nil || !fi.Mode().IsRegular() {
			return "", false
		}
		return r.File + " exists", true
	case r.Composer != "":
		if !ComposerPackages(dir)[r.Composer] {
			return "", false
		}
		return "composer.json requires " + r.Composer, true
	case r.Gem != "":
		if !Gems(dir)[r.Gem] {
			return "", false
		}
		return "Gemfile declares " + r.Gem, true
	case r.Package != "":
		if !Packages(dir)[r.Package] {
			return "", false
		}
		return "package.json depends on " + r.Package, true
	case len(r.All) > 0:
		var matched []string
		for _, rule := range r.All {
			m, ok := rule.Match(dir)
			if !ok {
				return "", false
			}
			matched = append(matched, m)
		}
		return strings.Join(matched, " and "), true
	case len(r.Any) > 0:
		// The first that matches, in the order declared, is the one named.
		for _, rule := range r.Any {
			if m, ok := rule.Match(dir); ok {
				return m, true
			}
		}
	}
	return "", false
}

// gemLine matches a Gemfile line declaring a gem: gem "name", ….
var gemLine = regexp.MustCompile(`^\s*gem\s*\(?\s*["']([^"']+)["']`)

// Gems are the gems the Gemfile in dir declares; none without a readable
// Gemfile.
func Gems(dir string) map[string]bool {
	data, err := os.ReadFile(filepath.Join(dir, "Gemfile"))
	if err != nil {
		return nil
	}
	gems := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if m := gemLine.FindStringSubmatch(sc.Text()); m != nil {
			gems[m[1]] = true
		}
	}
	return gems
}

// ComposerPackages are the packages the composer.json in dir requires, in
// require or require-dev; none without a readable composer.json.
func ComposerPackages(dir string) map[string]bool {
	return jsonKeys(filepath.Join(dir, "composer.json"), "require", "require-dev")
}

// Packages are the packages the package.json in dir depends on, in
// dependencies or devDependencies; none without a readable package.json.
func Packages(dir string) map[string]bool {
	return jsonKeys(filepath.Join(dir, "package.json"), "dependencies", "devDependencies")
}

// jsonKeys are the keys of the given objects in the JSON object in file;
// none if it isn't readable JSON.
func jsonKeys(file string, objects ...string) map[string]bool {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	keys := map[string]bool{}
	for _, name := range objects {
		var object map[string]json.RawMessage
		if json.Unmarshal(doc[name], &object) != nil {
			continue
		}
		for key := range object {
			keys[key] = true
		}
	}
	return keys
}

// Validate fails unless r and every rule in it sets exactly one field, with
// All and Any not empty: a malformed rule would otherwise silently detect
// nothing. Errors name the rule at fault by its path, such as
// "detect.all[1]".
func (r Rule) Validate() error {
	return r.validate("detect")
}

func (r Rule) validate(path string) error {
	var set []string
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"file", r.File != ""}, {"composer", r.Composer != ""}, {"gem", r.Gem != ""}, {"package", r.Package != ""},
		{"all", r.All != nil}, {"any", r.Any != nil},
	} {
		if f.set {
			set = append(set, f.name)
		}
	}
	if len(set) != 1 {
		what := "sets none of them"
		if len(set) > 1 {
			what = "sets " + strings.Join(set, " and ")
		}
		return fmt.Errorf("%s: a detection rule sets exactly one of file, composer, gem, package, all or any, but this one %s", path, what)
	}
	for _, list := range []struct {
		name  string
		rules []Rule
	}{{"all", r.All}, {"any", r.Any}} {
		if list.rules == nil {
			continue
		}
		if len(list.rules) == 0 {
			return fmt.Errorf("%s.%s is empty, so it would detect no Project; list at least one rule", path, list.name)
		}
		for i, rule := range list.rules {
			if err := rule.validate(fmt.Sprintf("%s.%s[%d]", path, list.name, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// String describes r for people, the way Match describes what matched, such
// as "artisan exists and composer.json requires laravel/framework".
func (r Rule) String() string {
	switch {
	case r.File != "":
		return r.File + " exists"
	case r.Composer != "":
		return "composer.json requires " + r.Composer
	case r.Gem != "":
		return "Gemfile declares " + r.Gem
	case r.Package != "":
		return "package.json depends on " + r.Package
	case len(r.All) > 0:
		return join(r.All, " and ")
	case len(r.Any) > 0:
		return join(r.Any, " or ")
	}
	return "nothing"
}

// join describes rules joined by sep, bracketing the ones made of several.
func join(rules []Rule, sep string) string {
	parts := make([]string, len(rules))
	for i, rule := range rules {
		parts[i] = rule.String()
		if len(rule.All)+len(rule.Any) > 1 {
			parts[i] = "(" + parts[i] + ")"
		}
	}
	return strings.Join(parts, sep)
}
