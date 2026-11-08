package e2e

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/daemon"
	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/paths"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// Caddy's in-memory certificate cache is shared by every daemon in the test
// process, so tests running in parallel must not serve the same Domain: give
// each test's Project its own name.

// processConfig is a laneway.toml declaring one Primary Process.
func processConfig(command string) string {
	return fmt.Sprintf("[processes.web]\ncommand = %q\nprimary = true\n", command)
}

func TestLinkedProjectIsServedAtItsDomainOverHTTPS(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	repo := gitfixture.New(t, "serve-basic").
		WriteFile("laneway.toml", processConfig(server+" --name web")).
		Commit("declare web Process")

	d.MustRunCLI(repo.Dir, "link")

	res := d.GetServer("https://serve-basic.localhost/hello")
	if res.Name != "web" || res.Path != "/hello" || res.Host != "serve-basic.localhost" || res.ForwardedProto != "https" {
		t.Fatalf("test server saw %+v", res)
	}
}

func TestProcessGetsItsPortThroughPlaceholderAndEnv(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	repo := gitfixture.New(t, "serve-port").
		WriteFile("laneway.toml", processConfig(server+" --port={port}")).
		Commit("declare web Process")

	d.MustRunCLI(repo.Dir, "link")

	res := d.GetServer("https://serve-port.localhost/")
	if res.ListenPort == "" || res.ListenPort != res.EnvPort {
		t.Fatalf("Process listened on {port}=%q but PORT=%q; want the same port in both", res.ListenPort, res.EnvPort)
	}
}

func TestProcessGetsAFreshPortOnEveryStart(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	repo := gitfixture.New(t, "serve-fresh-port").
		WriteFile("laneway.toml", processConfig(server)).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")
	first := d.GetServer("https://serve-fresh-port.localhost/")

	// Something else takes the port while the daemon is down.
	d.Stop()
	squatter, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", first.ListenPort))
	if err != nil {
		t.Fatalf("taking the Process's old port: %v", err)
	}
	defer squatter.Close()
	d.Start()

	second := d.GetServer("https://serve-fresh-port.localhost/")
	if second.ListenPort == first.ListenPort {
		t.Fatalf("Process reused port %s after restart", first.ListenPort)
	}
}

func TestUnknownDomainGetsLanewayNotFoundPage(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)

	res := d.Get("https://serve-nothing-here.localhost/some/path")

	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404:\n%s", res.StatusCode, res.Body)
	}
	assertContains(t, res.Body, "Laneway")
	assertContains(t, res.Body, "serve-nothing-here.localhost")
	assertContains(t, res.Body, "lane status")
}

func TestProxyForwardsWebSockets(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	repo := gitfixture.New(t, "serve-ws").
		WriteFile("laneway.toml", processConfig(server)).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")

	// A browser-style upgrade over HTTP/1.1 through the proxy.
	conn, err := tls.Dial("tcp", d.ProxyAddr(), &tls.Config{
		RootCAs: d.CAPool(), ServerName: "serve-ws.localhost", NextProtos: []string{"http/1.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	fmt.Fprint(conn, "GET /ws HTTP/1.1\r\nHost: serve-ws.localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("upgrade response: %d %v", resp.StatusCode, resp.Header)
	}

	for _, msg := range []string{"ping", "and again"} {
		if _, err := conn.Write([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(msg))
		if _, err := io.ReadFull(br, got); err != nil || string(got) != msg {
			t.Fatalf("echo of %q: got %q, %v", msg, got, err)
		}
	}
}

func TestProxyListensOnlyOnLoopback(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)

	host, port, err := net.SplitHostPort(d.ProxyAddr())
	if err != nil {
		t.Fatal(err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("proxy listens on %s, want a loopback address", d.ProxyAddr())
	}
	ip := nonLoopbackIP(t)
	if conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip.String(), port), 2*time.Second); err == nil {
		conn.Close()
		t.Fatalf("proxy accepted a connection on %s", ip)
	}
}

// nonLoopbackIP returns one of this machine's own non-loopback IPv4
// addresses, or skips the test if it has none.
func nonLoopbackIP(t *testing.T) net.IP {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			return n.IP
		}
	}
	t.Skip("no non-loopback IPv4 address to check")
	return nil
}

func TestDaemonRefusesANonLoopbackProxyAddress(t *testing.T) {
	t.Parallel()
	home := gitfixture.TempDir(t)
	runtimeDir, err := os.MkdirTemp("", "laneway-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runtimeDir) })

	_, err = daemon.Start(daemon.Config{
		Paths:     paths.Paths{StateDir: home, Socket: filepath.Join(runtimeDir, "laneway.sock")},
		ProxyAddr: "0.0.0.0:0",
	})

	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("daemon.Start with a non-loopback proxy address: err = %v, want a loopback error", err)
	}
}

func TestPlainHTTPRedirectsToTheSameURLOverHTTPS(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	_, httpsPort, _ := net.SplitHostPort(d.ProxyAddr())
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
	req, err := http.NewRequest(http.MethodGet, "http://"+d.HTTPAddr()+"/orders?page=2", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "serve-redirect.localhost"

	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if res.StatusCode != http.StatusPermanentRedirect {
		t.Fatalf("status = %d, want 308", res.StatusCode)
	}
	want := "https://serve-redirect.localhost:" + httpsPort + "/orders?page=2"
	if got := res.Header.Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestPlainHTTPOnlyRedirectsLocalhostNames(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	req, err := http.NewRequest(http.MethodGet, "http://"+d.HTTPAddr()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "example.com"
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status for example.com = %d, want 404", res.StatusCode)
	}
}
