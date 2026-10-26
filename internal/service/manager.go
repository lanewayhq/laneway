package service

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/activity"
	"github.com/mrsafalpiya/portby.dev/internal/clock"
	"github.com/mrsafalpiya/portby.dev/internal/container"
)

// Service is an Engine at a version, listening on Port.
type Service struct {
	Engine  *Engine
	Version string
	Port    int
}

// Name is how the CLI and API name the Service, such as "redis:7".
func (s Service) Name() string { return s.Engine.Name + ":" + s.Version }

// States a Service can be in.
const (
	// Stopped: its container isn't running (or Portby doesn't know it is).
	Stopped = "stopped"
	// Starting: a connection or `services up` is starting it.
	Starting = "starting"
	// Running: it is ready for clients.
	Running = "running"
	// Idle: it was stopped after its idle timeout; the next connection
	// starts it again.
	Idle = "idle"
)

// DefaultIdleTimeout is how long a Service may go without connections and
// without a running Worktree using it before it is stopped, unless
// service_idle_timeout is configured.
const DefaultIdleTimeout = 15 * time.Minute

// DefaultStartTimeout is how long a Service gets to be ready after its
// container is started. It doesn't cover pulling the image, which normally
// happens in the background before the first connection.
const DefaultStartTimeout = 60 * time.Second

// Options configure a Manager.
type Options struct {
	// Engine runs the containers.
	Engine container.Engine
	// Clock decides when a Service is Idle.
	Clock clock.Clock
	Log   *slog.Logger
	// Hosts are the addresses each Service listens on, normally
	// 127.0.0.1 and ::1. Listening on the first one must succeed; the
	// others are best effort (a machine without IPv6). The first is the
	// host clients are given.
	Hosts []string
	// Prefix starts the name of every container and volume, normally
	// "portby": redis:7 runs as portby-redis-7.
	Prefix string
	// IdleTimeout is read every time a Service starts; nil or 0 means
	// DefaultIdleTimeout.
	IdleTimeout func() time.Duration
	// StartTimeout is DefaultStartTimeout if 0.
	StartTimeout time.Duration
}

// Status is what a Service is doing.
type Status struct {
	Service
	// Host is where clients connect, with Service.Port.
	Host  string
	State string
	// Error is why the Service can't be used: Portby can't listen on its
	// port, or its last start failed. "" if it is fine.
	Error string
}

// Manager runs Services. It is safe for concurrent use.
type Manager struct {
	opts   Options
	log    *slog.Logger
	ctx    context.Context // cancelled by Close
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	services map[string]*managed // by Name
	closed   bool
}

// New returns a Manager serving no Services yet.
func New(opts Options) *Manager {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	opts.Prefix = cmp.Or(opts.Prefix, "portby")
	opts.StartTimeout = cmp.Or(opts.StartTimeout, DefaultStartTimeout)
	m := &Manager{opts: opts, log: opts.Log, services: map[string]*managed{}}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	return m
}

// managed is one Service the Manager serves.
type managed struct {
	Service
	m         *Manager
	spec      container.Spec
	listeners []net.Listener
	// activity sees every connection, and every running Worktree using
	// the Service (as one long connection), and decides when it is Idle.
	activity *activity.Tracker

	// lifecycle serialises starting and stopping the container.
	lifecycle sync.Mutex

	mu    sync.Mutex
	state string
	addr  string // the container's published address while Running
	err   string
	conns map[net.Conn]bool
}

// Serve starts listening for the Service on its port, if it isn't served
// yet. Its container starts on the first connection. In the background it
// makes sure the image is pulled, so the first connection doesn't wait for
// a download, and adopts a container left running by an earlier daemon.
//
// A Service whose port can't be bound is still served (and listed, with the
// error) but can't be connected to.
func (m *Manager) Serve(svc Service) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.services[svc.Name()] != nil {
		return
	}
	s := &managed{Service: svc, m: m, spec: m.spec(svc), state: Stopped, conns: map[net.Conn]bool{}}
	s.activity = activity.New(m.opts.Clock, m.idleTimeout(), s.goIdle)
	m.services[svc.Name()] = s
	for i, host := range m.opts.Hosts {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(svc.Port)))
		if err != nil {
			if i == 0 {
				s.err = fmt.Sprintf("Portby can't listen on port %d: %v", svc.Port, err)
				m.log.Error("serving Service", "service", svc.Name(), "err", err)
				break
			}
			m.log.Warn("serving Service", "service", svc.Name(), "err", err)
			continue
		}
		s.listeners = append(s.listeners, ln)
		m.wg.Go(func() { s.accept(ln) })
	}
	m.wg.Go(s.prepare)
}

