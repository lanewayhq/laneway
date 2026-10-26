package supervisor

import (
	"bytes"
	"sync"
)

// RecentLines is how many lines of output each Process keeps in memory.
const RecentLines = 50

// recent is an io.Writer that keeps the last RecentLines lines written to it.
type recent struct {
	mu      sync.Mutex
	lines   []string
	partial []byte // the start of a line that hasn't ended yet
}

func (r *recent) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(p)
	for {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			break
		}
		r.add(string(append(r.partial, p[:i]...)))
		r.partial = r.partial[:0]
		p = p[i+1:]
	}
	r.partial = append(r.partial, p...)
	return n, nil
}

func (r *recent) add(line string) {
	r.lines = append(r.lines, line)
	if len(r.lines) > RecentLines {
		r.lines = r.lines[len(r.lines)-RecentLines:]
	}
}

// snapshot returns the kept lines, including an unfinished last line.
func (r *recent) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	lines := append([]string(nil), r.lines...)
	if len(r.partial) > 0 {
		lines = append(lines, string(r.partial))
		lines = lines[max(0, len(lines)-RecentLines):]
	}
	return lines
}
