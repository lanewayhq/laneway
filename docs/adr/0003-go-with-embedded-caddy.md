# The daemon is written in Go and embeds Caddy

Portby is a single Go binary that runs as a rootless user daemon. Caddy is embedded as a library, not run as a separate program, to provide the reverse proxy, WebSocket forwarding, local CA and on-demand TLS certificates. The CLI talks to the daemon over a Unix socket, and the daemon also serves the dashboard at `portby.localhost`.

We considered Rust (hyper/pingora + rcgen) and TypeScript on Bun. Go plus Caddy gets proxying and certificate handling that are already proven, ships as one static binary, and has straightforward process supervision. Changing this later would mean rewriting the daemon.
