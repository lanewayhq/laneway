// Package daemon is the long-running Portby process. It owns the Registry and
// serves the JSON API (see package api) over a Unix socket that only the user
// can access.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/paths"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
)

// Config is everything the daemon needs from the outside world. Tests build
// it through testkit; `portby daemon` builds it from the environment.
//
// The spec's Seam 1 swaps exactly two things in tests: the clock and the
// container engine. Both will be added here as interface-typed fields with
// real defaults filled in by Start, so tests can inject fakes.
type Config struct {
	Paths  paths.Paths
	Logger *slog.Logger
}

// Daemon is a running daemon.
type Daemon struct {
	cfg      Config
	log      *slog.Logger
	registry *registry.Registry
	server   *http.Server
	listener net.Listener

	done     chan struct{} // closed once the server stops
	serveErr error         // why it stopped; read only after done is closed
}

// Start opens the Registry, binds the API socket and begins serving in the
// background. It returns once the socket accepts connections.
func Start(cfg Config) (*Daemon, error) {
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	reg, err := registry.Open(cfg.Paths.Registry())
	if err != nil {
		return nil, err
	}
	ln, err := listen(cfg.Paths.Socket)
	if err != nil {
		return nil, err
	}
	d := &Daemon{
		cfg:      cfg,
		log:      log,
		registry: reg,
		listener: ln,
		done:     make(chan struct{}),
	}
	d.server = &http.Server{
		Handler:           d.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		err := d.server.Serve(ln)
		if !errors.Is(err, http.ErrServerClosed) {
			d.serveErr = err
		}
		close(d.done)
	}()
	log.Info("daemon started", "socket", cfg.Paths.Socket)
	return d, nil
}

// Wait blocks until the daemon stops serving and returns why.
func (d *Daemon) Wait() error {
	<-d.done
	return d.serveErr
}

// Shutdown stops accepting requests, waits for in-flight ones to finish and
// removes the socket.
func (d *Daemon) Shutdown(ctx context.Context) error {
	err := d.server.Shutdown(ctx)
	if werr := d.Wait(); err == nil {
		err = werr
	}
	return err
}

// listen binds the API socket. The socket's directory is created (or tightened)
// to 0700 and the socket itself to 0600, so only the user can reach the API.
func listen(socket string) (net.Listener, error) {
	dir := filepath.Dir(socket)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating socket directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("securing socket directory: %w", err)
	}
	if conn, err := net.Dial("unix", socket); err == nil {
		conn.Close()
		return nil, fmt.Errorf("a Portby daemon is already running on %s", socket)
	}
	// Nothing is listening, so any file left here is a stale socket from a
	// daemon that didn't shut down cleanly.
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("removing stale socket: %w", err)
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("listening on %s: %w", socket, err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("securing socket: %w", err)
	}
	return ln, nil
}
