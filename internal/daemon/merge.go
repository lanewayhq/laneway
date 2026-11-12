package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/gitrepo"
)

// Merge status: whether each linked Worktree's work is safe on its Project's
// Default Branch (api.MergeStatus). GET /v1/projects and every `projects`
// event read it, so it is kept, and git is only asked again when it may have
// changed:
//
//   - a Project's Default Branch, when gitrepo.DefaultBranchKey changes;
//   - a Worktree's branch and unmerged commits, when gitrepo.MergeKey (its
//     HEAD, index and the Default Branch's refs) changes;
//   - its changed files, also when they are older than changedFilesMaxAge,
//     because editing a file changes nothing git keeps. That read runs in
//     the background, so the list being sent doesn't wait for it.
//
// Nothing here fetches from a remote.

// changedFilesMaxAge is how old a Worktree's changed files may be before
// they are read again.
const changedFilesMaxAge = 2 * time.Second

// mergeGitTimeout bounds each read of merge status from git.
const mergeGitTimeout = 30 * time.Second

type mergeCache struct {
	mu        sync.Mutex
	branches  map[string]*defaultBranchEntry // by main Worktree dir
	worktrees map[string]*mergeEntry         // by Worktree dir
}

type defaultBranchEntry struct {
	key    string
	branch string
}

type mergeEntry struct {
	mu   sync.Mutex // held while git is read for this Worktree
	read bool       // key and commits have been read
	key  string     // gitrepo.MergeKey when commits were read

	branch     string
	commits    []gitrepo.Commit
	commitsErr error

	changed    []gitrepo.Change
	changedErr error
	changedAt  time.Time
	refreshing bool // a background read of changed is under way
}

// defaultBranch is the Default Branch of the Project whose main Worktree is
// at main, "" if it has none.
func (d *Daemon) defaultBranch(main string) string {
	key := gitrepo.DefaultBranchKey(main)
	d.merges.mu.Lock()
	e := d.merges.branches[main]
	d.merges.mu.Unlock()
	if e != nil && e.key == key {
		return e.branch
	}
	ctx, cancel := context.WithTimeout(d.stopping, mergeGitTimeout)
	defer cancel()
	e = &defaultBranchEntry{key: key, branch: gitrepo.DefaultBranch(ctx, main)}
	d.merges.mu.Lock()
	if d.merges.branches == nil {
		d.merges.branches = map[string]*defaultBranchEntry{}
	}
	d.merges.branches[main] = e
	d.merges.mu.Unlock()
	return e.branch
}

// mergeStatus is the merge status of the linked Worktree at dir, of the
// Project whose main Worktree is at main and whose Default Branch is
// defaultBranch.
func (d *Daemon) mergeStatus(main, defaultBranch, dir string) *api.MergeStatus {
	d.merges.mu.Lock()
	if d.merges.worktrees == nil {
		d.merges.worktrees = map[string]*mergeEntry{}
	}
	e := d.merges.worktrees[dir]
	if e == nil {
		e = &mergeEntry{}
		d.merges.worktrees[dir] = e
	}
	d.merges.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	key := gitrepo.MergeKey(main, dir, defaultBranch)
	switch {
	case !e.read || e.key != key:
		ctx, cancel := context.WithTimeout(d.stopping, mergeGitTimeout)
		defer cancel()
		e.branch = gitrepo.Branch(ctx, dir)
		e.commits, e.commitsErr = gitrepo.UnmergedCommits(ctx, dir, defaultBranch)
		e.changed, e.changedErr = gitrepo.ChangedFiles(ctx, dir)
		e.changedAt = time.Now()
		e.key, e.read = key, true
	case time.Since(e.changedAt) > changedFilesMaxAge && !e.refreshing:
		e.refreshing = true
		d.background(func() {
			ctx, cancel := context.WithTimeout(d.stopping, mergeGitTimeout)
			defer cancel()
			started := time.Now()
			changed, err := gitrepo.ChangedFiles(ctx, dir)
			e.mu.Lock()
			defer e.mu.Unlock()
			e.refreshing = false
			if started.After(e.changedAt) { // not overtaken by a newer read
				e.changed, e.changedErr, e.changedAt = changed, err, started
			}
		})
	}
	s := e.status()
	// Read every time: it is one small file, and locking a Worktree
	// changes nothing MergeKey looks at.
	s.Locked, s.LockReason = gitrepo.Lock(dir)
	return s
}

// status is the API's view of what e last read. The caller holds e.mu.
func (e *mergeEntry) status() *api.MergeStatus {
	s := &api.MergeStatus{Branch: e.branch}
	switch {
	case e.commitsErr != nil:
		s.Error = e.commitsErr.Error()
	case e.changedErr != nil:
		s.Error = e.changedErr.Error()
	}
	s.UnmergedCount = len(e.commits)
	for _, c := range e.commits[:min(len(e.commits), api.MergeListLimit)] {
		s.UnmergedCommits = append(s.UnmergedCommits, api.Commit{Hash: c.Hash, Subject: c.Subject, Time: c.Time})
	}
	s.ChangedCount = len(e.changed)
	for _, c := range e.changed[:min(len(e.changed), api.MergeListLimit)] {
		s.ChangedFiles = append(s.ChangedFiles, api.ChangedFile{Path: c.Path, Status: c.Status})
	}
	if s.Error == "" {
		s.Merged = s.UnmergedCount == 0
		s.Clean = s.ChangedCount == 0
	}
	return s
}

// forgetMerges drops what is kept for Worktrees and Projects that are no
// longer in projects.
func (d *Daemon) forgetMerges(projects []api.Project) {
	mains := map[string]bool{}
	dirs := map[string]bool{}
	for _, p := range projects {
		mains[p.Path] = true
		for _, w := range p.Worktrees {
			dirs[w.Path] = true
		}
	}
	d.merges.mu.Lock()
	defer d.merges.mu.Unlock()
	for main := range d.merges.branches {
		if !mains[main] {
			delete(d.merges.branches, main)
		}
	}
	for dir := range d.merges.worktrees {
		if !dirs[dir] {
			delete(d.merges.worktrees, dir)
		}
	}
}
