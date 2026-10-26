// Package containertest is the fake container engine for Seam 1 tests. It
// records every call, and "runs" a container by serving its port from an
// in-process server on 127.0.0.1 (port 0) that speaks just enough of the
// image's protocol, with data kept in memory across stops like a volume.
//
// Servers are chosen by image: see Fake.Serve. Redis is built in; adding a
// Service engine (Postgres, MySQL, Mailpit) to the fake means writing a
// Server for its image.
package containertest

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"

	"github.com/mrsafalpiya/portby.dev/internal/container"
)

// Server serves one fake container's port on ln until ln is closed. spec is
// the container's Spec, and data its "volume", which survives the container
// being stopped and started again. Each connection should be handled in its
// own goroutine; closing ln closes them (the Fake tracks them).
type Server func(ln net.Listener, spec container.Spec, data *Volume)

// Volume is a fake container's data. It is safe for concurrent use.
type Volume struct {
	mu    sync.Mutex
	data  map[string]string
	lists map[string][]string
}

// Get returns the value stored under key.
func (v *Volume) Get(key string) (string, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	value, ok := v.data[key]
	return value, ok
}

// Set stores value under key.
func (v *Volume) Set(key, value string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.data[key] = value
}

// Append adds value at the end of the list called key.
func (v *Volume) Append(key, value string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.lists == nil {
		v.lists = map[string][]string{}
	}
	v.lists[key] = append(v.lists[key], value)
}

// List returns the list called key, oldest first.
func (v *Volume) List(key string) []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.lists[key])
}

// Fake is a container engine whose containers are in-process servers. It is
// safe for concurrent use.
type Fake struct {
	mu          sync.Mutex
	servers     map[string]Server         // image repository (without tag) → Server
	extra       map[string]map[int]Server // image repository → extra port → Server
	containers  map[string]*fakeContainer
	calls       []string
	hold        chan struct{} // while non-nil, Start waits for it to close
	unavailable error
}

type fakeContainer struct {
	spec   container.Spec
	volume *Volume
	ln     net.Listener         // nil when stopped
	extra  map[int]net.Listener // the ExtraPorts' listeners while running
	conns  *connSet
}

// New returns a Fake that can run Redis and Mailpit images.
func New() *Fake {
	f := &Fake{servers: map[string]Server{}, extra: map[string]map[int]Server{}, containers: map[string]*fakeContainer{}}
	f.Serve("docker.io/library/redis", Redis)
	f.Serve("docker.io/axllent/mailpit", MailpitSMTP)
	f.ServePort("docker.io/axllent/mailpit", 8025, MailpitWeb)
	return f
}

// Serve makes containers of image (a repository without a tag, such as
// "docker.io/library/redis") run server.
func (f *Fake) Serve(image string, server Server) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.servers[image] = server
}

// ServePort makes containers of image run server on port, one of their
// Spec's ExtraPorts. It shares the container's Volume with the Server of its
// main port.
func (f *Fake) ServePort(image string, port int, server Server) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.extra[image] == nil {
		f.extra[image] = map[int]Server{}
	}
	f.extra[image][port] = server
}

// Calls are the calls made so far, oldest first: "pull <image>",
// "start <name>", "stop <name>".
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// IsRunning reports whether the named container is running.
func (f *Fake) IsRunning(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.containers[name]
	return c != nil && c.ln != nil
}

// Containers names every container that exists, running or not, sorted.
func (f *Fake) Containers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for name := range f.containers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// HoldStarts makes every Start wait, as if the container were slow to
// start, until release is called.
func (f *Fake) HoldStarts() (release func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hold := make(chan struct{})
	f.hold = hold
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			if f.hold == hold {
				f.hold = nil
			}
			f.mu.Unlock()
			close(hold)
		})
	}
}

// SetUnavailable makes every call fail as if no engine could be reached,
// until it is called with nil.
func (f *Fake) SetUnavailable(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		err = fmt.Errorf("%w: %v", container.ErrUnavailable, err)
	}
	f.unavailable = err
}