// spec is the container that runs svc.
func (m *Manager) spec(svc Service) container.Spec {
	e := svc.Engine
	name := m.opts.Prefix + "-" + e.Name + "-" + svc.Version
	spec := container.Spec{
		Name:         name,
		Image:        e.Image + ":" + svc.Version,
		Labels:       map[string]string{"dev.portby.service": svc.Name()},
		Port:         e.Port,
		Volume:       name,
		VolumeTarget: e.DataDir,
	}
	if e.Env != nil {
		spec.Env = e.Env(svc.Version)
	}
	if e.Cmd != nil {
		spec.Cmd = e.Cmd(svc.Version)
	}
	if e.WebPort != 0 {
		spec.ExtraPorts = []int{e.WebPort}
	}
	return spec
}

func (m *Manager) idleTimeout() time.Duration {
	if m.opts.IdleTimeout == nil {
		return DefaultIdleTimeout
	}
	return cmp.Or(m.opts.IdleTimeout(), DefaultIdleTimeout)
}

func (m *Manager) lookup(name string) (*managed, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.services[name]
	return s, ok
}

// Use records that a running Worktree uses the Service called name: it
// isn't Idle until release is called (when the Worktree stops or is Idle).
// It doesn't start the Service; connections do.
func (m *Manager) Use(name string) (release func()) {
	s, ok := m.lookup(name)
	if !ok {
		return func() {}
	}
	return s.activity.Begin()
}

// Up starts the Service called name and waits until it is ready. It goes
// Idle like a Service started by a connection.
func (m *Manager) Up(ctx context.Context, name string) error {
	s, ok := m.lookup(name)
	if !ok {
		return fmt.Errorf("no Service %s", name)
	}
	_, err := s.ensure(ctx)
	return err
}

// Web starts the Service called name if needed, like Up, and returns the
// host address its engine's web UI (Engine.WebPort) is published on, for
// the proxy to forward to.
func (m *Manager) Web(ctx context.Context, name string) (string, error) {
	s, ok := m.lookup(name)
	if !ok {
		return "", fmt.Errorf("no Service %s", name)
	}
	if s.Engine.WebPort == 0 {
		return "", fmt.Errorf("Service %s has no web UI", name)
	}
	for attempt := 0; ; attempt++ {
		addr, err := s.ensure(ctx)
		if err != nil {
			return "", err
		}
		web, running, err := m.opts.Engine.Running(ctx, s.spec.Name, s.Engine.WebPort)
		switch {
		case err != nil:
			return "", fmt.Errorf("finding Service %s's web UI: %w", name, err)
		case running && web != "":
			return web, nil
		case attempt > 0:
			return "", fmt.Errorf("Service %s isn't running", name)
		}
		// Stopped between ensure and now: start it again.
		s.forget(addr)
	}
}

// Down stops the Service called name and closes its connections. The next
// connection starts it again.
func (m *Manager) Down(ctx context.Context, name string) error {
	s, ok := m.lookup(name)
	if !ok {
		return fmt.Errorf("no Service %s", name)
	}
	return s.stop(ctx, Stopped)
}

// Status is the Service called name's Status.
func (m *Manager) Status(name string) (Status, bool) {
	s, ok := m.lookup(name)
	if !ok {
		return Status{}, false
	}
	return s.status(), true
}

// List is every Service's Status, sorted by name.
func (m *Manager) List() []Status {
	m.mu.Lock()
	all := make([]*managed, 0, len(m.services))
	for _, s := range m.services {
		all = append(all, s)
	}
	m.mu.Unlock()
	out := make([]Status, 0, len(all))
	for _, s := range all {
		out = append(out, s.status())
	}
	slices.SortFunc(out, func(a, b Status) int { return strings.Compare(a.Name(), b.Name()) })
	return out
}

