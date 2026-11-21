package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/lanewayhq/laneway/internal/rootdomain"
)

// Redirect is the plain HTTP listener that sends browsers to the same URL
// over HTTPS, so typing a Domain such as shop.localhost (which a browser may
// try as http://) still reaches it.
type Redirect struct {
	server *http.Server
	addr   net.Addr
}

// StartRedirect listens on addr, a loopback address, and redirects every
// request for the Root Domain or a name under it to https:// on httpsPort
// (left out of the URL when it is 443). Requests for other names get a 404:
// Laneway serves nothing else. rootDomain is read on every request, so it
// may change while the listener runs.
func StartRedirect(addr string, httpsPort string, rootDomain func() string) (*Redirect, error) {
	if err := checkLoopback(addr); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	r := &Redirect{addr: ln.Addr(), server: &http.Server{
		Handler:           redirectHandler(httpsPort, rootDomain),
		ReadHeaderTimeout: 10 * time.Second,
	}}
	go func() {
		if err := r.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			ln.Close()
		}
	}()
	return r, nil
}

func redirectHandler(httpsPort string, rootDomain func() string) http.Handler {
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
		if httpsPort != "443" {
			host = net.JoinHostPort(host, httpsPort)
		}
		http.Redirect(w, req, "https://"+host+req.URL.RequestURI(), http.StatusPermanentRedirect)
	})
}

// Addr is the address it listens on.
func (r *Redirect) Addr() net.Addr { return r.addr }

// Stop closes the listener and waits for in-flight requests until ctx ends.
func (r *Redirect) Stop(ctx context.Context) error { return r.server.Shutdown(ctx) }
