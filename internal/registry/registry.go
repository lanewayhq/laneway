// Package registry is the daemon's persistent record of Linked Projects. Later
// tickets extend it with Worktrees, Aliases, Service port assignments and
// Orphans. Every change is written to disk before it is acknowledged, so
// registrations survive daemon restarts.
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Project is a Linked git repository.
type Project struct {
	Name string `json:"name"`
	// Path is the main Worktree's directory, which identifies the repository.
	Path string `json:"path"`
}

var (
	// ErrNameTaken means another repository is already Linked under the name.
	ErrNameTaken = errors.New("project name is already taken")
	// ErrAlreadyLinked means the repository is already a Project.
	ErrAlreadyLinked = errors.New("repository is already linked")
)

// Registry is safe for concurrent use.
type Registry struct {
	path string

	mu    sync.Mutex
	state state
}

// state is the on-disk format. Version lets later tickets migrate it.
type state struct {
	Version  int       `json:"version"`
	Projects []Project `json:"projects"`
}

const currentVersion = 1

// Open loads the Registry stored at path, or starts an empty one if the file
// doesn't exist yet.
func Open(path string) (*Registry, error) {
	r := &Registry{path: path, state: state{Version: currentVersion}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading registry: %w", err)
	}
	if err := json.Unmarshal(data, &r.state); err != nil {
		return nil, fmt.Errorf("reading registry %s: %w", path, err)
	}
	return r, nil
}

// Projects returns every Project, sorted by name.
func (r *Registry) Projects() []Project {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.state.Projects)
	slices.SortFunc(out, func(a, b Project) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// ProjectAt returns the Project whose main Worktree is at path.
func (r *Registry) ProjectAt(path string) (Project, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.state.Projects {
		if p.Path == path {
			return p, true
		}
	}
	return Project{}, false
}

// AddProject records a newly Linked Project and persists the change. It
// returns ErrAlreadyLinked if a Project already has p's path, and ErrNameTaken
// if one already has p's name.
func (r *Registry) AddProject(p Project) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.state.Projects {
		if existing.Path == p.Path {
			return ErrAlreadyLinked
		}
		if existing.Name == p.Name {
			return ErrNameTaken
		}
	}
	next := r.state
	next.Projects = append(slices.Clone(r.state.Projects), p)
	if err := r.save(next); err != nil {
		return err
	}
	r.state = next
	return nil
}

// save writes s atomically: to a temp file in the same directory, then renamed
// over the real one.
func (r *Registry) save(s state) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("saving registry: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".registry-*.json")
	if err != nil {
		return fmt.Errorf("saving registry: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("saving registry: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("saving registry: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("saving registry: %w", err)
	}
	if err := os.Rename(tmp.Name(), r.path); err != nil {
		return fmt.Errorf("saving registry: %w", err)
	}
	return nil
}
