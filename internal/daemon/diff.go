package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/gitrepo"
)

// handleDiff shows one changed file's diff, or one commit's patch, in a
// Worktree (api.DiffResponse), for the Dashboard's Project page. A path is
// only diffed if git lists it as one of the Worktree's changed files right
// now, so nothing else on disk (an ignored .env, a file outside the
// Worktree) can be read through it.
func (d *Daemon) handleDiff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	path, commit := q.Get("path"), q.Get("commit")
	if (path == "") == (commit == "") {
		d.writeError(w, &api.Error{Code: api.CodeBadRequest, Message: "give either path (one of the Worktree's changed files) or commit (a commit's hash)"})
		return
	}
	dir, err := d.worktreeDir(r.Context(), q.Get("dir"), q.Get("worktree"))
	if err != nil {
		d.writeError(w, err)
		return
	}
	_, checkout, err := d.worktreeAt(r.Context(), dir)
	if err != nil {
		d.writeError(w, err)
		return
	}
	res := api.DiffResponse{Worktree: checkout.Dir, Path: path, Commit: commit}
	if path != "" {
		changes, err := gitrepo.ChangedFiles(r.Context(), checkout.Dir)
		if err != nil {
			d.writeError(w, err)
			return
		}
		i := slices.IndexFunc(changes, func(c gitrepo.Change) bool { return c.Path == path })
		if i < 0 {
			d.writeError(w, &api.Error{Code: api.CodeBadRequest,
				Message: fmt.Sprintf("%s has no uncommitted changes in the Worktree at %s (any more)", path, checkout.Dir)})
			return
		}
		res.Diff, res.Truncated, err = gitrepo.FileDiff(r.Context(), checkout.Dir, changes[i], api.DiffLimit)
	} else {
		res.Diff, res.Truncated, err = gitrepo.CommitDiff(r.Context(), checkout.Dir, commit, api.DiffLimit)
		if errors.Is(err, gitrepo.ErrUnknownCommit) {
			err = &api.Error{Code: api.CodeBadRequest, Message: fmt.Sprintf("the Worktree at %s has %v", checkout.Dir, err)}
		}
	}
	if err != nil {
		d.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
