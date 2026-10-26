package gitrepo

import (
	"os"
	"path/filepath"
	"strings"
)

// MainDir is the main Worktree's directory of the linked Worktree whose root
// is dir, read from git's files without running git: dir/.git is a file
// naming the Worktree's git dir, whose commondir names the repository's .git.
// false if dir isn't the root of a linked Worktree (the main Worktree's .git
// is a directory).
func MainDir(dir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil {
		return "", false
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return "", false
	}
	gitDir = relativeTo(dir, strings.TrimSpace(gitDir))
	common, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return "", false
	}
	commonDir := relativeTo(gitDir, strings.TrimSpace(string(common)))
	if filepath.Base(commonDir) != ".git" {
		return "", false // a bare repository has no main Worktree
	}
	return filepath.Dir(commonDir), true
}

// relativeTo is path, made absolute against dir if it is relative.
func relativeTo(dir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(dir, path)
}
