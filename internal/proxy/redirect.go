package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/lanewayhq/laneway/internal/rootdomain"
)

// Redirect is the plain HTTP listener that sends browsers to the same URL
// over HTTPS, so typing a Domain such as shop.localhost (which a browser may
// try as http://) still reaches it.
type Redirect struct {
	server   *http.Server
	listener *fanIn
}

// StartRedirect listens on addr, a loopback address, and redirects every
// request for the Root Domain or a name under it to https:// on httpsPort
// (left out of the URL when it is 443). Requests for other names get a 404:
// Laneway serves nothing else. rootDomain is read on every request, so it
// may change while the listener runs.
//
// plain picks the requests served over plain HTTP instead of redirected,
// such as the CA page a device opens before it trusts Laneway's CA: it
// returns their handler, or nil to redirect. A nil plain redirects every
// request.
func StartRedirect(addr string, httpsPort string, rootDomain func() string, plain func(host, path string) http.Handler) (*Redirect, error) {
	if err := checkLoopback(addr); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	r := &Redirect{listener: newFanIn(ln), server: &http.Server{
		Handler:           redirectHandler(httpsPort, rootDomain, plain),
		ReadHeaderTimeout: 10 * time.Second,
	}}
	go func() {
		if err := r.server.Serve(r.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			r.listener.Close()
		}
	}()
	return r, nil
}

func redirectHandler(httpsPort string, rootDomain func() string, plain func(host, path string) http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		host := req.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		if root := rootDomain(); !rootdomain.Covers(root, host) {
			http.Error(w, "Laneway only serves Domains under "+root, http.StatusNotFound)
			return
		}
		if plain != nil {
			if h := plain(host, req.URL.Path); h != nil {
				h.ServeHTTP(w, req)
				return
			}
		}
		if httpsPort != "443" {
			host = net.JoinHostPort(host, httpsPort)
		}
		http.Redirect(w, req, "https://"+host+req.URL.RequestURI(), http.StatusPermanentRedirect)
	})
}

// Addr is the loopback address it listens on.
func (r *Redirect) Addr() net.Addr { return r.listener.Addr() }

// Listen makes it listen on ip too, at the same port as on loopback, as
// Proxy.Listen does.
func (r *Redirect) Listen(ip netip.Addr) error { return r.listener.Listen(ip) }

// Unlisten stops it listening on ip, an address Listen added.
func (r *Redirect) Unlisten(ip netip.Addr) { r.listener.Unlisten(ip) }

// Stop closes the listener and waits for in-flight requests until ctx ends.
func (r *Redirect) Stop(ctx context.Context) error { return r.server.Shutdown(ctx) }
