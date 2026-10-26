package supervisor

import (
	"bufio"
	"cmp"
	"io"
	"maps"
	"slices"
	"time"
)

// Line is one line of a Process's output (stdout and stderr together).
type Line struct {
	// Seq orders lines across every Process of the Supervisor, so output of
	// several Processes can be interleaved. It is the order lines were read
	// from the Processes' pipes: lines two Processes write within moments of
	// each other may come out in either order.
	Seq     uint64
	Time    time.Time
	Process string
	Text    string
}

// RecentLines is how many lines of output are kept in memory per Process.
const RecentLines = 1000

// maxLineBytes caps one line. Longer output without a newline is split.
const maxLineBytes = 64 << 10

// output is everything a Process has written. It outlives the Process (and
// its restarts), so its recent lines can still be read after it stops.
// Guarded by the Supervisor's outMu.
type output struct {
	recent []Line // ring of the last RecentLines lines
	next   int    // where the next line goes once recent is full
	file   *logFile
}

// setFile makes path the output's log file from now on.
func (o *output) setFile(path string, opts Options) {
	if o.file != nil && o.file.path == path {
		return
	}
	o.file.close()
	o.file = nil
	if path != "" {
		o.file = &logFile{path: path, maxBytes: opts.LogMaxBytes, backups: opts.LogBackups}
	}
}

func (o *output) add(l Line) {
	if len(o.recent) < RecentLines {
		o.recent = append(o.recent, l)
		return
	}
	o.recent[o.next] = l
	o.next = (o.next + 1) % RecentLines
}

// lines returns the kept lines, oldest first.
func (o *output) lines() []Line {
	return append(slices.Clone(o.recent[o.next:]), o.recent[:o.next]...)
}

// OutputNames returns the names of group's Processes that have output kept,
// sorted, whether or not they are still running.
func (s *Supervisor) OutputNames(group string) []string {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	return slices.Sorted(maps.Keys(s.outputs[group]))
}

// outputOf returns the output kept for Process name of group, creating it.
// The caller holds outMu.
func (s *Supervisor) outputOf(group, name string) *output {
	byName := s.outputs[group]
	if byName == nil {
		byName = map[string]*output{}
		s.outputs[group] = byName
	}
	o := byName[name]
	if o == nil {
		o = &output{}
		byName[name] = o
	}
	return o
}

// writeLine records one line of output from Process name of group and wakes
// everyone following output.
func (s *Supervisor) writeLine(group, name, text string) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	s.seq++
	o := s.outputOf(group, name)
	o.add(Line{Seq: s.seq, Time: s.clock.Now(), Process: name, Text: text})
	if err := o.file.writeLine(text); err != nil {
		s.log.Warn("writing Process log file", "group", group, "process", name, "err", err)
	}
	close(s.outChanged)
	s.outChanged = make(chan struct{})
}

// closeLogFiles closes every open log file. A later line reopens its file.
func (s *Supervisor) closeLogFiles() {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	for _, byName := range s.outputs {
		for _, o := range byName {
			o.file.close()
		}
	}
}

// copyOutput reads r line by line into the output of Process name of group
// until EOF.
func (s *Supervisor) copyOutput(group, name string, r io.Reader) {
	br := bufio.NewReaderSize(r, maxLineBytes)
	for {
		line, err := br.ReadSlice('\n')
		if len(line) > 0 {
			text := string(line)
			if text[len(text)-1] == '\n' {
				text = text[:len(text)-1]
			}
			s.writeLine(group, name, text)
		}
		if err != nil && err != bufio.ErrBufferFull {
			return
		}
	}
}

// Output returns the kept output of group's Processes written after the line
// numbered after, oldest first. With process set, only that Process's
// output is returned. It also returns a channel that is closed when any
// Process next writes a line, so callers can follow output without missing
// any.
func (s *Supervisor) Output(group, process string, after uint64) ([]Line, <-chan struct{}) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	var lines []Line
	for name, o := range s.outputs[group] {
		if process != "" && name != process {
			continue
		}
		for _, l := range o.lines() {
			if l.Seq > after {
				lines = append(lines, l)
			}
		}
	}
	slices.SortFunc(lines, func(a, b Line) int { return cmp.Compare(a.Seq, b.Seq) })
	return lines, s.outChanged
}
