// Package dashboard is Laneway's web Dashboard: a page built into the binary,
// served by the daemon at https://laneway.localhost, that talks to the same
// JSON API as the CLI (package api), mounted under /api.
//
// The frontend is plain HTML, CSS and JavaScript modules in static/, with no
// build step and no dependencies: what is in the repository is what the
// browser runs. See static/app.js for how panels are added.
package dashboard

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"

	"github.com/lanewayhq/laneway/internal/api"
)

// DefaultDomain is where the daemon serves the Dashboard.
const DefaultDomain = "laneway.localhost"

//go:embed static
var static embed.FS

// Handler serves the Dashboard at domain: its files, and routes (the
// daemon's API routes) under /api, so GET /api/v1/projects is GET
// /v1/projects. Requests that can change state only reach routes from the
// Dashboard's own pages (see sameOrigin).
func Handler(domain string, routes http.Handler) http.Handler {
	files, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", sameOrigin(domain, http.StripPrefix("/api", routes)))
	mux.Handle("/", pageHeaders(http.FileServerFS(files)))
	return mux
}

// pageHeaders sets the headers of the Dashboard's files. No caching, so a new
// daemon's Dashboard is what the next reload shows. Its own scripts and API
// only, and no framing: a page that framed the Dashboard could trick a click
// on its buttons, which sameOrigin would then let through.
func pageHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// sameOrigin rejects every request but GET and HEAD unless its Origin header
// is the Dashboard's own origin, https://<domain> (plus the port the browser
// used, if not 443). Browsers send Origin with every such request, and other
// pages can't forge it, so a page elsewhere (another site, or a Project's own
// app at its Domain) can't start or stop anything through a visitor's
// browser. Clients without an Origin, like curl, are rejected too: the CLI
// talks to the daemon over its Unix socket instead.
//
// Reads need no check: without CORS headers, a page elsewhere can't read
// the answer.
func sameOrigin(domain string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != domain || r.Header.Get("Origin") != "https://"+r.Host {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(api.ErrorResponse{Error: api.Error{
				Code:    api.CodeForbidden,
				Message: "only the Dashboard's own pages at https://" + domain + " can change anything through it",
			}})
			return
		}
		next.ServeHTTP(w, r)
	})
}
