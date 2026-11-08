package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lanewayhq/laneway/internal/prepare"
	"github.com/lanewayhq/laneway/internal/registry"
)

// copyStep copies the paths config's copy lists from the main Worktree into
// the Worktree w, which isn't the main one. It runs first, so that a Harness
// started in a new worktree finds them as soon as possible. A path the main
// Worktree doesn't have is skipped: lists such as AGENTS.md and CLAUDE.md
// name files a Project may not have.
func copyStep(p registry.Project, w worktreeRef, paths []string) prepare.Step {
	return prepare.Step{Name: "copy", Run: func(ctx context.Context, _ io.Writer) error {
		for _, rel := range paths {
			if err := ctx.Err(); err != nil {
				return err
			}
			src := filepath.Join(p.Path, filepath.FromSlash(rel))
			if _, err := os.Lstat(src); errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err := copyMissing(src, filepath.Join(w.dir, filepath.FromSlash(rel))); err != nil {
				return fmt.Errorf("copying %s from the main Worktree: %w", rel, err)
			}
		}
		return nil
	}}
}

// copyMissing copies src to dst, a directory with everything in it, leaving
// every file dst already has as it is. Links are copied as links, and files
// keep their permissions.
func copyMissing(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
			return err // nil: the Worktree has its own
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.Type().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		}
		return nil // a socket or such
	})
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}
