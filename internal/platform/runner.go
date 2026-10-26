package platform

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Cmd is one program for a Runner to run.
type Cmd struct {
	Name string
	Args []string
	// Dir is the working directory; "" is the current one.
	Dir string
	// Interactive connects the user's terminal to stdin and stderr, for a
	// sudo password prompt. Stdout is still captured.
	Interactive bool
	// Detach starts the program in its own session and returns without
	// waiting for it, as for a browser.
	Detach bool
}

// Runner runs other programs. Linux uses it for everything it does to the
// machine, so tests can replace it.
type Runner interface {
	LookPath(name string) (string, error)
	// Run runs c and returns its stdout. A failure's error includes stderr.
	Run(ctx context.Context, c Cmd) ([]byte, error)
}

// ExecRunner runs programs for real.
type ExecRunner struct{}

func (ExecRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (ExecRunner) Run(ctx context.Context, c Cmd) ([]byte, error) {
	if c.Detach {
		cmd := exec.Command(c.Name, c.Args...)
		cmd.Dir = c.Dir
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		go cmd.Wait()
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if c.Interactive {
		cmd.Stdin = os.Stdin
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return stdout.Bytes(), fmt.Errorf("%s: %w: %s", c.Name, err, msg)
		}
		return stdout.Bytes(), fmt.Errorf("%s: %w", c.Name, err)
	}
	return stdout.Bytes(), nil
}
