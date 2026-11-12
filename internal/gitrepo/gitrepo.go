// Package gitrepo answers questions about git checkouts on disk by running the
// git binary.
package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNotGitRepo is returned when a directory isn't inside a git repository.
var ErrNotGitRepo = errors.New("not a git repository")

// Checkout describes the Worktree that contains a directory, and the
// repository that owns it.
type Checkout struct {
	// MainDir is the main Worktree's directory. It identifies the repository
	// no matter which of its Worktrees the directory was in.
	MainDir string
	// Dir is the top-level directory of the Worktree containing the directory.
	Dir string
	// Name is git's name for a linked worktree (the directory name under
	// .git/worktrees/). It is empty for the main Worktree.
	Name string
}

// Main reports whether the checkout is the repository's main Worktree.
func (c Checkout) Main() bool { return c.Dir == c.MainDir }

// Resolve finds the Worktree containing dir and the repository that owns it,
// through git's common directory.
func Resolve(ctx context.Context, dir string) (Checkout, error) {
	cmd := gitCommand(ctx, dir, "rev-parse", "--path-format=absolute",
		"--show-toplevel", "--git-dir", "--git-common-dir")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "not a git repository") {
			return Checkout{}, ErrNotGitRepo
		}
		return Checkout{}, fmt.Errorf("git rev-parse in %s: %v: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 {
		return Checkout{}, fmt.Errorf("git rev-parse in %s: unexpected output %q", dir, out)
	}
	top, gitDir, commonDir := lines[0], lines[1], lines[2]

	if filepath.Base(commonDir) != ".git" {
		return Checkout{}, fmt.Errorf("%s: bare repositories and separate git directories are not supported", dir)
	}
	c := Checkout{MainDir: filepath.Dir(commonDir), Dir: top}
	if gitDir != commonDir {
		c.Name = filepath.Base(gitDir)
	}
	return c, nil
}

// Worktree is one entry in a repository's git worktree registry.
type Worktree struct {
	// Name is git's name for a linked worktree; empty for the main Worktree.
	Name string
	Dir  string
	Main bool
	// Gone means git still lists the worktree but its directory (or the
	// directory's .git file) no longer exists: it was deleted by hand.
	Gone bool
	// Initializing means `git worktree add` is still checking it out.
	Initializing bool
	// Locked means it is locked (`git worktree lock`), so git won't remove
	// it; LockReason is the reason given, if any.
	Locked     bool
	LockReason string
}

// Worktrees lists the Worktrees of the repository whose main Worktree is at
// mainDir, from git's registry. The main Worktree comes first.
func Worktrees(ctx context.Context, mainDir string) ([]Worktree, error) {
	cmd := gitCommand(ctx, mainDir, "worktree", "list", "--porcelain", "-z")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list in %s: %v: %s", mainDir, err, strings.TrimSpace(stderr.String()))
	}
	// Records are NUL-terminated attribute lines, separated by an empty one.
	var list []Worktree
	var cur *Worktree
	for _, f := range strings.Split(string(out), "\x00") {
		switch {
		case f == "":
			cur = nil
		case strings.HasPrefix(f, "worktree "):
			list = append(list, Worktree{Dir: filepath.Clean(strings.TrimPrefix(f, "worktree "))})
			cur = &list[len(list)-1]
		case cur == nil:
		case f == "prunable" || strings.HasPrefix(f, "prunable "):
			cur.Gone = true
		case f == "locked initializing":
			// `git worktree add` holds this lock until the checkout is done.
			cur.Initializing = true
		case f == "locked" || strings.HasPrefix(f, "locked "):
			cur.Locked = true
			cur.LockReason = strings.TrimPrefix(strings.TrimPrefix(f, "locked"), " ")
		}
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("git worktree list in %s: no worktrees listed", mainDir)
	}
	list[0].Main = true
	// Porcelain output has no names, so map each path back to its entry in
	// <common>/worktrees/. That also works for entries whose directory is gone.
	names, err := registryNames(filepath.Join(mainDir, ".git"))
	if err != nil {
		return nil, err
	}
	for i := range list[1:] {
		w := &list[i+1]
		w.Name = names[w.Dir]
		if w.Name == "" {
			// Added after the registry was read; the next scan will see it.
			w.Initializing = true
		}
	}
	return list, nil
}

// registryNames reads <commonDir>/worktrees/<name>/gitdir for every linked
// worktree and maps the checkout directory to its name. gitdir holds the path
// of the checkout's .git file, relative to the admin dir when the worktree
// was added with --relative-paths.
func registryNames(commonDir string) (map[string]string, error) {
	names := map[string]string{}
	entries, err := os.ReadDir(filepath.Join(commonDir, "worktrees"))
	if errors.Is(err, os.ErrNotExist) {
		return names, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		admin := filepath.Join(commonDir, "worktrees", e.Name())
		b, err := os.ReadFile(filepath.Join(admin, "gitdir"))
		if err != nil {
			continue // half-created by a concurrent `git worktree add`
		}
		p := strings.TrimSpace(string(b))
		if !filepath.IsAbs(p) {
			p = filepath.Join(admin, p)
		}
		names[filepath.Dir(filepath.Clean(p))] = e.Name()
	}
	return names, nil
}

// gitCommand runs git in dir. Variables that would point git at another
// repository (set when Laneway itself runs inside a git hook) are removed.
func gitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		switch strings.SplitN(kv, "=", 2)[0] {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR":
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	return cmd
}

// AddWorktree runs `git worktree add` in the repository whose main Worktree
// is at mainDir, checking out branch at dir. The branch is created from the
// current HEAD if it doesn't exist.
func AddWorktree(ctx context.Context, mainDir, dir, branch string) error {
	args := []string{"worktree", "add", "--quiet"}
	if gitCommand(ctx, mainDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil {
		args = append(args, dir, branch)
	} else {
		args = append(args, "-b", branch, dir)
	}
	cmd := gitCommand(ctx, mainDir, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
