// Package service is the Service Manager (ADR-0001, ADR-0005): Services are
// shared containers, one per Engine and version (redis:7), started on the
// first connection to the port Laneway listens on for them and stopped once
// they have had no connections and no running Worktree depending on them
// for the Service idle timeout.
//
// What a Service of an Engine runs, and when it is ready, is the Engine's
// manifest (package engine): the Manager turns it into a container and runs
// its readiness probes.
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
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lanewayhq/laneway/internal/activity"
	"github.com/lanewayhq/laneway/internal/clock"
	"github.com/lanewayhq/laneway/internal/container"
	"github.com/lanewayhq/laneway/internal/engine"
)

// Ref names a Service: an engine (by name) at a version, such as redis at
// "7". Config, the Registry and the API all name Services this way.
type Ref struct {
	Engine  string `json:"engine"`
	Version string `json:"version"`
}

// Name is how the CLI and API name the Service, such as "redis:7".
func (r Ref) Name() string { return r.Engine + ":" + r.Version }

// Service is an Engine at a version, listening on Port.
type Service struct {
	Engine  *engine.Manifest
	Version string
	Port    int
}

// Ref names s.
func (s Service) Ref() Ref { return Ref{Engine: s.Engine.Name, Version: s.Version} }

// Name is how the CLI and API name the Service, such as "redis:7".
func (s Service) Name() string { return s.Ref().Name() }

// States a Service can be in.
const (
	// Stopped: its container isn't running (or Laneway doesn't know it is).
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
	// "laneway": redis:7 runs as laneway-redis-7.
	Prefix string
	// IdleTimeout is read every time a Service starts; nil or 0 means
	// DefaultIdleTimeout.
	IdleTimeout func() time.Duration
	// StartTimeout is DefaultStartTimeout if 0.
	StartTimeout time.Duration
	// PortHolders describes who holds a port Laneway can't listen on, for
	// the Service's error, such as "nginx (pid 4242)"; "" (or nil) if it
	// can't tell.
	PortHolders func(port int) string
	// OnStarted and OnStopped, if set, are told every time a Service's
	// container is started and ready, or stopped after running (by hand,
	// Idle or Close). They are called while the Service can't start or
	// stop, so they must return quickly and must not call the Manager.
	OnStarted, OnStopped func(Status)
}

// Status is what a Service is doing.
type Status struct {
	Service
	// Host is where clients connect, with Service.Port.
	Host  string
	State string
	// Error is why the Service can't be used: Laneway can't listen on its
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
	opts.Prefix = cmp.Or(opts.Prefix, "laneway")
	opts.StartTimeout = cmp.Or(opts.StartTimeout, DefaultStartTimeout)
	m := &Manager{opts: opts, log: opts.Log, services: map[string]*managed{}}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	return m
}

