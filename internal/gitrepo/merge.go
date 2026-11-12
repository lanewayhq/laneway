package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Merge status: whether a Worktree's work is safe on its Project's Default
// Branch. Everything here reads the repository as it is on disk; nothing
// fetches from a remote.

// remote is the remote whose HEAD names the Default Branch and whose copy of
// it, as last fetched, counts as merged.
const remote = "origin"

// ErrNoDefaultBranch is returned when a repository's Default Branch can't be
// found: its remote has no HEAD and its main Worktree isn't on a branch.
var ErrNoDefaultBranch = errors.New("no Default Branch: the origin remote names no HEAD and the main Worktree isn't on a branch")

// DefaultBranch is the Default Branch of the repository whose main Worktree
// is at mainDir: the branch refs/remotes/origin/HEAD names, else the branch
// checked out in the main Worktree. It is "" when neither names one.
func DefaultBranch(ctx context.Context, mainDir string) string {
	if ref, ok := symbolicRef(ctx, mainDir, "refs/remotes/"+remote+"/HEAD"); ok {
		if b, ok := strings.CutPrefix(ref, "refs/remotes/"+remote+"/"); ok && b != "" {
			return b
		}
	}
	if ref, ok := symbolicRef(ctx, mainDir, "HEAD"); ok {
		if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			return b
		}
	}
	return ""
}

// Branch is the branch checked out in the Worktree at dir, or "" when it
// isn't on one.
func Branch(ctx context.Context, dir string) string {
	ref, ok := symbolicRef(ctx, dir, "HEAD")
	if !ok {
		return ""
	}
	b, _ := strings.CutPrefix(ref, "refs/heads/")
	return b
}

// symbolicRef is the ref name points to, false if it doesn't exist or isn't
// symbolic.
func symbolicRef(ctx context.Context, dir, name string) (string, bool) {
	out, err := gitCommand(ctx, dir, "symbolic-ref", "--quiet", name).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// Commit is a commit as merge status lists it.
type Commit struct {
	Hash    string
	Subject string
	Time    time.Time
}

// UnmergedCommits are the commits on the HEAD of the Worktree at dir that are
// on neither the local Default Branch, defaultBranch, nor the remote one as
// last fetched, newest first. A commit whose patch is already on one of them
// (a rebase merge) counts as merged; merge commits are left out. It fails
// with ErrNoDefaultBranch when neither branch exists.
func UnmergedCommits(ctx context.Context, dir, defaultBranch string) ([]Commit, error) {
	bases, err := defaultBranchRefs(ctx, dir, defaultBranch)
	if err != nil {
		return nil, err
	}
	// A commit is unmerged when it is unmerged against every base: one
	// base having it, or its patch, is enough.
	var unmerged []Commit
	for i, base := range bases {
		commits, err := notOn(ctx, dir, base)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			unmerged = commits
			continue
		}
		unmerged = slices.DeleteFunc(unmerged, func(c Commit) bool {
			return !slices.ContainsFunc(commits, func(o Commit) bool { return o.Hash == c.Hash })
		})
	}
	return unmerged, nil
}

// defaultBranchRefs are the refs of the local and remote Default Branch that
// exist.
func defaultBranchRefs(ctx context.Context, dir, defaultBranch string) ([]string, error) {
	if defaultBranch == "" {
		return nil, ErrNoDefaultBranch
	}
	want := []string{"refs/heads/" + defaultBranch, "refs/remotes/" + remote + "/" + defaultBranch}
	out, err := run(ctx, dir, append([]string{"for-each-ref", "--format=%(refname)"}, want...)...)
	if err != nil {
		return nil, err
	}
	// for-each-ref matches by prefix, so refs/heads/main also matches
	// refs/heads/main/x.
	var refs []string
	for _, ref := range strings.Split(out, "\n") {
		if slices.Contains(want, ref) {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("the Default Branch %s doesn't exist, locally or at %s", defaultBranch, remote)
	}
	return refs, nil
}

// notOn are the commits on HEAD in dir that neither base nor a commit on base
// with the same patch has, newest first, leaving out merge commits.
func notOn(ctx context.Context, dir, base string) ([]Commit, error) {
	out, err := run(ctx, dir, "log", "--cherry-mark", "--right-only", "--no-merges",
		"--format=%m%x00%H%x00%ct%x00%s", base+"...HEAD", "--")
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\x00", 4)
		if len(f) != 4 || f[0] == "=" {
			continue // "=": the same patch is on base
		}
		secs, _ := strconv.ParseInt(f[2], 10, 64)
		commits = append(commits, Commit{Hash: f[1], Subject: f[3], Time: time.Unix(secs, 0).UTC()})
	}
	return commits, nil
}

