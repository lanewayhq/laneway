package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

// Redirect is the plain HTTP listener that sends browsers to the same URL
// over HTTPS, so typing shop.localhost (which a browser may try as
// http://) still reaches the Domain.
type Redirect struct {
	server *http.Server
	addr   net.Addr
}

// StartRedirect listens on addr, a loopback address, and redirects every
// request for localhost or a *.localhost name to https:// on httpsPort
// (left out of the URL when it is 443). Requests for other names get a 404:
// Laneway serves nothing else.
func StartRedirect(addr string, httpsPort string) (*Redirect, error) {
	if err := checkLoopback(addr); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	r := &Redirect{addr: ln.Addr(), server: &http.Server{
		Handler:           redirectHandler(httpsPort),
		ReadHeaderTimeout: 10 * time.Second,
	}}
	go func() {
		if err := r.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			ln.Close()
		}
	}()
	return r, nil
}

func redirectHandler(httpsPort string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		host := req.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		if host != "localhost" && !strings.HasSuffix(host, ".localhost") {
			http.Error(w, "Laneway only serves localhost Domains", http.StatusNotFound)
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
