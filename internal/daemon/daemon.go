// Package daemon is the long-running Portby process. It owns the Registry,
// serves the JSON API (see package api) over a Unix socket that only the user
// can access, runs each Project's Processes and serves them at their Domains
// through the HTTPS proxy.
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
	"sync"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/clock"
	"github.com/mrsafalpiya/portby.dev/internal/paths"
	"github.com/mrsafalpiya/portby.dev/internal/provision"
	"github.com/mrsafalpiya/portby.dev/internal/proxy"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
	"github.com/mrsafalpiya/portby.dev/internal/supervisor"
	"github.com/mrsafalpiya/portby.dev/internal/watcher"
)

// Config is everything the daemon needs from the outside world. Tests build
// it through testkit; `portby daemon` builds it from the environment.
//
// The spec's Seam 1 swaps exactly two things in tests: the clock and the
// container engine. Both are interface-typed fields with real defaults filled
// in by Start, so tests can inject fakes. (The container engine isn't here
// yet.)
type Config struct {
	Paths  paths.Paths
	Logger *slog.Logger
	// Clock is what everything time-based reads (crash backoff, and later
	// Idle and Orphan grace). Defaults to the real clock; see package clock.
	Clock clock.Clock
	// LogFileMaxBytes is the size Process log files are rotated at;
	// supervisor.DefaultLogMaxBytes if 0.
	LogFileMaxBytes int64
	// ProxyAddr is the HTTPS proxy's listen address, DefaultProxyAddr if
	// empty. It must be a loopback address; tests use port 0.
	ProxyAddr string
	// WorktreeRescanInterval is how often every Project's git worktree
	// registry is rescanned even without file system events;
	// watcher.DefaultRescanInterval if zero.
	WorktreeRescanInterval time.Duration
}

// DefaultProxyAddr is where browsers expect HTTPS.
const DefaultProxyAddr = "127.0.0.1:443"

// Daemon is a running daemon.
type Daemon struct {
	cfg      Config
	log      *slog.Logger
	registry *registry.Registry
	server   *http.Server
	listener net.Listener

	supervisor *supervisor.Supervisor
	proxy      *proxy.Proxy
	servedMu   sync.Mutex
	served     map[string]*served // Domain → what is served there

	// Worktree starts run in the background. Shutdown cancels stopping and
	// waits for them, so none starts Processes after StopAll.
	stopping     context.Context
	stop         context.CancelFunc
	startsMu     sync.Mutex
	starts       sync.WaitGroup
	shuttingDown bool

	watcher *watcher.Watcher
	syncMu  sync.Mutex // serialises Worktree syncs

	provisioner *provision.Provisioner // Worktree setup, see provision.go

	// closing is closed (under syncMu) when Shutdown begins. It ends
	// streaming responses, and Worktree syncs that start afterwards don't
	// change anything.
	closing  chan struct{}
	done     chan struct{} // closed once the server stops
	serveErr error         // why it stopped; read only after done is closed
}

// Start opens the Registry, binds the API socket and the HTTPS proxy, starts
// every Project's Processes and begins serving in the background. It returns
// once the socket accepts connections.
func Start(cfg Config) (*Daemon, error) {
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.ProxyAddr == "" {
		cfg.ProxyAddr = DefaultProxyAddr
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	reg, err := registry.Open(cfg.Paths.Registry())
	if err != nil {
		return nil, err
	}
	// The socket comes first: it is what detects a daemon that's already
	// running, before anything else is bound or started.
	ln, err := listen(cfg.Paths.Socket)
	if err != nil {
		return nil, err
	}
	prov, err := provision.Open(cfg.Paths.Setup(), log)
	if err != nil {
		ln.Close()
		return nil, err
	}
	sup := supervisor.New(log, supervisor.Options{Clock: cfg.Clock, LogMaxBytes: cfg.LogFileMaxBytes})
	d := &Daemon{
		cfg:        cfg,
		log:        log,
		registry:   reg,
		listener:   ln,
		supervisor: sup,
		served:     map[string]*served{},
		closing:    make(chan struct{}),
		done:       make(chan struct{}),
	}
	d.provisioner = prov
	d.stopping, d.stop = context.WithCancel(context.Background())
	d.proxy, err = proxy.Start(proxy.Config{
		Addr:       cfg.ProxyAddr,
		StorageDir: cfg.Paths.CertStorage(),
		Router:     router{d},
		LogErrors:  cfg.Logger != nil,
	})
	if err != nil {
		ln.Close()
		return nil, err
	}
	d.watcher, err = watcher.New(watcher.Config{
		Log:            log,
		Changed:        d.worktreesChanged,
		RescanInterval: cfg.WorktreeRescanInterval,
	})
	if err != nil {
		d.proxy.Stop()
		ln.Close()
		return nil, fmt.Errorf("watching Worktrees: %w", err)
	}
	for _, p := range reg.Projects() {
		ctx := context.Background()
		d.serveProject(p)
		d.syncWorktrees(ctx, p)
		d.serveKnownWorktrees(p)
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
	log.Info("daemon started", "socket", cfg.Paths.Socket, "proxy", d.proxy.Addr())
	return d, nil
}

// ProxyAddr is the address the HTTPS proxy is listening on.
func (d *Daemon) ProxyAddr() net.Addr {
	return d.proxy.Addr()
}

// Wait blocks until the daemon stops serving and returns why.
func (d *Daemon) Wait() error {
	<-d.done
	return d.serveErr
}

// Shutdown ends streaming responses and stops Worktrees from changing, stops
// accepting API requests, waits for in-flight ones to finish and removes the
// socket, then stops watching Worktrees, the proxy and every Process.
func (d *Daemon) Shutdown(ctx context.Context) error {
	d.syncMu.Lock()
	close(d.closing)
	d.syncMu.Unlock()
	err := d.server.Shutdown(ctx)
	if werr := d.Wait(); err == nil {
		err = werr
	}
	d.watcher.Close()
	if perr := d.proxy.Stop(); err == nil {
		err = perr
	}
	d.startsMu.Lock()
	d.shuttingDown = true
	d.startsMu.Unlock()
	d.stop()
	d.starts.Wait()
	d.provisioner.Close()
	d.supervisor.StopAll(ctx)
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