// Close stops listening, closes every connection and stops every Service
// that is running.
func (m *Manager) Close(ctx context.Context) {
	m.mu.Lock()
	m.closed = true
	all := make([]*managed, 0, len(m.services))
	for _, s := range m.services {
		all = append(all, s)
	}
	m.mu.Unlock()
	m.cancel()
	for _, s := range all {
		s.activity.Reset()
		for _, ln := range s.listeners {
			ln.Close()
		}
		s.closeConns()
	}
	m.wg.Wait()
	var wg sync.WaitGroup
	for _, s := range all {
		if s.status().State == Running {
			wg.Go(func() { s.stop(ctx, Stopped) })
		}
	}
	wg.Wait()
}

func (s *managed) status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	host := ""
	if len(s.m.opts.Hosts) > 0 {
		host = s.m.opts.Hosts[0]
	}
	return Status{Service: s.Service, Host: host, State: s.state, Error: s.err}
}

// prepare adopts the container if it is already running, so it goes Idle,
// and otherwise pulls its image.
func (s *managed) prepare() {
	ctx := s.m.ctx
	addr, running, err := s.m.opts.Engine.Running(ctx, s.spec.Name, s.spec.Port)
	if err != nil {
		s.m.log.Warn("checking Service", "service", s.Name(), "err", err)
		return
	}
	if running {
		s.lifecycle.Lock()
		s.mu.Lock()
		if s.state == Stopped {
			s.state, s.addr = Running, addr
		}
		s.mu.Unlock()
		s.lifecycle.Unlock()
		s.activity.Touch()
		return
	}
	if err := s.m.opts.Engine.Pull(ctx, s.spec.Image); err != nil && ctx.Err() == nil {
		s.m.log.Warn("pulling Service image", "service", s.Name(), "image", s.spec.Image, "err", err)
	}
}

func (s *managed) accept(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				s.m.log.Error("accepting Service connection", "service", s.Name(), "err", err)
			}
			return
		}
		s.m.wg.Go(func() { s.handle(conn) })
	}
}

// handle holds a client connection, without reading it, until the Service
// is ready, then forwards it to the container.
func (s *managed) handle(conn net.Conn) {
	defer conn.Close()
	end := s.activity.Begin()
	defer end()
	if !s.track(conn) {
		return
	}
	defer s.untrack(conn)
	backend, err := s.connect(s.m.ctx)
	if err != nil {
		if s.m.ctx.Err() == nil {
			s.m.log.Error("starting Service", "service", s.Name(), "err", err)
		}
		return
	}
	defer backend.Close()
	splice(conn, backend)
}

// connect returns a connection to the ready container, starting it if
// needed. If the container was stopped behind Portby's back, it is started
// again.
func (s *managed) connect(ctx context.Context) (net.Conn, error) {
	for attempt := 0; ; attempt++ {
		addr, err := s.ensure(ctx)
		if err != nil {
			return nil, err
		}
		var d net.Dialer
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, err := d.DialContext(dctx, "tcp", addr)
		cancel()
		if err == nil || attempt > 0 {
			return conn, err
		}
		s.forget(addr)
	}
}

// forget marks the Service stopped if it is still running at addr, which
// stopped answering.
func (s *managed) forget(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == Running && s.addr == addr {
		s.state, s.addr = Stopped, ""
	}
}

