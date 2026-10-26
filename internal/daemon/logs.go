package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/gitrepo"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
)

// worktreeAt finds the Linked Project containing dir, and the Worktree dir is
// in. The Worktree's path is also its Supervisor group.
func (d *Daemon) worktreeAt(ctx context.Context, dir string) (registry.Project, gitrepo.Checkout, error) {
	if !filepath.IsAbs(dir) {
		return registry.Project{}, gitrepo.Checkout{}, &api.Error{Code: api.CodeBadRequest, Message: "dir must be an absolute path"}
	}
	checkout, err := gitrepo.Resolve(ctx, dir)
	if errors.Is(err, gitrepo.ErrNotGitRepo) {
		return registry.Project{}, gitrepo.Checkout{}, &api.Error{
			Code:    api.CodeNotGitRepo,
			Message: fmt.Sprintf("%s is not inside a git repository, so it isn't in any Project", dir),
		}
	}
	if err != nil {
		return registry.Project{}, gitrepo.Checkout{}, err
	}
	p, ok := d.registry.ProjectAt(checkout.MainDir)
	if !ok {
		return registry.Project{}, gitrepo.Checkout{}, &api.Error{
			Code:    api.CodeNotLinked,
			Message: fmt.Sprintf("%s is not Linked; run `portby link` there first", checkout.MainDir),
		}
	}
	return p, checkout, nil
}

// handleLogs streams Process output as one JSON api.LogLine per line.
func (d *Daemon) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	process, follow := q.Get("process"), q.Get("follow") == "true"
	_, checkout, err := d.worktreeAt(r.Context(), q.Get("dir"))
	if err != nil {
		d.writeError(w, err)
		return
	}
	group := checkout.Dir
	if process != "" {
		names := d.processNames(group)
		if !slices.Contains(names, process) {
			d.writeError(w, unknownProcess(process, names))
			return
		}
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	flusher, _ := w.(http.Flusher)
	var after uint64
	for {
		lines, changed := d.supervisor.Output(group, process, after)
		for _, l := range lines {
			if enc.Encode(api.LogLine{Process: l.Process, Time: l.Time, Text: l.Text}) != nil {
				return
			}
			after = l.Seq
		}
		if !follow {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		case <-d.closing:
			return
		}
	}
}

// processNames are the names of the Processes whose output can be asked for:
// those in the Worktree's config, and any others with output kept (such as
// one removed from the config since it ran), sorted. The Worktree's
// directory is its group.
func (d *Daemon) processNames(group string) []string {
	names := d.supervisor.OutputNames(group)
	// A broken config still leaves the kept output readable.
	if cfg, err := recipe.ResolveDir(group); err == nil {
		for _, p := range cfg.Processes {
			names = append(names, p.Name)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func unknownProcess(name string, names []string) error {
	msg := fmt.Sprintf("this Worktree has no Process %q", name)
	if len(names) > 0 {
		msg += "; its Processes are " + strings.Join(names, ", ")
	}
	return &api.Error{Code: api.CodeUnknownProcess, Message: msg}
}
