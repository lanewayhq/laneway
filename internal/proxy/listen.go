package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
)

// fanIn is a net.Listener accepting connections from several listeners: the
// loopback one it starts with, and extra addresses added and removed while
// it runs (Listen, Unlisten). Caddy and the plain-HTTP server each serve one,
// so the set of addresses changes without restarting either.
type fanIn struct {
	primary net.Listener
	conns   chan net.Conn
	closed  chan struct{}

	mu     sync.Mutex
	extra  map[netip.Addr]net.Listener
	isDone bool
}

func newFanIn(primary net.Listener) *fanIn {
	f := &fanIn{primary: primary, conns: make(chan net.Conn), closed: make(chan struct{}),
		extra: map[netip.Addr]net.Listener{}}
	go f.acceptFrom(primary)
	return f
}

// acceptFrom hands ln's connections to Accept until ln is closed.
func (f *fanIn) acceptFrom(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			select {
			case <-f.closed:
				return
			case <-time.After(10 * time.Millisecond):
				continue
			}
		}
		select {
		case f.conns <- conn:
		case <-f.closed:
			conn.Close()
			return
		}
	}
}

func (f *fanIn) Accept() (net.Conn, error) {
	select {
	case conn := <-f.conns:
		return conn, nil
	case <-f.closed:
		return nil, net.ErrClosed
	}
}

// Addr is the loopback address.
func (f *fanIn) Addr() net.Addr { return f.primary.Addr() }

func (f *fanIn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.isDone {
		return nil
	}
	f.isDone = true
	close(f.closed)
	err := f.primary.Close()
	for ip, ln := range f.extra {
		ln.Close()
		delete(f.extra, ip)
	}
	return err
}

// Listen also accepts connections on ip, at the loopback address's port. It
// does nothing if it already does.
func (f *fanIn) Listen(ip netip.Addr) error {
	if err := checkExtra(ip); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.isDone {
		return net.ErrClosed
	}
	if _, ok := f.extra[ip]; ok {
		return nil
	}
	_, port, err := net.SplitHostPort(f.primary.Addr().String())
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(ip.String(), port))
	if err != nil {
		return err
	}
	f.extra[ip] = ln
	go f.acceptFrom(ln)
	return nil
}

// Unlisten stops accepting new connections on ip. Connections already
// accepted there carry on.
func (f *fanIn) Unlisten(ip netip.Addr) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ln, ok := f.extra[ip]; ok {
		ln.Close()
		delete(f.extra, ip)
	}
}

// checkExtra refuses the addresses that mean every interface, including
// 0.0.0.0 written as an IPv4-mapped IPv6 address (::ffff:0.0.0.0): Laneway
// listens only on addresses it is given (ADR-0009).
func checkExtra(ip netip.Addr) error {
	if !ip.IsValid() {
		return errors.New("no address to listen on")
	}
	if ip.Unmap().IsUnspecified() {
		return fmt.Errorf("Laneway never listens on every interface, so not on %s", ip)
	}
	return nil
}

// fanInNetwork is the Caddy network a Proxy's server listens on: its
// "host" is the Proxy's id, which fanIns maps to the listener.
const fanInNetwork = "laneway"

var fanIns sync.Map // Proxy id → *fanIn

func init() {
	caddy.RegisterNetwork(fanInNetwork, func(_ context.Context, _, host, _ string, _ uint, _ net.ListenConfig) (any, error) {
		f, ok := fanIns.Load(host)
		if !ok {
			return nil, fmt.Errorf("no Laneway proxy with id %q", host)
		}
		return f, nil
	})
}
