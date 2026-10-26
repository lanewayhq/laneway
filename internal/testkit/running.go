package testkit

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/cli"
)

// RunningCLI is a CLI invocation started by StartCLI that may still be
// running, such as `portby logs -f`.
type RunningCLI struct {
	d      *Daemon
	args   []string
	cancel context.CancelFunc
	done   chan struct{}
	stdout syncBuffer
	stderr syncBuffer
	code   int
}

// StartCLI runs `portby <args...>` in-process in the background, with dir as
// its working directory. Stop it with Stop (like pressing Ctrl-C) or wait for
// it to exit by itself with Wait. It is stopped when the test ends.
func (d *Daemon) StartCLI(dir string, args ...string) *RunningCLI {
	ctx, cancel := context.WithCancel(context.Background())
	r := &RunningCLI{d: d, args: args, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		r.code = cli.Run(ctx, cli.Env{Args: args, Dir: dir, Getenv: d.Getenv, Environ: d.Environ, Stdout: &r.stdout, Stderr: &r.stderr})
	}()
	d.t.Cleanup(func() { cancel(); <-r.done })
	return r
}

// Stdout returns what the command has printed to stdout so far.
func (r *RunningCLI) Stdout() string { return r.stdout.String() }

// WaitForOutput waits until stdout contains want, failing the test after 10s
// or if the command exits first.
func (r *RunningCLI) WaitForOutput(want string) {
	r.d.t.Helper()
	deadline := time.After(10 * time.Second)
	for !strings.Contains(r.Stdout(), want) {
		select {
		case <-r.done:
			if strings.Contains(r.Stdout(), want) {
				return
			}
			r.d.t.Fatalf("portby %s exited %d before printing %q:\n%s%s",
				strings.Join(r.args, " "), r.code, want, r.Stdout(), r.stderr.String())
		case <-deadline:
			r.d.t.Fatalf("portby %s never printed %q; so far:\n%s%s",
				strings.Join(r.args, " "), want, r.Stdout(), r.stderr.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Stop interrupts the command and returns its result.
func (r *RunningCLI) Stop() CLIResult {
	r.cancel()
	return r.Wait()
}

// Wait waits for the command to exit by itself, failing the test after
// cliTimeout, and returns its result.
func (r *RunningCLI) Wait() CLIResult {
	r.d.t.Helper()
	select {
	case <-r.done:
	case <-time.After(cliTimeout):
		r.d.t.Fatalf("portby %s still running after %s:\n%s%s",
			strings.Join(r.args, " "), cliTimeout, r.Stdout(), r.stderr.String())
	}
	return CLIResult{Stdout: r.Stdout(), Stderr: r.stderr.String(), ExitCode: r.code}
}

// syncBuffer is a bytes.Buffer safe to write and read concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