// managed is one Service the Manager serves.
type managed struct {
	// Service and spec change when Update gives the Service a new
	// manifest: they are written holding both lifecycle and mu, so read
	// them holding either (see current).
	Service
	spec container.Spec
	// name and container are the Service's and its container's names,
	// which never change.
	name, container string

	m         *Manager
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
	// unserved is set once Unserve stops serving it: nothing may start
	// its container again.
	unserved bool
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
	spec := m.spec(svc)
	s := &managed{Service: svc, m: m, spec: spec, name: svc.Name(), container: spec.Name, state: Stopped, conns: map[net.Conn]bool{}}
	s.activity = activity.New(m.opts.Clock, m.idleTimeout(), s.goIdle)
	m.services[svc.Name()] = s
	for i, host := range m.opts.Hosts {
		ln, err := listen(net.JoinHostPort(host, strconv.Itoa(svc.Port)))
		if err != nil {
			if i == 0 {
				s.err = fmt.Sprintf("Laneway can't listen on port %d: %v", svc.Port, err)
				if m.opts.PortHolders != nil {
					if holders := m.opts.PortHolders(svc.Port); holders != "" {
						s.err += "; it is in use by " + holders
					}
				}
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

// listenRetry is how long listen waits for an address in use to be freed.
const listenRetry = time.Second

// listen listens on the TCP address addr, waiting up to listenRetry for it
// if it is in use. A port is often in use for a moment after its listener
// closes: a program starting holds a copy of every socket of the process
// that starts it until it runs, so the listener of a Service the daemon
// served before it restarted can outlive its Close by a few milliseconds.
// A port another program listens on is still in use after the wait.
func listen(addr string) (net.Listener, error) {
	deadline := time.Now().Add(listenRetry)
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil || !errors.Is(err, syscall.EADDRINUSE) || time.Now().After(deadline) {
			return ln, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// spec is the container that runs svc. Its Image is "" if svc's Engine
// has no image for its version (see engine.Manifest.ImageRef).
func (m *Manager) spec(svc Service) container.Spec {
	e := svc.Engine
	name := m.Volume(svc.Ref()) // the container is named like its volume
	image, _ := e.ImageRef(svc.Version)
	spec := container.Spec{
		Name:         name,
		Image:        image,
		Labels:       map[string]string{"dev.laneway.service": svc.Name()},
		Port:         e.Port,
		Volume:       name,
		VolumeTarget: e.DataDir,
		Env:          e.ContainerEnv(svc.Version),
		Cmd:          e.ContainerCmd(svc.Version),
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

// Pull makes sure the image of the Service called name is available,
// downloading it if it isn't, within ctx rather than Up's start timeout:
// call it before Up when a first download mustn't count against that.
func (m *Manager) Pull(ctx context.Context, name string) error {
	s, ok := m.lookup(name)
	if !ok {
		return fmt.Errorf("no Service %s", name)
	}
	_, spec := s.current()
	return m.opts.Engine.Pull(ctx, spec.Image)
}

// Exec runs cmd inside the container of the Service called name, starting
// it first if needed (like Up), and returns what it printed and how it
// exited. Callers keep the Service from going Idle meanwhile with Use.
func (m *Manager) Exec(ctx context.Context, name string, cmd []string) (container.Output, error) {
	s, ok := m.lookup(name)
	if !ok {
		return container.Output{}, fmt.Errorf("no Service %s", name)
	}
	if _, err := s.ensure(ctx); err != nil {
		return container.Output{}, err
	}
	return m.opts.Engine.Exec(ctx, s.container, cmd)
}

// Forward starts the Service called name if needed, like Up, and returns
// the host address its container port port (the Engine's Port or WebPort,
// as a Service Domain's is) is published on, for the proxy to forward to.
func (m *Manager) Forward(ctx context.Context, name string, port int) (string, error) {
	s, ok := m.lookup(name)
	if !ok {
		return "", fmt.Errorf("no Service %s", name)
	}
	for attempt := 0; ; attempt++ {
		svc, _ := s.current()
		if port == 0 || (port != svc.Engine.Port && port != svc.Engine.WebPort) {
			return "", fmt.Errorf("Service %s doesn't publish port %d", name, port)
		}
		addr, err := s.ensure(ctx)
		if err != nil {
			return "", err
		}
		published, running, err := m.opts.Engine.Running(ctx, s.container, port)
		switch {
		case err != nil:
			return "", fmt.Errorf("finding Service %s's port %d: %w", name, port, err)
		case running && published != "":
			return published, nil
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
	_, spec := s.current()
	addr, running, err := s.m.opts.Engine.Running(ctx, s.container, spec.Port)
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
	s.pull(spec.Image)
}

// pull pulls image in the background of the daemon's life, so the
// Service's next start doesn't wait for a download.
func (s *managed) pull(image string) {
	if image == "" {
		return // its Engine has no image for its version: starting it says so
	}
	ctx := s.m.ctx
	if err := s.m.opts.Engine.Pull(ctx, image); err != nil && ctx.Err() == nil {
		s.m.log.Warn("pulling Service image", "service", s.Name(), "image", image, "err", err)
	}
}

// current is the Service and its container's spec, which Update changes.
func (s *managed) current() (Service, container.Spec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Service, s.spec
}

// Name is how the CLI and API name the Service, such as "redis:7".
func (s *managed) Name() string { return s.name }

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
// needed. If the container was stopped behind Laneway's back, it is started
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
	running, addr, unserved := s.state == Running, s.addr, s.unserved
	s.mu.Unlock()
	if unserved {
		return "", fmt.Errorf("Service %s is no longer served", s.Name())
	}
	if running {
		// Check with the engine rather than trusting the address: the
		// container may have been stopped or restarted behind Laneway's
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
	var err error
	if s.spec.Image == "" {
		_, err = s.Engine.ImageRef(s.Version)
	} else {
		addr, err = s.m.opts.Engine.Start(ctx, s.spec)
	}
	if err == nil {
		err = s.waitReady(ctx, addr)
	}
	s.mu.Lock()
	if err != nil {
		s.state, s.err = Stopped, err.Error()
		s.mu.Unlock()
		return "", fmt.Errorf("Service %s could not be started: %w", s.Name(), err)
	}
	s.state, s.addr, s.err = Running, addr, ""
	s.activity.SetTimeout(s.m.idleTimeout())
	s.activity.Touch()
	s.mu.Unlock()
	s.m.log.Info("Service started", "service", s.Name(), "container", s.spec.Name)
	s.tell(s.m.opts.OnStarted)
	return addr, nil
}

// probeInterval is how often a starting Service is probed.
const probeInterval = 50 * time.Millisecond

// waitReady waits until every one of the Engine's readiness probes passes,
// in order, with the container's Port published at addr.
func (s *managed) waitReady(ctx context.Context, addr string) error {
	for _, p := range s.Engine.Ready {
		for {
			err := s.probe(ctx, addr, p)
			if err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("it wasn't ready within %s (last check: %v)", s.m.opts.StartTimeout, err)
			case <-time.After(probeInterval):
			}
		}
	}
	return nil
}

// probe checks p once.
func (s *managed) probe(ctx context.Context, addr string, p engine.Probe) error {
	if p.Kind == engine.Command {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		out, err := s.m.opts.Engine.Exec(ctx, s.spec.Name, s.Engine.Expand(p.Command, map[string]string{"version": s.Version}))
		if err != nil {
			return err
		}
		if out.ExitCode != 0 {
			return fmt.Errorf("%s exited with status %d: %s", p.Command[0], out.ExitCode,
				cmp.Or(strings.TrimSpace(string(out.Stderr)), strings.TrimSpace(string(out.Stdout))))
		}
		return nil
	}
	if p.Port != s.Engine.Port {
		published, running, err := s.m.opts.Engine.Running(ctx, s.spec.Name, p.Port)
		if err != nil {
			return err
		}
		if !running || published == "" {
			return fmt.Errorf("port %d isn't published", p.Port)
		}
		addr = published
	}
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
	r := bufio.NewReader(conn)
	switch p.Kind {
	case engine.Banner:
		line, err := r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading its banner: %w", err)
		}
		if !strings.HasPrefix(line, p.Expect) {
			return fmt.Errorf("its banner is %q, not %s…", strings.TrimSpace(line), p.Expect)
		}
	case engine.RedisPing:
		if _, err := io.WriteString(conn, "PING\r\n"); err != nil {
			return err
		}
		line, err := r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("PING: %w", err)
		}
		if strings.HasPrefix(line, "-LOADING") || !(strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) {
			return fmt.Errorf("PING: %s", strings.TrimSpace(line))
		}
	case engine.HTTP:
		if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.0\r\nHost: localhost\r\n\r\n", p.Path); err != nil {
			return err
		}
		resp, err := http.ReadResponse(r, nil)
		if err != nil {
			return fmt.Errorf("GET %s: %w", p.Path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != p.Status {
			return fmt.Errorf("GET %s: %s, want %d", p.Path, resp.Status, p.Status)
		}
	}
	return nil
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
	return s.stopLocked(ctx, state)
}

// stopLocked is stop for a caller holding s.lifecycle.
func (s *managed) stopLocked(ctx context.Context, state string) error {
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
	s.state, s.addr = state, ""
	s.mu.Unlock()
	if wasUp {
		s.m.log.Info("Service stopped", "service", s.Name(), "state", state)
		s.tell(s.m.opts.OnStopped)
	}
	return nil
}

// tell calls f, Options.OnStarted or OnStopped, with the Service's status, if
// it is set. The caller holds s.lifecycle.
func (s *managed) tell(f func(Status)) {
	if f != nil {
		f(s.status())
	}
}

func (s *managed) track(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m.ctx.Err() != nil || s.unserved {
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
