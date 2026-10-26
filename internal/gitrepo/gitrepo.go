// Package gitrepo answers questions about git checkouts on disk by running the
// git binary.
package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--path-format=absolute",
		"--show-toplevel", "--git-dir", "--git-common-dir")
	cmd.Dir = dir
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
