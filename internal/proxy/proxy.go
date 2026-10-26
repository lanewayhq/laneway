// Package proxy is the HTTPS front door: Caddy, embedded as a library
// (ADR-0003), terminating TLS for every *.localhost Domain with certificates
// from Portby's local CA and reverse-proxying each request (WebSockets
// included) to the Process a Router picks.
//
// The Caddy config is fixed: routing decisions are made per request by the
// Router, so Projects and Processes coming and going never reload Caddy.
//
// Each Proxy is its own Caddy instance, built with caddy.ProvisionContext and
// started app by app rather than through caddy.Load, which allows only one
// config per process. That lets tests run several daemons in one process.
// Caddy still keeps some state process-wide, which shapes this package:
//
//   - Provisioning writes Caddy globals, so Start and Stop are serialized.
//   - The in-memory certificate cache is shared by every running instance and
//     ignores which CA issued a certificate. Stop throws the cache away so a
//     later Proxy never serves a stopped one's certificates, but two Proxies
//     running at once must not serve the same Domain, or one may hand out a
//     certificate signed by the other's CA.
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	// The Caddy modules the config below uses.
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
	_ "github.com/caddyserver/caddy/v2/modules/caddypki"
	_ "github.com/caddyserver/caddy/v2/modules/caddytls"
	_ "github.com/caddyserver/caddy/v2/modules/filestorage"
)

// Router decides where a request for a Domain goes.
type Router interface {
	// Upstream returns the loopback address ("127.0.0.1:<port>") of the
	// Process serving domain, starting it if needed. While it is starting,
	// Upstream waits for it to accept connections if wait is true, and
	// otherwise returns ErrStarting at once. It returns an error wrapping
	// ErrUnknownDomain if domain is not one of Portby's Domains; any other
	// error is shown to the user.
	Upstream(ctx context.Context, domain string, wait bool) (string, error)
	// Begin is called when a request for domain arrives, and the func it
	// returns once the request is over. For a WebSocket that is when the
	// connection closes, so an open connection keeps its Domain active.
	Begin(domain string) (end func())
}

var (
	// ErrUnknownDomain is returned by a Router for a Domain nothing is
	// served at.
	ErrUnknownDomain = errors.New("unknown Domain")
	// ErrStarting is returned by a Router, when asked not to wait, for a
	// Domain whose Process is still starting.
	ErrStarting = errors.New("starting")
)

// StartError is returned by a Router when a Domain's Primary Process failed
// to start. The proxy shows it as Portby's error page.
type StartError struct {
	// Process is the Primary Process's name.
	Process string
	// Command is the command it was started with, and Output its last
	// lines of output.
	Command string
	Output  []string
	// TimedOut is set when the Process was still running but hadn't
	// accepted a connection by the start timeout.
	TimedOut bool
	Err      error
}

func (e *StartError) Error() string { return e.Err.Error() }
func (e *StartError) Unwrap() error { return e.Err }

// Config configures a Proxy.
type Config struct {
	// Addr is the HTTPS listen address. It must be a loopback IP address;
	// port 0 picks a free port.
	Addr string
	// StorageDir holds the local CA and the certificates issued from it.
	StorageDir string
	Router     Router
	// LogErrors sends Caddy's own error logs to stderr. Otherwise they are
	// discarded.
	LogErrors bool
}

// Proxy is a running proxy.
type Proxy struct {
	id   string
	apps []caddy.App // in start order
	addr net.Addr
}

// The apps a Proxy runs, in the order they are started.
var appNames = []string{"pki", "tls", "http"}

// Start provisions and starts the proxy. It returns once the listener is
// bound, so Addr is the real address even when cfg.Addr asked for port 0.
func Start(cfg Config) (*Proxy, error) {
	if err := checkLoopback(cfg.Addr); err != nil {
		return nil, err
	}
	if cfg.Router == nil {
		return nil, errors.New("proxy: no Router")
	}
	var caddyCfg caddy.Config
	p := &Proxy{id: strconv.FormatInt(nextID.Add(1), 10)}
	if err := json.Unmarshal(caddyConfig(p.id, cfg), &caddyCfg); err != nil {
		return nil, fmt.Errorf("proxy: building Caddy config: %w", err)
	}
	routers.Store(p.id, cfg.Router)

	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	ctx, err := caddy.ProvisionContext(&caddyCfg)
	if err != nil {
		routers.Delete(p.id)
		return nil, fmt.Errorf("starting the HTTPS proxy: %w", err)
	}
	for _, name := range appNames {
		app, err := ctx.App(name)
		if err != nil {
			p.stop()
			return nil, fmt.Errorf("starting the HTTPS proxy: %w", err)
		}
		p.apps = append(p.apps, app.(caddy.App))
		if err := app.(caddy.App).Start(); err != nil {
			p.stop()
			return nil, fmt.Errorf("starting the HTTPS proxy on %s: %w", cfg.Addr, err)
		}
	}
	httpApp, _ := ctx.App("http")
	ln := httpApp.(*caddyhttp.App).Servers["https"].Listeners()
	if len(ln) == 0 {
		p.stop()
		return nil, fmt.Errorf("starting the HTTPS proxy on %s: no listener", cfg.Addr)
	}
	p.addr = ln[0].Addr()
	return p, nil
}

