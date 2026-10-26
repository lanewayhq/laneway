// Package gitfixture builds real git repositories and worktrees in temporary
// directories for tests. Every repository is created with an initial commit so
// worktrees can be added straight away.
package gitfixture

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is a real git repository whose main Worktree lives at Dir.
type Repo struct {
	t   testing.TB
	Dir string
}

// Worktree is a linked git worktree of a Repo. Name is git's name for it.
type Worktree struct {
	Repo *Repo
	Name string
	Dir  string
}

// New creates a git repository in a fresh temporary directory named name, on
// branch main, with one commit.
func New(t testing.TB, name string) *Repo {
	t.Helper()
	dir := filepath.Join(TempDir(t), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("gitfixture: %v", err)
	}
	r := &Repo{t: t, Dir: dir}
	r.Git("init", "--quiet", "--initial-branch=main")
	r.WriteFile("README.md", "# "+name+"\n")
	r.Commit("initial commit")
	return r
}

// NotARepo creates a plain directory named name that is not inside any git
// repository.
func NotARepo(t testing.TB, name string) string {
	t.Helper()
	dir := filepath.Join(TempDir(t), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("gitfixture: %v", err)
	}
	return dir
}

// WriteFile writes content to the path rel inside the main Worktree, creating
// parent directories as needed. It does not commit.
func (r *Repo) WriteFile(rel, content string) *Repo {
	r.t.Helper()
	writeFile(r.t, filepath.Join(r.Dir, rel), content)
	return r
}

// Commit stages everything in the main Worktree and commits it.
func (r *Repo) Commit(msg string) *Repo {
	r.t.Helper()
	r.Git("add", "--all")
	r.Git("commit", "--quiet", "--allow-empty", "-m", msg)
	return r
}

// AddWorktree runs `git worktree add` to create a linked worktree named name on
// a new branch of the same name, in a temporary directory outside the repo.
func (r *Repo) AddWorktree(name string) *Worktree {
	r.t.Helper()
	dir := filepath.Join(TempDir(r.t), name)
	r.Git("worktree", "add", "--quiet", "-b", name, dir)
	return &Worktree{Repo: r, Name: name, Dir: dir}
}

// Git runs git with args in the main Worktree and returns its trimmed output.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	return Git(r.t, r.Dir, args...)
}

// WriteFile writes content to the path rel inside the worktree.
func (w *Worktree) WriteFile(rel, content string) *Worktree {
	w.Repo.t.Helper()
	writeFile(w.Repo.t, filepath.Join(w.Dir, rel), content)
	return w
}

// Git runs git with args in dir, isolated from the user's git config, and
// fails the test if it exits non-zero.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Portby Test",
		"GIT_AUTHOR_EMAIL=test@portby.invalid",
		"GIT_COMMITTER_NAME=Portby Test",
		"GIT_COMMITTER_EMAIL=test@portby.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gitfixture: git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TempDir returns a test temporary directory with symlinks resolved, so paths
// compare equal to the ones git reports.
func TempDir(t testing.TB) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("gitfixture: %v", err)
	}
	return dir
}

func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("gitfixture: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("gitfixture: %v", err)
	}
}
