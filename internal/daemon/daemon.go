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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/clock"
	"github.com/mrsafalpiya/portby.dev/internal/container"
	"github.com/mrsafalpiya/portby.dev/internal/dashboard"
	"github.com/mrsafalpiya/portby.dev/internal/paths"
	"github.com/mrsafalpiya/portby.dev/internal/platform"
	"github.com/mrsafalpiya/portby.dev/internal/provision"
	"github.com/mrsafalpiya/portby.dev/internal/proxy"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
	"github.com/mrsafalpiya/portby.dev/internal/service"
	"github.com/mrsafalpiya/portby.dev/internal/supervisor"
	"github.com/mrsafalpiya/portby.dev/internal/watcher"
)

// Config is everything the daemon needs from the outside world. Tests build
// it through testkit; `portby daemon` builds it from the environment.
//
// The spec's Seam 1 swaps exactly two things in tests: the clock and the
// container engine. Both are interface-typed fields with real defaults filled
// in by Start, so tests can inject fakes.
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
	// HTTPAddr is where plain HTTP is redirected to HTTPS from,
	// DefaultHTTPAddr if empty. It must be a loopback address; tests use
	// port 0. If it can't be bound the daemon logs why and runs without
	// it (`portby doctor` reports the port's holder).
	HTTPAddr string
	// Env is the environment Processes start from, before their Worktree's
	// Runtimes are resolved into it (package runtimes): its PATH is where
	// mise, or without mise the Runtimes themselves, are looked up. Nil
	// means the daemon's own environment.
	Env []string
	// WorktreeRescanInterval is how often every Project's git worktree
	// registry is rescanned even without file system events;
	// watcher.DefaultRescanInterval if zero.
	WorktreeRescanInterval time.Duration
	// ContainerEngine runs Services' containers. Nil means the Docker
	// Engine API found on the machine when a Service first needs it,
	// rootless Podman's first (ADR-0005). Tests use containertest.Fake.
	ContainerEngine container.Engine
	// ServiceHosts are the addresses Services listen on; nil means
	// DefaultServiceHosts. The first is the host clients are given.
	ServiceHosts []string
	// ContainerPrefix starts the names of Services' containers and
	// volumes; "portby" if empty.
	ContainerPrefix string
	// DashboardDomain is where the Dashboard is served,
	// dashboard.DefaultDomain if empty. Tests running several daemons at
	// once give each its own, since Caddy's certificate cache is shared.
	DashboardDomain string
}

// DefaultServiceHosts are where Services listen: loopback only, IPv4 and
// IPv6 (`psql -h localhost` tries ::1 first).
var DefaultServiceHosts = []string{"127.0.0.1", "::1"}

// DefaultProxyAddr is where browsers expect HTTPS.
const DefaultProxyAddr = "127.0.0.1:443"

// DefaultHTTPAddr is where browsers expect plain HTTP.
const DefaultHTTPAddr = "127.0.0.1:80"