// Addr is the address the proxy is listening on.
func (p *Proxy) Addr() net.Addr {
	return p.addr
}

// Stop closes the listener and stops the Caddy apps, waiting a few seconds
// for in-flight requests.
func (p *Proxy) Stop() error {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	return p.stop()
}

// stop stops and cleans up the started apps. The caller holds lifecycleMu.
//
// caddy.ProvisionContext gives no way to cancel its context, which is what
// would normally run module cleanup, so the apps' Cleanup is called here.
// The TLS app's Cleanup is the one that matters: with no caddy.Load config
// active, it discards the shared certificate cache.
func (p *Proxy) stop() error {
	var errs []error
	for i := len(p.apps) - 1; i >= 0; i-- {
		errs = append(errs, p.apps[i].Stop())
	}
	for i := len(p.apps) - 1; i >= 0; i-- {
		if c, ok := p.apps[i].(caddy.CleanerUpper); ok {
			errs = append(errs, c.Cleanup())
		}
	}
	p.apps = nil
	routers.Delete(p.id)
	return errors.Join(errs...)
}

func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid proxy listen address %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("the proxy only listens on loopback, so its address must be a loopback IP such as 127.0.0.1, not %q", addr)
	}
	return nil
}

var (
	// lifecycleMu serializes starting and stopping Proxies, which write
	// Caddy globals.
	lifecycleMu sync.Mutex

	nextID  atomic.Int64
	routers sync.Map // Proxy id → Router, for the Caddy modules to find
)

func routerFor(id string) (Router, error) {
	r, ok := routers.Load(id)
	if !ok {
		return nil, fmt.Errorf("no Portby proxy with id %q", id)
	}
	return r.(Router), nil
}

// upstreamVar is the Caddy request variable the routing handler stores the
// chosen upstream address in, for reverse_proxy to dial.
const upstreamVar = "portby.upstream"

// caddyConfig is the whole Caddy config for one Proxy.
func caddyConfig(id string, cfg Config) []byte {
	logOutput := "discard"
	if cfg.LogErrors {
		logOutput = "stderr"
	}
	c := map[string]any{
		"admin":   map[string]any{"disabled": true, "config": map[string]any{"persist": false}},
		"storage": map[string]any{"module": "file_system", "root": cfg.StorageDir},
		"logging": map[string]any{"logs": map[string]any{"default": map[string]any{
			"level":  "ERROR",
			"writer": map[string]any{"output": logOutput},
		}}},
		"apps": map[string]any{
			"pki": map[string]any{
				"certificate_authorities": map[string]any{
					"local": map[string]any{
						"name": "Portby Local CA",
						// Trusting the CA is `portby install`'s job; Caddy
						// would try to do it with sudo.
						"install_trust": false,
					},
				},
			},
			"tls": map[string]any{
				"automation": map[string]any{
					"policies": []any{map[string]any{
						"issuers":   []any{map[string]any{"module": "internal", "ca": "local"}},
						"on_demand": true,
					}},
					"on_demand": map[string]any{
						"permission": map[string]any{"module": "portby"},
					},
				},
				// Storage cleaning stamps an instance ID into Caddy's data
				// dir under $HOME; certificates are tiny, so skip it.
				"disable_storage_clean": true,
			},
			"http": map[string]any{
				"grace_period": "5s",
				"servers": map[string]any{
					"https": map[string]any{
						"listen": []string{cfg.Addr},
						// No HTTP/3: it would need a UDP listener too.
						"protocols": []string{"h1", "h2"},
						// Routes have no host matchers, so Caddy can't infer
						// names to manage; TLS is switched on explicitly and
						// certificates are issued on demand.
						"automatic_https":         map[string]any{"disable": true},
						"tls_connection_policies": []any{map[string]any{}},
						"routes": []any{map[string]any{
							"handle": []any{
								map[string]any{"handler": "portby", "proxy": id},
								map[string]any{
									"handler":   "reverse_proxy",
									"upstreams": []any{map[string]any{"dial": "{http.vars." + upstreamVar + "}"}},
								},
							},
						}},
					},
				},
			},
		},
	}
	b, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	return b
}