// Change is a file with uncommitted changes in a Worktree.
type Change struct {
	// Path is relative to the Worktree's top directory; an untracked
	// directory ends in "/".
	Path string
	// Status is git's two-letter short status, such as " M" or "??".
	Status string
}

// ChangedFiles are the files with uncommitted changes in the Worktree at dir,
// staged or not, and its untracked files (an untracked directory is one
// entry), sorted by path. Ignored files aren't listed.
func ChangedFiles(ctx context.Context, dir string) ([]Change, error) {
	// --no-optional-locks: don't refresh the index, which would change
	// what MergeKey sees and look like a new change.
	out, err := run(ctx, dir, "--no-optional-locks", "status", "--porcelain=v1", "-z")
	if err != nil {
		return nil, err
	}
	var changes []Change
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		status := f[:2]
		changes = append(changes, Change{Path: f[3:], Status: status})
		if status[0] == 'R' || status[0] == 'C' {
			i++ // the path it was renamed or copied from
		}
	}
	slices.SortFunc(changes, func(a, b Change) int { return strings.Compare(a.Path, b.Path) })
	return changes, nil
}

// MergeKey is a fingerprint of everything the merge status of the Worktree at
// dir, in the repository whose main Worktree is at mainDir, is computed from
// except its working files: its HEAD and index, the branch it is on, the
// Default Branch's local and remote refs, and what names the Default Branch.
// It only reads file metadata, so it is cheap to compare often; when it
// changes, the merge status may have.
func MergeKey(mainDir, dir, defaultBranch string) string {
	common := filepath.Join(mainDir, ".git")
	gitDir := common
	if dir != mainDir {
		if gd, ok := linkedGitDir(dir); ok {
			gitDir = gd
		}
	}
	var b strings.Builder
	head, _ := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	b.Write(head)
	paths := []string{
		filepath.Join(gitDir, "HEAD"),
		filepath.Join(gitDir, "index"),
		filepath.Join(common, "HEAD"),
		filepath.Join(common, "packed-refs"),
		filepath.Join(common, "reftable", "tables.list"),
		filepath.Join(common, "refs", "remotes", remote, "HEAD"),
	}
	if ref, ok := bytes.CutPrefix(bytes.TrimSpace(head), []byte("ref: ")); ok {
		paths = append(paths, filepath.Join(common, filepath.FromSlash(string(ref))))
	}
	if defaultBranch != "" {
		paths = append(paths,
			filepath.Join(common, "refs", "heads", filepath.FromSlash(defaultBranch)),
			filepath.Join(common, "refs", "remotes", remote, filepath.FromSlash(defaultBranch)))
	}
	for _, p := range paths {
		b.WriteString("\x00")
		b.WriteString(statKey(p))
	}
	return b.String()
}

// DefaultBranchKey is a fingerprint of what names the Default Branch of the
// repository whose main Worktree is at mainDir: when it changes,
// DefaultBranch may give another answer.
func DefaultBranchKey(mainDir string) string {
	common := filepath.Join(mainDir, ".git")
	var b strings.Builder
	for _, p := range []string{
		filepath.Join(common, "HEAD"),
		filepath.Join(common, "refs", "remotes", remote, "HEAD"),
		filepath.Join(common, "packed-refs"),
		filepath.Join(common, "reftable", "tables.list"),
	} {
		b.WriteString(statKey(p))
		b.WriteString("\x00")
	}
	return b.String()
}

// statKey identifies the version of the file at path: git replaces the files
// it changes, so a new version has a new inode or modification time.
func statKey(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "-"
	}
	var ino uint64
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		ino = uint64(st.Ino)
	}
	return fmt.Sprintf("%d:%d:%d", fi.ModTime().UnixNano(), fi.Size(), ino)
}

// linkedGitDir is the git dir of the linked Worktree whose root is dir, read
// from its .git file.
func linkedGitDir(dir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil {
		return "", false
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return "", false
	}
	return relativeTo(dir, strings.TrimSpace(gitDir)), true
}

// run runs git in dir and returns its output with the trailing newline
// removed.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := gitCommand(ctx, dir, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s in %s: %v: %s", args[0], dir, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(string(out), "\n"), nil
}
