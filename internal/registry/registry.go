// Package registry is the daemon's persistent record of Linked Projects and
// their Worktrees. Later tickets extend it with Aliases, Service port
// assignments and Orphans. Every change is written to disk before it is acknowledged, so
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

	"github.com/mrsafalpiya/portby.dev/internal/naming"
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
	Version   int        `json:"version"`
	Projects  []Project  `json:"projects"`
	Worktrees []Worktree `json:"worktrees,omitempty"`
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

// Worktree is a linked (not main) Worktree of a Project, as last discovered
// in git's worktree registry.
type Worktree struct {
	// Project is the owning Project's Path.
	Project string `json:"project"`
	// Name is git's name for the worktree.
	Name string `json:"name"`
	Path string `json:"path"`
	// Label is the Worktree's part of its Domain, unique within the Project.
	// It is assigned once and kept, so a Domain doesn't change when another
	// Worktree with a clashing name comes or goes.
	Label string `json:"label"`
}

// Found is a linked worktree seen in git's worktree registry.
type Found struct {
	Name string
	Path string
}

// Worktrees returns the linked Worktrees of the Project at project, sorted
// by name.
func (r *Registry) Worktrees(project string) []Worktree {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Worktree
	for _, w := range r.state.Worktrees {
		if w.Project == project {
			out = append(out, w)
		}
	}
	slices.SortFunc(out, func(a, b Worktree) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// WorktreeChanges is how a Project's linked Worktrees change to match what
// was found in git's worktree registry. Build it with PlanWorktrees and
// record it with ApplyWorktrees.
type WorktreeChanges struct {
	project string
	next    []Worktree // the Project's Worktrees afterwards
	// Added and Removed are the Worktrees that appear and disappear. A
	// Worktree that moved to another path is in both, with the same label.
	Added, Removed []Worktree
}

// Empty reports whether nothing changes.
func (c WorktreeChanges) Empty() bool { return len(c.Added) == 0 && len(c.Removed) == 0 }

// PlanWorktrees works out how to make found the complete set of linked
// Worktrees of the Project at project. It changes nothing; the caller must
// not plan or apply other changes for the Project until it has applied this
// one.
//
// New Worktrees get their name's DomainLabel, or that label plus a hash of
// their name if another Worktree of the Project already uses it.
func (r *Registry) PlanWorktrees(project string, found []Found) WorktreeChanges {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := WorktreeChanges{project: project}
	foundPath := map[string]string{} // name → path
	for _, f := range found {
		foundPath[f.Name] = f.Path
	}
	known := map[string]bool{}
	taken := map[string]bool{} // labels in use
	for _, w := range r.state.Worktrees {
		path, ok := foundPath[w.Name]
		switch {
		case w.Project != project:
			continue
		case !ok:
			c.Removed = append(c.Removed, w)
			continue
		case path != w.Path:
			c.Removed = append(c.Removed, w)
			w.Path = path
			c.Added = append(c.Added, w)
		}
		c.next = append(c.next, w)
		known[w.Name] = true
		taken[w.Label] = true
	}
	// Names that are already valid labels go first, so "feat-x" keeps its
	// own name when "feat_x" turns up in the same scan.
	fresh := slices.Clone(found)
	slices.SortFunc(fresh, func(a, b Found) int {
		ra, rb := naming.DomainLabel(a.Name) != a.Name, naming.DomainLabel(b.Name) != b.Name
		if ra != rb {
			if ra {
				return 1
			}
			return -1
		}
		return strings.Compare(a.Name, b.Name)
	})
	for _, f := range fresh {
		if known[f.Name] {
			continue
		}
		label := naming.DomainLabel(f.Name)
		for seed := f.Name; taken[label]; seed += "'" {
			label = naming.DomainLabelWithHash(f.Name, seed)
		}
		taken[label] = true
		known[f.Name] = true
		w := Worktree{Project: project, Name: f.Name, Path: f.Path, Label: label}
		c.Added = append(c.Added, w)
		c.next = append(c.next, w)
	}
	return c
}

// ApplyWorktrees records changes planned by PlanWorktrees and persists them.
func (r *Registry) ApplyWorktrees(c WorktreeChanges) error {
	if c.Empty() {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []Worktree
	for _, w := range r.state.Worktrees {
		if w.Project != c.project {
			all = append(all, w)
		}
	}
	next := r.state
	next.Worktrees = append(all, c.next...)
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
