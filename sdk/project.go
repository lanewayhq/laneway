package sdk

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/lanewayhq/laneway/internal/detect"
	"github.com/lanewayhq/laneway/internal/dotenv"
)

// EnvFile is what the Project's env file sets.
type EnvFile struct{ f *dotenv.File }

// EnvFile reads the Project's .env or, before there is one (Laneway creates
// it from .env.example), its .env.example. With neither, it sets nothing.
func (p Project) EnvFile() EnvFile {
	for _, name := range []string{".env", ".env.example"} {
		if data, err := p.ReadFile(name); err == nil {
			return EnvFile{dotenv.Parse(data)}
		}
	}
	return EnvFile{dotenv.Parse(nil)}
}

// Get is the value the env file gives key, and whether it gives it one.
func (e EnvFile) Get(key string) (string, bool) {
	return e.f.Get(key)
}

// Gems are the gems the Project's Gemfile declares.
func (p Project) Gems() map[string]bool { return detect.Gems(p.Dir) }

// Packages are the packages the Project's package.json depends on, in
// dependencies or devDependencies.
func (p Project) Packages() map[string]bool { return detect.Packages(p.Dir) }

// ComposerPackages are the packages the Project's composer.json requires.
func (p Project) ComposerPackages() map[string]bool { return detect.ComposerPackages(p.Dir) }

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
