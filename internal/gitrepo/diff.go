package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

// Diffs of a Worktree's changed files and of its commits, as git prints
// them, for the Dashboard to show. Like the merge status, nothing here
// fetches from a remote. External diff programs and textconv filters are
// never run.

// ErrUnknownCommit is returned for a commit the repository doesn't have.
var ErrUnknownCommit = errors.New("no such commit")

// commitHash is a full or abbreviated commit hash (SHA-1 or SHA-256).
var commitHash = regexp.MustCompile(`^[0-9a-f]{4,64}$`)

// FileDiff is the unified diff of changed file c of the Worktree at dir
// against its HEAD: its staged and unstaged changes together, an untracked
// file as added, and an untracked directory as each file in it added. It is
// at most limit bytes, cut at a line's end, and truncated says if it was
// cut.
func FileDiff(ctx context.Context, dir string, c Change, limit int) (diff string, truncated bool, err error) {
	b := &diffBuffer{limit: limit}
	if c.Status != "??" {
		err = b.git(ctx, dir, false, "--no-optional-locks", "diff", "--no-color", "--no-ext-diff", "--no-textconv", "HEAD", "--", c.Path)
		return b.result(err)
	}
	files := []string{c.Path}
	if strings.HasSuffix(c.Path, "/") {
		out, err := run(ctx, dir, "ls-files", "--others", "--exclude-standard", "-z", "--", c.Path)
		if err != nil {
			return "", false, err
		}
		files = strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	}
	for _, f := range files {
		if f == "" {
			continue
		}
		// --no-index exits 1 when the files differ, which they always do.
		if err := b.git(ctx, dir, true, "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-index", "--", "/dev/null", f); err != nil {
			return "", false, err
		}
	}
	return b.result(nil)
}

// CommitDiff is commit's header, message and patch, as git show prints
// them, in the repository of the Worktree at dir. commit is a full or
// abbreviated hash; ErrUnknownCommit if the repository has no such commit.
// It is at most limit bytes, like FileDiff.
func CommitDiff(ctx context.Context, dir, commit string, limit int) (diff string, truncated bool, err error) {
	if !commitHash.MatchString(commit) {
		return "", false, fmt.Errorf("%w: %q isn't a commit hash", ErrUnknownCommit, commit)
	}
	if _, err := run(ctx, dir, "cat-file", "-e", commit+"^{commit}"); err != nil {
		return "", false, fmt.Errorf("%w: %s", ErrUnknownCommit, commit)
	}
	b := &diffBuffer{limit: limit}
	return b.result(b.git(ctx, dir, false, "show", "--no-color", "--no-ext-diff", "--no-textconv", commit, "--"))
}

// diffBuffer collects git's output up to limit bytes.
type diffBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

// git runs git in dir and appends its output, stopping it once the buffer
// is full. Exit status 1 is a success when ok1 is set.
func (b *diffBuffer) git(ctx context.Context, dir string, ok1 bool, args ...string) error {
	if b.truncated {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := gitCommand(ctx, dir, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	room := int64(b.limit - b.buf.Len())
	n, _ := io.Copy(&b.buf, io.LimitReader(out, room+1))
	if n > room {
		b.truncated = true
		cancel() // the rest isn't wanted
	}
	err = cmd.Wait()
	var exit *exec.ExitError
	switch {
	case b.truncated, err == nil, ok1 && errors.As(err, &exit) && exit.ExitCode() == 1:
		return nil
	}
	return fmt.Errorf("git %s in %s: %v: %s", strings.Join(args, " "), dir, err, strings.TrimSpace(stderr.String()))
}

// result is what was collected, cut back to its last whole line within the
// limit if it ran over.
func (b *diffBuffer) result(err error) (string, bool, error) {
	if err != nil {
		return "", false, err
	}
	out := b.buf.Bytes()
	if b.truncated {
		out = out[:min(len(out), b.limit)]
		if i := bytes.LastIndexByte(out, '\n'); i >= 0 {
			out = out[:i+1]
		}
	}
	return string(out), b.truncated, nil
}
