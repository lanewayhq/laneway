package runtimes

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
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
// files declare: composer.json `require.php` and `ext-*` for PHP, and
// package.json `engines.node` for Node. It only reads files.
func Check(dir string, found []Runtime) []Warning {
	var warnings []Warning
	byName := map[string]Runtime{}
	for _, r := range found {
		byName[r.Name] = r
	}
	var composer composerJSON
	if ok, w := readManifest(dir, "composer.json", &composer); ok {
		warnings = append(warnings, checkPHP(byName[PHP], composer)...)
	} else if w != nil {
		warnings = append(warnings, *w)
	}
	var pkg packageJSON
	if ok, w := readManifest(dir, "package.json", &pkg); ok {
		warnings = append(warnings, checkNode(byName[Node], pkg)...)
	} else if w != nil {
		warnings = append(warnings, *w)
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

func checkPHP(php Runtime, composer composerJSON) []Warning {
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
	return warnings
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

// installHint suggests how to get a Runtime matching constraint with mise.
func installHint(runtime, constraint string) string {
	return fmt.Sprintf("install a matching version with mise: `mise use %s@%s` (Portby never installs Runtimes)",
		runtime, suggestedVersion(constraint))
}

// commandRuntimes are the Runtimes commands belong to, for NotFound.
var commandRuntimes = map[string]string{"php": PHP, "node": Node, "npm": Node, "npx": Node}

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
	if runtime == PHP {
		var composer composerJSON
		if ok, _ := readManifest(dir, "composer.json", &composer); ok {
			constraint = composer.Require["php"]
		}
	} else {
		var pkg packageJSON
		if ok, _ := readManifest(dir, "package.json", &pkg); ok {
			constraint = pkg.Engines["node"]
		}
	}
	return fmt.Errorf("%s; %s", msg, installHint(runtime, constraint))
}
