package daemon

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/gitrepo"
	"github.com/lanewayhq/laneway/internal/registry"
)

// Removing a linked Worktree (POST /v1/worktree/remove). Laneway only runs
// git: `git worktree remove`, then `git branch -D` when the branch is merged
// into the Default Branch or the developer asked. Git's worktree registry
// stays the record (ADR-0004), so syncWorktrees then finds the Worktree gone
// and does what it does whenever one goes: its Processes stop, its Worktree
// removed hooks run, and its databases and Buckets become Orphans.
//
// The developer sees a preview first, and the call that removes it sends
// back what the preview showed. It is checked against git again, read in
// full, so nothing is lost that the developer didn't see.

// removeWorktree previews, or carries out, the removal req asks for.
func (d *Daemon) removeWorktree(ctx context.Context, req api.RemoveRequest) (api.RemoveResponse, error) {
	dir, err := d.worktreeDir(ctx, req.Dir, req.Worktree)
	if err != nil {
		return api.RemoveResponse{}, err
	}
	p, checkout, err := d.worktreeAt(ctx, dir)
	if err != nil {
		return api.RemoveResponse{}, err
	}
	if checkout.Main() {
		return api.RemoveResponse{}, &api.Error{
			Code: api.CodeMainWorktree,
			Message: fmt.Sprintf("the main Worktree of %q can't be Removed; run `lane unlink` there to Unlink the Project instead",
				p.Name),
		}
	}
	w, err := d.linkedWorktree(ctx, p, checkout.Dir)
	if err != nil {
		return api.RemoveResponse{}, err
	}
	res, err := d.removal(ctx, p, w)
	if err != nil || req.Preview {
		return res, err
	}

	if res.Locked {
		msg := fmt.Sprintf("Worktree %q is locked", w.Name)
		if res.LockReason != "" {
			msg += ": " + res.LockReason
		}
		return res, &api.Error{Code: api.CodeWorktreeLocked,
			Message: msg + "; run `git worktree unlock` on it first if it can go"}
	}
	if changed := changedSince(req, res); changed != "" {
		return res, &api.Error{Code: api.CodeWorktreeChanged,
			Message: fmt.Sprintf("Worktree %q changed since the preview (%s), so nothing was removed; look at it again", w.Name, changed)}
	}
	if err := gitrepo.RemoveWorktree(ctx, p.Path, w.Path, len(res.ChangedFiles) > 0); err != nil {
		return res, err
	}
	res.Removed = true
	d.log.Info("Worktree removed through Laneway", "project", p.Name, "worktree", w.Name, "path", w.Path)
	// The Default Branch itself is never deleted, whatever it is checked
	// out as.
	if res.Branch != "" && res.Branch != res.DefaultBranch && (res.Merged || req.DeleteBranch) {
		if err := gitrepo.DeleteBranch(ctx, p.Path, res.Branch); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("the branch %s was kept: %v", res.Branch, err))
		} else {
			res.BranchDeleted = true
		}
	}
	// Now rather than when the Worktree Watcher notices, so the Worktree
	// is gone from the API when this returns. A client hanging up doesn't
	// leave it half done.
	d.syncWorktrees(context.WithoutCancel(ctx), p)
	return res, nil
}

// removeWorktrees carries out each removal req asks for, one at a time, as
// removeWorktree would, and says how each went. One that is refused, or
// fails, doesn't stop the rest.
func (d *Daemon) removeWorktrees(ctx context.Context, req api.RemoveAllRequest) api.RemoveAllResponse {
	res := api.RemoveAllResponse{Results: []api.RemoveResult{}}
	for _, one := range req.Worktrees {
		r, err := d.removeWorktree(ctx, one)
		result := api.RemoveResult{RemoveResponse: r}
		if err != nil {
			result.Error = d.apiError(err)
		}
		res.Results = append(res.Results, result)
	}
	return res
}

// linkedWorktree is the Project p's linked Worktree at dir, as recorded.
func (d *Daemon) linkedWorktree(ctx context.Context, p registry.Project, dir string) (registry.Worktree, error) {
	find := func() (registry.Worktree, bool) {
		for _, w := range d.registry.Worktrees(p.Path) {
			if w.Path == dir {
				return w, true
			}
		}
		return registry.Worktree{}, false
	}
	if w, ok := find(); ok {
		return w, nil
	}
	// One git has but the Worktree Watcher hasn't reported yet.
	d.syncWorktrees(ctx, p)
	if w, ok := find(); ok {
		return w, nil
	}
	return registry.Worktree{}, &api.Error{
		Code:    api.CodeUnknownWorktree,
		Message: fmt.Sprintf("git doesn't list %s as a Worktree of %q (yet)", dir, p.Name),
	}
}

