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

// CAPool returns a cert pool holding only Portby's local root CA, read from
// the daemon's home the way `portby install` would trust it.
func (d *Daemon) CAPool() *x509.CertPool {
	d.t.Helper()
	pem, err := os.ReadFile(d.Paths.CARootCert())
	if err != nil {
		d.t.Fatalf("testkit: reading Portby's root CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		d.t.Fatalf("testkit: no certificate in %s", d.Paths.CARootCert())
	}
	return pool
}

// HTTPS returns a client that sends every request to the daemon's proxy,
// whatever host the URL names, and trusts only Portby's root CA. The URL's
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
	resp, err := d.HTTPS().Get(url)
	if err != nil {
		d.t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		d.t.Fatalf("GET %s: reading body: %v", url, err)
	}
	return Response{Response: resp, Body: string(body)}
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
