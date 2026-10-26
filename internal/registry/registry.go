// Package registry is the daemon's persistent record of Linked Projects,
// their Worktrees and their Aliases, and the ports of Services. Later
// tickets extend it with Orphans. Every change is written to disk before it is acknowledged, so
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
	Aliases   []Alias    `json:"aliases,omitempty"`
	Services  []Service  `json:"services,omitempty"`
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
// their name if another Worktree or an Alias of the Project already uses it,
// or it is one of reserved (the names of the main Worktree's HTTP Processes,
// served at <process>.<project>.localhost).
func (r *Registry) PlanWorktrees(project string, found []Found, reserved []string) WorktreeChanges {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := WorktreeChanges{project: project}
	foundPath := map[string]string{} // name → path
	for _, f := range found {
		foundPath[f.Name] = f.Path
	}
	known := map[string]bool{}
	taken := map[string]bool{} // labels in use
	for _, name := range reserved {
		taken[name] = true
	}
	for _, a := range r.state.Aliases {
		if a.Project == project {
			taken[a.Name] = true
		}
	}
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
// The Aliases of Worktrees that are gone are dropped with them.
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
	kept := map[string]bool{"": true} // the main Worktree is always there
	for _, w := range c.next {
		kept[w.Name] = true
	}
	next := r.state
	next.Worktrees = append(all, c.next...)
	next.Aliases = slices.DeleteFunc(slices.Clone(r.state.Aliases), func(a Alias) bool {
		return a.Project == c.project && !kept[a.Worktree]
	})
	if err := r.save(next); err != nil {
		return err
	}
	r.state = next
	return nil
}

// Alias is an extra Domain of a Worktree: <name>.<project>.localhost.
type Alias struct {
	// Project is the owning Project's Path.
	Project string `json:"project"`
	// Worktree is git's name for the Worktree; "" for the main Worktree.
	Worktree string `json:"worktree"`
	// Name is the Alias's part of the Domain, unique within the Project
	// among Aliases and Worktree labels.
	Name string `json:"name"`
}

// ErrNoAlias means the Worktree has no Alias by that name.
var ErrNoAlias = errors.New("no such Alias")

// AliasTakenError means the Project already uses an Alias's name for a
// Worktree's Domain or for another Alias.
type AliasTakenError struct {
	// Worktree is the Worktree using the name; "" for the main Worktree.
	Worktree string
	// IsAlias is set if the name is that Worktree's Alias, rather than its
	// own Domain label.
	IsAlias bool
}

func (e *AliasTakenError) Error() string {
	if e.IsAlias {
		return fmt.Sprintf("the name is already an Alias of Worktree %q", e.Worktree)
	}
	return fmt.Sprintf("the name is already the Domain label of Worktree %q", e.Worktree)
}

// Aliases returns the Aliases of the Project at project, sorted by name.
func (r *Registry) Aliases(project string) []Alias {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Alias
	for _, a := range r.state.Aliases {
		if a.Project == project {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b Alias) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// AddAlias records a and persists it. It returns an *AliasTakenError if the
// Project already uses a.Name for a Worktree label or another Alias. Adding
// an Alias the Worktree already has changes nothing.
func (r *Registry) AddAlias(a Alias) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, w := range r.state.Worktrees {
		if w.Project == a.Project && w.Label == a.Name {
			return &AliasTakenError{Worktree: w.Name}
		}
	}
	for _, existing := range r.state.Aliases {
		switch {
		case existing == a:
			return nil
		case existing.Project == a.Project && existing.Name == a.Name:
			return &AliasTakenError{Worktree: existing.Worktree, IsAlias: true}
		}
	}
	next := r.state
	next.Aliases = append(slices.Clone(r.state.Aliases), a)
	if err := r.save(next); err != nil {
		return err
	}
	r.state = next
	return nil
}

// RemoveAlias forgets a and persists the change. It returns ErrNoAlias if
// the Worktree doesn't have it.
func (r *Registry) RemoveAlias(a Alias) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := slices.Index(r.state.Aliases, a)
	if i < 0 {
		return ErrNoAlias
	}
	next := r.state
	next.Aliases = slices.Delete(slices.Clone(r.state.Aliases), i, i+1)
	if err := r.save(next); err != nil {
		return err
	}
	r.state = next
	return nil
}

// Service is a Service and the port Portby listens on for it. Once a
// Service has a port it keeps it, so connection settings never change.
type Service struct {
	// Engine and Version name the Service, such as redis and "7".
	Engine  string `json:"engine"`
	Version string `json:"version"`
	Port    int    `json:"port"`
}

// ErrNoServicePort means every candidate port is taken by other Services.
var ErrNoServicePort = errors.New("no free port for the Service")

// Services returns every Service that has a port, sorted by engine and
// version.
func (r *Registry) Services() []Service {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.state.Services)
	slices.SortFunc(out, func(a, b Service) int {
		return strings.Compare(a.Engine+":"+a.Version, b.Engine+":"+b.Version)
	})
	return out
}

// PlanService returns the Service with the port it has, or else the first of
// candidates no other Service has. It changes nothing.
func (r *Registry) PlanService(engine, version string, candidates []int) (Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	svc, _, err := r.planServiceLocked(engine, version, candidates)
	return svc, err
}

// AddService is PlanService, recording the port the first time.
func (r *Registry) AddService(engine, version string, candidates []int) (Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	svc, known, err := r.planServiceLocked(engine, version, candidates)
	if err != nil || known {
		return svc, err
	}
	next := r.state
	next.Services = append(slices.Clone(r.state.Services), svc)
	if err := r.save(next); err != nil {
		return Service{}, err
	}
	r.state = next
	return svc, nil
}

func (r *Registry) planServiceLocked(engine, version string, candidates []int) (Service, bool, error) {
	taken := map[int]bool{}
	for _, s := range r.state.Services {
		if s.Engine == engine && s.Version == version {
			return s, true, nil
		}
		taken[s.Port] = true
	}
	for _, port := range candidates {
		if !taken[port] {
			return Service{Engine: engine, Version: version, Port: port}, false, nil
		}
	}
	return Service{}, false, fmt.Errorf("%s:%s: %w", engine, version, ErrNoServicePort)
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