// removal is what removing the linked Worktree w of p would lose, read from
// git now, in full.
func (d *Daemon) removal(ctx context.Context, p registry.Project, w registry.Worktree) (api.RemoveResponse, error) {
	res := api.RemoveResponse{
		Project:         p.Name,
		DefaultBranch:   gitrepo.DefaultBranch(ctx, p.Path),
		Branch:          gitrepo.Branch(ctx, w.Path),
		UnmergedCommits: []api.Commit{},
		ChangedFiles:    []api.ChangedFile{},
		Domains:         []string{},
		Orphans:         []api.Orphan{},
	}
	commits, err := gitrepo.UnmergedCommits(ctx, w.Path, res.DefaultBranch)
	if err != nil {
		return api.RemoveResponse{}, fmt.Errorf("checking Worktree %q against the Default Branch: %w", w.Name, err)
	}
	for _, c := range commits {
		res.UnmergedCommits = append(res.UnmergedCommits, api.Commit{Hash: c.Hash, Subject: c.Subject, Time: c.Time})
	}
	changes, err := gitrepo.ChangedFiles(ctx, w.Path)
	if err != nil {
		return api.RemoveResponse{}, err
	}
	for _, c := range changes {
		res.ChangedFiles = append(res.ChangedFiles, api.ChangedFile{Path: c.Path, Status: c.Status})
	}
	res.Merged, res.Clean = len(commits) == 0, len(changes) == 0

	list, err := gitrepo.Worktrees(ctx, p.Path)
	if err != nil {
		return api.RemoveResponse{}, err
	}
	if i := slices.IndexFunc(list, func(g gitrepo.Worktree) bool { return g.Dir == w.Path }); i >= 0 {
		res.Locked, res.LockReason = list[i].Locked, list[i].LockReason
	}

	for _, aw := range d.apiProject(p).Worktrees {
		if aw.Path == w.Path {
			res.Worktree = aw
		}
	}
	if primary := res.Worktree.PrimaryDomain; primary != "" {
		res.Domains = append(res.Domains, primary)
		var others []string
		for _, domain := range append([]string{res.Worktree.Domain}, res.Worktree.Aliases...) {
			if domain != primary {
				others = append(others, domain)
			}
		}
		slices.Sort(others)
		res.Domains = append(res.Domains, others...)
	}

	// As orphanWorktree will work them out once the checkout is gone: from
	// the main Worktree's config.
	cfg, err := d.plugins.ResolveDir(p.Path)
	if err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("Laneway can't tell which databases and Buckets become Orphans: %v", err))
		return res, nil
	}
	for _, o := range d.worktreeOrphans(p, linkedWorktreeRef(p, w), cfg, d.cfg.Clock.Now()) {
		res.Orphans = append(res.Orphans, apiOrphan(o))
	}
	return res, nil
}

// changedSince says how the Worktree, as it is now (res), differs from the
// preview the request req confirms, or "" if it doesn't.
func changedSince(req api.RemoveRequest, res api.RemoveResponse) string {
	var diffs []string
	if req.Branch != res.Branch {
		now := "on no branch"
		if res.Branch != "" {
			now = "on the branch " + res.Branch
		}
		diffs = append(diffs, "it is now "+now)
	}
	var commits []string
	for _, c := range res.UnmergedCommits {
		commits = append(commits, c.Hash)
	}
	if !sameSet(req.UnmergedCommits, commits) {
		diffs = append(diffs, fmt.Sprintf("its unmerged commits differ: %d now", len(commits)))
	}
	var files []string
	for _, f := range res.ChangedFiles {
		files = append(files, f.Path)
	}
	if !sameSet(req.ChangedFiles, files) {
		diffs = append(diffs, fmt.Sprintf("its changed files differ: %d now", len(files)))
	}
	return strings.Join(diffs, "; ")
}

// sameSet reports whether a and b hold the same strings, in any order.
func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}
