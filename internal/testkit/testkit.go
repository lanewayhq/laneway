// Package testkit is Seam 1 from the spec: it runs a real Portby daemon in a
// temporary home directory so tests can drive it through its API and the CLI,
// and observe only what is visible from outside the daemon.
//
// The daemon runs in-process so tests can swap the few things the spec allows
// (the clock and the container engine) through Options.
package testkit

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/cli"
	"github.com/mrsafalpiya/portby.dev/internal/client"
	"github.com/mrsafalpiya/portby.dev/internal/clock"
	"github.com/mrsafalpiya/portby.dev/internal/daemon"
	"github.com/mrsafalpiya/portby.dev/internal/paths"
	"github.com/mrsafalpiya/portby.dev/internal/platform/platformtest"
)

// Option adjusts the daemon's Config before every start, including restarts.
// A later ticket adds one for the fake container engine.
type Option func(*daemon.Config)

// WithClock makes the daemon read time from c, normally a *clock.Fake that
// the test moves forward instead of waiting. The same clock is kept across
// restarts.
func WithClock(c clock.Clock) Option {
	return func(cfg *daemon.Config) { cfg.Clock = c }
}

// Daemon is a running test daemon and the temporary home it owns.
type Daemon struct {
	t    testing.TB
	env  map[string]string
	opts []Option

	// Home is the temporary $HOME the daemon and CLI see.
	Home string
	// Paths is where the daemon keeps its state and socket.
	Paths paths.Paths
	// Platform is what the CLI uses instead of the real machine, so `portby
	// install`, `doctor` and `open` never run sudo, touch a trust store or
	// systemd, or open a browser. Tests set it up and inspect it.
	Platform *platformtest.Fake

	running *daemon.Daemon
}

// StartDaemon starts a daemon in a fresh temporary home and stops it when the
// test ends.
func StartDaemon(t testing.TB, opts ...Option) *Daemon {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("testkit: %v", err)
	}
	// Unix socket paths are limited to ~108 bytes, and t.TempDir() paths can
	// exceed that, so the runtime dir gets a short name of its own.
	runtimeDir, err := os.MkdirTemp("", "portby-test-")
	if err != nil {
		t.Fatalf("testkit: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(runtimeDir) })

	env := map[string]string{
		"HOME":            home,
		"XDG_RUNTIME_DIR": runtimeDir,
	}
	p, err := paths.FromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("testkit: %v", err)
	}
	d := &Daemon{t: t, env: env, opts: opts, Home: home, Paths: p, Platform: platformtest.New()}
	// Registered before d.Stop, so it runs after it (cleanups run in
	// reverse). Every t.TempDir of the test, home included, is in one dir.
	t.Cleanup(func() { killLeftovers(t, filepath.Dir(home)) })
	d.Start()
	t.Cleanup(d.Stop)
	return d
}

// Start starts the daemon if it isn't running.
func (d *Daemon) Start() {
	d.t.Helper()
	if d.running != nil {
		return
	}
	// Port 0: tests never bind fixed ports.
	cfg := daemon.Config{Paths: d.Paths, ProxyAddr: "127.0.0.1:0"}
	for _, opt := range d.opts {
		opt(&cfg)
	}
	running, err := daemon.Start(cfg)
	if err != nil {
		d.t.Fatalf("testkit: starting daemon: %v", err)
	}
	d.running = running
}

// Stop shuts the daemon down gracefully. It is safe to call more than once.
func (d *Daemon) Stop() {
	d.t.Helper()
	if d.running == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.running.Shutdown(ctx); err != nil {
		d.t.Errorf("testkit: stopping daemon: %v", err)
	}
	d.running = nil
}

// Restart stops the daemon and starts a new one on the same home.
func (d *Daemon) Restart() {
	d.t.Helper()
	d.Stop()
	d.Start()
}

// Client returns an API client connected to the daemon's socket.
func (d *Daemon) Client() *client.Client {
	return client.New(d.Paths.Socket)
}

// Getenv returns the environment variables the CLI sees.
func (d *Daemon) Getenv(key string) string {
	return d.env[key]
}

// CLIResult is the outcome of one CLI invocation.
type CLIResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Output returns stdout and stderr together, for assertions that don't care
// which stream a message went to.
func (r CLIResult) Output() string {
	return r.Stdout + r.Stderr
}

// cliTimeout bounds one CLI invocation, so a command that wrongly blocks
// (such as a second `portby daemon` that started) fails instead of hanging.
const cliTimeout = 15 * time.Second

// RunCLI runs `portby <args...>` in-process with dir as its working directory
// and the daemon's home as its environment.
func (d *Daemon) RunCLI(dir string, args ...string) CLIResult {
	d.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	code := cli.Run(ctx, cli.Env{
		Args:     args,
		Dir:      dir,
		Getenv:   d.Getenv,
		Stdout:   &stdout,
		Stderr:   &stderr,
		Platform: d.Platform,
	})
	return CLIResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: code}
}

// MustRunCLI is RunCLI that fails the test unless the command exits 0.
func (d *Daemon) MustRunCLI(dir string, args ...string) CLIResult {
	d.t.Helper()
	res := d.RunCLI(dir, args...)
	if res.ExitCode != 0 {
		d.t.Fatalf("portby %s exited %d\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), res.ExitCode, res.Stdout, res.Stderr)
	}
	return res
}
