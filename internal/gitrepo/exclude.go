package gitrepo

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Exclude adds pattern to the repository's own exclude file,
// .git/info/exclude, which applies to all its Worktrees and is never
// committed, with comment above it, unless the file has that line already.
// mainDir is the main Worktree's directory.
func Exclude(mainDir, pattern, comment string) error {
	path := filepath.Join(mainDir, ".git", "info", "exclude")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for line := range strings.Lines(string(data)) {
		if strings.TrimSpace(line) == pattern {
			return nil
		}
	}
	var b bytes.Buffer
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "# %s\n%s\n", comment, pattern)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b.Bytes()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