// ensure starts the container unless it is running, waits until it is
// ready and returns its address.
func (s *managed) ensure(ctx context.Context) (string, error) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	running, addr := s.state == Running, s.addr
	s.mu.Unlock()
	if running {
		// Check with the engine rather than trusting the address: the
		// container may have been stopped or restarted behind Portby's
		// back, and its old port taken by something else. If the engine
		// can't say, the address is still the best guess.
		now, ok, err := s.m.opts.Engine.Running(ctx, s.spec.Name, s.spec.Port)
		switch {
		case err != nil:
			return addr, nil
		case ok:
			s.mu.Lock()
			s.addr = now
			s.mu.Unlock()
			return now, nil
		}
	}
	s.mu.Lock()
	s.state = Starting
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, s.m.opts.StartTimeout)
	defer cancel()
	addr, err := s.m.opts.Engine.Start(ctx, s.spec)
	if err == nil {
		err = s.waitReady(ctx, addr, s.Engine.Probe)
	}
	if err == nil && s.Engine.WebPort != 0 {
		err = s.waitWebReady(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.state, s.err = Stopped, err.Error()
		return "", fmt.Errorf("Service %s could not be started: %w", s.Name(), err)
	}
	s.state, s.addr, s.err = Running, addr, ""
	s.activity.SetTimeout(s.m.idleTimeout())
	s.activity.Touch()
	s.m.log.Info("Service started", "service", s.Name(), "container", s.spec.Name)
	return addr, nil
}

// probeInterval is how often a starting Service is probed.
const probeInterval = 50 * time.Millisecond

// waitWebReady waits until the engine's web UI answers HTTP, so the first
// page of it doesn't fail while the engine is starting.
func (s *managed) waitWebReady(ctx context.Context) error {
	addr, running, err := s.m.opts.Engine.Running(ctx, s.spec.Name, s.Engine.WebPort)
	if err != nil {
		return err
	}
	if !running || addr == "" {
		return fmt.Errorf("its web UI (port %d) isn't published", s.Engine.WebPort)
	}
	return s.waitReady(ctx, addr, probeHTTP)
}

// probeHTTP sends a request for /: any HTTP response means the server is
// up.
func probeHTTP(ctx context.Context, conn net.Conn) error {
	if _, err := io.WriteString(conn, "GET / HTTP/1.0\r\nHost: localhost\r\n\r\n"); err != nil {
		return err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	if !strings.HasPrefix(line, "HTTP/") {
		return fmt.Errorf("http: %s", strings.TrimSpace(line))
	}
	return nil
}

// waitReady probes addr with probe until the engine answers.
func (s *managed) waitReady(ctx context.Context, addr string, probe func(context.Context, net.Conn) error) error {
	for {
		err := s.probe(ctx, addr, probe)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("it wasn't ready within %s (last check: %v)", s.m.opts.StartTimeout, err)
		case <-time.After(probeInterval):
		}
	}
}

func (s *managed) probe(ctx context.Context, addr string, probe func(context.Context, net.Conn) error) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	return probe(ctx, conn)
}

// goIdle is called by the Activity Tracker once the Service has had no
// connections and no Worktree using it for its idle timeout.
func (s *managed) goIdle() {
	if s.m.ctx.Err() != nil {
		return
	}
	if err := s.stop(s.m.ctx, Idle); err != nil {
		s.m.log.Error("stopping idle Service", "service", s.Name(), "err", err)
		return
	}
}

// stop stops the container, if it is running or starting, and leaves the
// Service in state.
func (s *managed) stop(ctx context.Context, state string) error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	wasUp := s.state == Running
	s.mu.Unlock()
	if !wasUp && state == Idle {
		return nil
	}
	if state == Stopped {
		s.activity.Reset()
		s.closeConns()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), container.StopTimeout+20*time.Second)
	defer cancel()
	if err := s.m.opts.Engine.Stop(ctx, s.spec.Name); err != nil {
		return fmt.Errorf("stopping Service %s: %w", s.Name(), err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state, s.addr = state, ""
	if wasUp {
		s.m.log.Info("Service stopped", "service", s.Name(), "state", state)
	}
	return nil
}

func (s *managed) track(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m.ctx.Err() != nil {
		return false
	}
	s.conns[conn] = true
	return true
}

func (s *managed) untrack(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
}

func (s *managed) closeConns() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		c.Close()
	}
}

// splice copies both ways until both directions are done, passing on each
// half-close.
func splice(client, backend net.Conn) {
	var wg sync.WaitGroup
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if tc, ok := dst.(interface{ CloseWrite() error }); ok {
			tc.CloseWrite()
		} else {
			dst.Close()
		}
	}
	wg.Go(func() { cp(backend, client) })
	wg.Go(func() { cp(client, backend) })
	wg.Wait()
}
