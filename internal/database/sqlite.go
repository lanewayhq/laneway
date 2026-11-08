package database

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// SQLite is the Engine of SQLite databases: a file at Path in each Worktree.
// It needs no Service.
type SQLite struct {
	// Path is the database file, relative to the Worktree.
	Path string
}

// Database is the absolute path of the file in the Worktree at dir.
func (s SQLite) Database(_, _, dir string) string { return filepath.Join(dir, s.Path) }

func (SQLite) Exists(_ context.Context, db string) (bool, error) {
	_, err := os.Stat(db)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Create creates an empty file, which SQLite opens as an empty database.
func (SQLite) Create(ctx context.Context, db string) error {
	if err := removeSidecars(db); err != nil {
		return err
	}
	return install(ctx, db, 0o644, func(*os.File) error { return nil })
}

// Clone copies the file, and its write-ahead log if there is one: in WAL
// mode, recent writes may only be in the log. Copying while the main
// Worktree's app is writing can give a torn copy, as with any file copy.
func (SQLite) Clone(ctx context.Context, from, to string) error {
	info, err := os.Stat(from)
	if err != nil {
		return err
	}
	if err := removeSidecars(to); err != nil {
		return err
	}
	if err := install(ctx, to+"-wal", info.Mode().Perm(), copyFrom(ctx, from+"-wal")); err != nil &&
		!errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return install(ctx, to, info.Mode().Perm(), copyFrom(ctx, from))
}

// removeSidecars removes the journal, write-ahead log and shared memory files
// an earlier database at db may have left: SQLite would apply them to a new
// database there.
func removeSidecars(db string) error {
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if err := os.Remove(db + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// install writes a file at path with write and moves it into place in one
// step, so path is either complete or missing.
func install(ctx context.Context, path string, mode fs.FileMode, write func(*os.File) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".laneway-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := write(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// copyFrom copies the file at src, stopping when ctx is cancelled.
func copyFrom(ctx context.Context, src string) func(*os.File) error {
	return func(dst *os.File) error {
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(dst, readerFunc(func(p []byte) (int, error) {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			return f.Read(p)
		}))
		return err
	}
}

type readerFunc func([]byte) (int, error)

func (r readerFunc) Read(p []byte) (int, error) { return r(p) }