// Daemon is a running daemon.
type Daemon struct {
	cfg      Config
	log      *slog.Logger
	registry *registry.Registry
	server   *http.Server
	listener net.Listener

	dashboard *http.Server // see dashboard.go

	supervisor *supervisor.Supervisor
	proxy      *proxy.Proxy
	redirect   *proxy.Redirect // nil if HTTPAddr couldn't be bound
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
	services    *service.Manager       // shared Services, see services.go

	orphanMu      sync.Mutex // serialises changes to Orphans, see orphans.go
	orphanTimer   clock.Timer
	orphanRetryAt time.Time // after a failed drop, when to try again
	orphansClosed bool
	// orphanSweeping is set while due Orphans are being dropped;
	// orphanDropMu serialises dropping (sweeps and gc).
	orphanSweeping bool
	orphanDropMu   sync.Mutex

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
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = DefaultHTTPAddr
	}
	if cfg.DashboardDomain == "" {
		cfg.DashboardDomain = dashboard.DefaultDomain
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	if cfg.Env == nil {
		cfg.Env = os.Environ()
	}
	if cfg.ServiceHosts == nil {
		cfg.ServiceHosts = DefaultServiceHosts
	}
	if cfg.ContainerEngine == nil {
		cfg.ContainerEngine = discoverEngine(cfg.Env)
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
	d.services = service.New(service.Options{
		Engine:      cfg.ContainerEngine,
		Clock:       cfg.Clock,
		Log:         log,
		Hosts:       cfg.ServiceHosts,
		Prefix:      cfg.ContainerPrefix,
		IdleTimeout: d.serviceIdleTimeout,
	})
	d.serveRecordedServices()
	d.stopping, d.stop = context.WithCancel(context.Background())
	d.proxy, err = proxy.Start(proxy.Config{
		Addr:       cfg.ProxyAddr,
		StorageDir: cfg.Paths.CertStorage(),
		Router:     router{d},
		LogErrors:  cfg.Logger != nil,
	})
	if err != nil {
		d.services.Close(context.Background())
		ln.Close()
		return nil, err
	}
	_, httpsPort, _ := net.SplitHostPort(d.proxy.Addr().String())
	if d.redirect, err = proxy.StartRedirect(cfg.HTTPAddr, httpsPort); err != nil {
		log.Error("not redirecting plain HTTP to HTTPS", "addr", cfg.HTTPAddr, "err", err)
	}
	if err := d.startDashboard(); err != nil {
		d.stopRedirect(context.Background())
		d.proxy.Stop()
		ln.Close()
		return nil, err
	}
	d.watcher, err = watcher.New(watcher.Config{
		Log:            log,
		Changed:        d.worktreesChanged,
		RescanInterval: cfg.WorktreeRescanInterval,
	})
	if err != nil {
		d.dashboard.Close()
		d.stopRedirect(context.Background())
		d.proxy.Stop()
		d.services.Close(context.Background())
		ln.Close()
		return nil, fmt.Errorf("watching Worktrees: %w", err)
	}
	for _, p := range reg.Projects() {
		ctx := context.Background()
		d.serveProject(p)
		d.syncWorktrees(ctx, p)
		d.serveKnownWorktrees(p)
	}
	d.serveOrphans()
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

// HTTPAddr is the address plain HTTP is redirected to HTTPS from, or nil if
// it couldn't be bound.
func (d *Daemon) HTTPAddr() net.Addr {
	if d.redirect == nil {
		return nil
	}
	return d.redirect.Addr()
}

func (d *Daemon) stopRedirect(ctx context.Context) error {
	if d.redirect == nil {
		return nil
	}
	return d.redirect.Stop(ctx)
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
	if derr := d.dashboard.Shutdown(ctx); err == nil {
		err = derr
	}
	os.Remove(d.dashboardSocket())
	d.watcher.Close()
	if rerr := d.stopRedirect(ctx); err == nil {
		err = rerr
	}
	if perr := d.proxy.Stop(); err == nil {
		err = perr
	}
	d.closeOrphans()
	d.startsMu.Lock()
	d.shuttingDown = true
	d.startsMu.Unlock()
	d.stop()
	d.starts.Wait()
	d.provisioner.Close()
	d.supervisor.StopAll(ctx)
	// Services last: Processes may be using them until they stop.
	d.services.Close(ctx)
	return err
}

// discoverEngine is the container engine found through the platform layer
// (the same sockets `portby doctor` checks), looked up on first use.
func discoverEngine(env []string) container.Engine {
	getenv := func(key string) string {
		for _, kv := range slices.Backward(env) {
			if k, v, ok := strings.Cut(kv, "="); ok && k == key {
				return v
			}
		}
		return ""
	}
	return &container.Discovered{Find: func(ctx context.Context) (string, error) {
		e, err := platform.Detect(getenv).ContainerEngine(ctx)
		return strings.TrimPrefix(e.Address, "unix://"), err
	}}
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
