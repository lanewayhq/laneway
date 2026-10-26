package proxy

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddytls"
)

// Two small Caddy modules connect the fixed Caddy config to Portby: a handler
// that asks the Router where each request goes, and a permission check for
// on-demand certificates.

func init() {
	caddy.RegisterModule(routeHandler{})
	caddy.RegisterModule(certPermission{})
}

// routeHandler (http.handlers.portby) runs before reverse_proxy. It answers
// requests for unknown Domains itself, and otherwise tells reverse_proxy which
// upstream to dial.
type routeHandler struct {
	// Proxy is the id of the Proxy whose Router to ask.
	Proxy string `json:"proxy"`

	router Router
}

func (routeHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.portby", New: func() caddy.Module { return new(routeHandler) }}
}

func (h *routeHandler) Provision(caddy.Context) error {
	r, err := routerFor(h.Proxy)
	h.router = r
	return err
}

func (h *routeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	domain := requestDomain(r)
	defer h.router.Begin(domain)()
	// Browsers are shown the starting page rather than left hanging; every
	// other client is held until the Process is up.
	addr, err := h.router.Upstream(r.Context(), domain, !isNavigation(r))
	if se, ok := errors.AsType[*StartError](err); ok {
		writeStartErrorPage(w, domain, se)
		return nil
	}
	if se, ok := errors.AsType[*SetupError](err); ok {
		writeSetupErrorPage(w, domain, se)
		return nil
	}
	switch {
	case errors.Is(err, ErrSettingUp):
		writeSettingUpPage(w, domain)
		return nil
	case errors.Is(err, ErrStarting):
		writeStartingPage(w, domain)
		return nil
	case errors.Is(err, ErrUnknownDomain):
		writePage(w, http.StatusNotFound, "Nothing is served here",
			fmt.Sprintf("Portby doesn't serve anything at <code>%s</code>.", html.EscapeString(domain)),
			"Run <code>portby status</code> to see the Linked Projects and their Domains.")
		return nil
	case err != nil:
		writePage(w, http.StatusBadGateway, "Can't reach this Domain's Process",
			fmt.Sprintf("Portby couldn't forward the request for <code>%s</code>:", html.EscapeString(domain)),
			"<pre>"+html.EscapeString(err.Error())+"</pre>")
		return nil
	}
	caddyhttp.SetVar(r.Context(), upstreamVar, addr)
	return next.ServeHTTP(w, r)
}

// requestDomain is the request's host without any port, lowercased.
func requestDomain(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func writePage(w http.ResponseWriter, status int, title string, paragraphs ...string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html>\n<html><head><meta charset=\"utf-8\"><title>%s · Portby</title></head><body>\n<h1>%s</h1>\n",
		html.EscapeString(title), html.EscapeString(title))
	for _, p := range paragraphs {
		fmt.Fprintf(w, "<p>%s</p>\n", p)
	}
	fmt.Fprint(w, "<p><small>Portby</small></p>\n</body></html>\n")
}

// certPermission (tls.permission.portby) allows on-demand certificates for
// localhost and its subdomains only. Unknown *.localhost names get a
// certificate too, so they can be shown the "nothing is served here" page.
type certPermission struct{}

func (certPermission) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "tls.permission.portby", New: func() caddy.Module { return new(certPermission) }}
}

func (certPermission) CertificateAllowed(_ context.Context, name string) error {
	name = strings.ToLower(name)
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return nil
	}
	return fmt.Errorf("%s is not a .localhost name", name)
}

var (
	_ caddyhttp.MiddlewareHandler = (*routeHandler)(nil)
	_ caddy.Provisioner           = (*routeHandler)(nil)
	_ caddytls.OnDemandPermission = (*certPermission)(nil)
)
