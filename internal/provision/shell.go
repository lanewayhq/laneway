package provision

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Shell is a step that runs command with `/bin/sh -c` in dir, with env as its
// environment. Stdout and stderr both go to the step's output. Cancelling
// the step kills the command's whole process group.
func Shell(name, command, dir string, env []string) Step {
	return Step{Name: name, Run: func(ctx context.Context, out io.Writer) error {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
		cmd.Dir = dir
		cmd.Env = env
		cmd.Stdout = out
		cmd.Stderr = out
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		// A command that leaves something running in the background
		// holding its output open doesn't hold up setup.
		cmd.WaitDelay = time.Second
		return cmd.Run()
	}}
}

// tail is a step's output: it keeps the last OutputLines lines.
type tail struct {
	mu      sync.Mutex
	lines   []string
	partial []byte
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.partial = append(t.partial, b...)
	for {
		i := bytes.IndexByte(t.partial, '\n')
		if i < 0 {
			break
		}
		t.add(strings.TrimSuffix(string(t.partial[:i]), "\r"))
		t.partial = t.partial[i+1:]
	}
	return len(b), nil
}

func (t *tail) add(line string) {
	t.lines = append(t.lines, line)
	if len(t.lines) > OutputLines {
		t.lines = t.lines[len(t.lines)-OutputLines:]
	}
}

// Lines returns the kept lines, including a last unterminated one.
func (t *tail) Lines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := append([]string(nil), t.lines...)
	if len(t.partial) > 0 {
		lines = append(lines, string(t.partial))
		lines = lines[max(0, len(lines)-OutputLines):]
	}
	return lines
}
