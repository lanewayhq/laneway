package testkit

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// ProxyAddr is the address the daemon's HTTPS proxy is listening on. It
// changes on every (re)start, because tests ask for a random port.
func (d *Daemon) ProxyAddr() string {
	d.t.Helper()
	if d.running == nil {
		d.t.Fatal("testkit: ProxyAddr called while the daemon is stopped")
	}
	return d.running.ProxyAddr().String()
}

// HTTPAddr is the address the daemon redirects plain HTTP to HTTPS from.
// Like ProxyAddr, it changes on every (re)start.
func (d *Daemon) HTTPAddr() string {
	d.t.Helper()
	if d.running == nil {
		d.t.Fatal("testkit: HTTPAddr called while the daemon is stopped")
	}
	addr := d.running.HTTPAddr()
	if addr == nil {
		d.t.Fatal("testkit: the daemon isn't redirecting plain HTTP")
	}
	return addr.String()
}

// CAPool returns a cert pool holding only Laneway's local root CA, read from
// the daemon's home the way `lane install` would trust it.
func (d *Daemon) CAPool() *x509.CertPool {
	d.t.Helper()
	pem, err := os.ReadFile(d.Paths.CARootCert())
	if err != nil {
		d.t.Fatalf("testkit: reading Laneway's root CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		d.t.Fatalf("testkit: no certificate in %s", d.Paths.CARootCert())
	}
	return pool
}

// HTTPS returns a client that sends every request to the daemon's proxy,
// whatever host the URL names, and trusts only Laneway's root CA. The URL's
// host is used for SNI and the Host header, so
// d.HTTPS().Get("https://shop.localhost/") is what a browser would do.
// Redirects are not followed.
func (d *Daemon) HTTPS() *http.Client {
	d.t.Helper()
	pool := d.CAPool()
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, d.ProxyAddr())
			},
		},
	}
}

// Response is a fully read HTTP response.
type Response struct {
	*http.Response
	Body string
}

// Get sends a GET through the proxy with d.HTTPS and reads the whole body. It
// fails the test if the request fails.
func (d *Daemon) Get(url string) Response {
	d.t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		d.t.Fatalf("GET %s: %v", url, err)
	}
	return d.do(req)
}

func (d *Daemon) do(req *http.Request) Response {
	d.t.Helper()
	resp, err := d.HTTPS().Do(req)
	if err != nil {
		d.t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		d.t.Fatalf("%s %s: reading body: %v", req.Method, req.URL, err)
	}
	return Response{Response: resp, Body: string(body)}
}

// Navigate is Get as a browser navigating to url sends it: with
// Sec-Fetch-Mode: navigate and an Accept header preferring HTML.
func (d *Daemon) Navigate(url string) Response {
	d.t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		d.t.Fatalf("GET %s: %v", url, err)
	}
	SetNavigationHeaders(req)
	return d.do(req)
}

// SetNavigationHeaders sets the headers a browser navigating sends:
// Sec-Fetch-Mode: navigate and an Accept header preferring HTML.
func SetNavigationHeaders(req *http.Request) {
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
}

// ServerResponse is what the TestServer Process answers every request with.
type ServerResponse struct {
	Name           string `json:"name"`
	ListenPort     string `json:"listenPort"`
	EnvPort        string `json:"envPort"`
	Host           string `json:"host"`
	Path           string `json:"path"`
	ForwardedProto string `json:"forwardedProto"`
}

// GetServer is Get for a Domain served by a TestServer Process: it fails the
// test unless the response is a 200 from the server, and decodes it.
func (d *Daemon) GetServer(url string) ServerResponse {
	d.t.Helper()
	res := d.Get(url)
	var sr ServerResponse
	if res.StatusCode != http.StatusOK || json.Unmarshal([]byte(res.Body), &sr) != nil {
		d.t.Fatalf("GET %s: want a 200 from the test server, got %d:\n%s", url, res.StatusCode, res.Body)
	}
	return sr
}