// Kill stops the named container behind Portby's back, as `docker stop`
// would.
func (f *Fake) Kill(name string) {
	f.mu.Lock()
	c := f.containers[name]
	f.mu.Unlock()
	if c != nil {
		f.stop(c)
	}
}

func (f *Fake) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	return f.unavailable
}

func (f *Fake) Pull(ctx context.Context, image string) error {
	return f.record("pull " + image)
}

func (f *Fake) Start(ctx context.Context, spec container.Spec) (string, error) {
	if err := f.record("start " + spec.Name); err != nil {
		return "", err
	}
	f.mu.Lock()
	hold := f.hold
	f.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	repo, _, _ := strings.Cut(spec.Image, ":")
	f.mu.Lock()
	defer f.mu.Unlock()
	server, ok := f.servers[repo]
	if !ok {
		return "", fmt.Errorf("the fake container engine can't run %s", spec.Image)
	}
	c := f.containers[spec.Name]
	if c == nil {
		c = &fakeContainer{spec: spec, volume: &Volume{data: map[string]string{}}}
		f.containers[spec.Name] = c
	}
	if c.ln != nil {
		return c.ln.Addr().String(), nil
	}
	extra := map[int]Server{}
	for _, port := range spec.ExtraPorts {
		if extra[port] = f.extra[repo][port]; extra[port] == nil {
			return "", fmt.Errorf("the fake container engine can't serve port %d of %s", port, spec.Image)
		}
	}
	conns := &connSet{conns: map[net.Conn]bool{}}
	listen := func() (net.Listener, error) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		return &trackingListener{Listener: ln, conns: conns}, nil
	}
	ln, err := listen()
	if err != nil {
		return "", err
	}
	c.extra = map[int]net.Listener{}
	for port, server := range extra {
		eln, err := listen()
		if err != nil {
			ln.Close()
			for _, l := range c.extra {
				l.Close()
			}
			return "", err
		}
		c.extra[port] = eln
		go server(eln, c.spec, c.volume)
	}
	c.conns, c.ln = conns, ln
	go server(c.ln, c.spec, c.volume)
	return ln.Addr().String(), nil
}

func (f *Fake) Stop(ctx context.Context, name string) error {
	if err := f.record("stop " + name); err != nil {
		return err
	}
	f.mu.Lock()
	c := f.containers[name]
	f.mu.Unlock()
	if c != nil {
		f.stop(c)
	}
	return nil
}

func (f *Fake) stop(c *fakeContainer) {
	f.mu.Lock()
	ln, extra, conns := c.ln, c.extra, c.conns
	c.ln, c.extra = nil, nil
	f.mu.Unlock()
	if ln != nil {
		ln.Close()
		for _, l := range extra {
			l.Close()
		}
		conns.closeAll()
	}
}

func (f *Fake) Running(ctx context.Context, name string, port int) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable != nil {
		return "", false, f.unavailable
	}
	c := f.containers[name]
	if c == nil || c.ln == nil {
		return "", false, nil
	}
	if ln, ok := c.extra[port]; ok {
		return ln.Addr().String(), true, nil
	}
	return c.ln.Addr().String(), true, nil
}

// connSet is a fake container's open connections, closed when it stops.
type connSet struct {
	mu     sync.Mutex
	conns  map[net.Conn]bool
	closed bool
}

func (s *connSet) add(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = true
	return true
}

func (s *connSet) remove(c net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, c)
}

func (s *connSet) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for c := range s.conns {
		c.Close()
	}
}

// trackingListener records accepted connections so stopping the container
// closes them.
type trackingListener struct {
	net.Listener
	conns *connSet
}

func (l *trackingListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.conns.add(c) {
			return &trackedConn{Conn: c, conns: l.conns}, nil
		}
		c.Close() // the container is stopping
	}
}

type trackedConn struct {
	net.Conn
	conns *connSet
}

func (c *trackedConn) Close() error {
	c.conns.remove(c.Conn)
	return c.Conn.Close()
}
