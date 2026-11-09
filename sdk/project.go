package sdk

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// EnvFile is what the Project's env file sets.
type EnvFile struct{ values map[string]string }

// EnvFile reads the Project's .env or, before there is one (Laneway creates
// it from .env.example), its .env.example. With neither, it sets nothing.
func (p Project) EnvFile() EnvFile {
	for _, name := range []string{".env", ".env.example"} {
		if data, err := p.ReadFile(name); err == nil {
			return EnvFile{parseEnv(data)}
		}
	}
	return EnvFile{}
}

// Get is the value the env file gives key, and whether it gives it one.
func (e EnvFile) Get(key string) (string, bool) {
	v, ok := e.values[key]
	return v, ok
}

// gemLine matches a Gemfile line declaring a gem: gem "name", ….
var gemLine = regexp.MustCompile(`^\s*gem\s*\(?\s*["']([^"']+)["']`)

// Gems are the gems the Project's Gemfile declares, as a Gemfile detection
// rule reads them; none without a readable Gemfile.
func (p Project) Gems() map[string]bool {
	data, err := p.ReadFile("Gemfile")
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

// Packages are the packages the Project's package.json depends on, in
// dependencies or devDependencies.
func (p Project) Packages() map[string]bool {
	return p.jsonKeys("package.json", "dependencies", "devDependencies")
}

// ComposerPackages are the packages the Project's composer.json requires,
// in require or require-dev.
func (p Project) ComposerPackages() map[string]bool {
	return p.jsonKeys("composer.json", "require", "require-dev")
}

// jsonKeys are the keys of the given objects in the JSON object in the
// Project's file name; none if it isn't readable JSON.
func (p Project) jsonKeys(name string, objects ...string) map[string]bool {
	data, err := p.ReadFile(name)
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

// Rel is file relative to the Project, with forward slashes, and whether it
// is inside the Project: file may be relative to the Project, or absolute on
// the developer's machine, as Laneway writes paths into env files.
func (p Project) Rel(file string) (string, bool) {
	if file == "" {
		return "", false
	}
	if filepath.IsAbs(file) {
		if p.HostDir == "" {
			return "", false
		}
		rel, err := filepath.Rel(p.HostDir, file)
		if err != nil {
			return "", false
		}
		file = rel
	}
	rel := path.Clean(filepath.ToSlash(file))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// MainFileExists reports whether the main Worktree has a file at name, when
// the Project is another Worktree.
func (p Project) MainFileExists(name string) bool {
	if p.MainDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(p.MainDir, name))
	return err == nil
}
