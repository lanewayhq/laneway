package supervisor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Log file rotation defaults: a Process's output takes at most about
// (DefaultLogBackups+1) × DefaultLogMaxBytes on disk.
const (
	DefaultLogMaxBytes = 10 << 20
	DefaultLogBackups  = 3
)

// logFile is a Process's log file. It is opened (for appending) on the first
// write. Once a line would take it past maxBytes it is rotated: <path> moves
// to <path>.1, <path>.1 to <path>.2 and so on, and the oldest beyond
// <path>.<backups> is deleted. A nil *logFile discards everything.
type logFile struct {
	path     string
	maxBytes int64
	backups  int

	f    *os.File
	size int64
}

func (l *logFile) writeLine(text string) error {
	if l == nil {
		return nil
	}
	line := text + "\n"
	if l.f == nil {
		if err := l.open(); err != nil {
			return err
		}
	}
	if l.size > 0 && l.size+int64(len(line)) > l.maxBytes {
		l.close()
		if err := l.rotate(); err != nil {
			return err
		}
		if err := l.open(); err != nil {
			return err
		}
	}
	n, err := l.f.WriteString(line)
	l.size += int64(n)
	return err
}

func (l *logFile) open() error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, info.Size()
	return nil
}

func (l *logFile) rotate() error {
	backup := func(i int) string { return fmt.Sprintf("%s.%d", l.path, i) }
	if err := os.Remove(backup(l.backups)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for i := l.backups - 1; i >= 1; i-- {
		if err := os.Rename(backup(i), backup(i+1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if l.backups == 0 {
		return os.Remove(l.path)
	}
	return os.Rename(l.path, backup(1))
}

func (l *logFile) close() {
	if l == nil || l.f == nil {
		return
	}
	l.f.Close()
	l.f = nil
}
