package e2e

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// extraAddr is the extra address tests listen on: another loopback address,
// so they prove the proxy accepts connections there without a real tailnet.
const extraAddr = "127.0.0.2"

// getServerAt is d.GetServer through the proxy at ip instead of loopback.
func getServerAt(t *testing.T, d *testkit.Daemon, ip, url string) testkit.ServerResponse {
	t.Helper()
	res, err := d.HTTPSAt(ip).Get(url)
	if err != nil {
		t.Fatalf("GET %s at %s: %v", url, ip, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	var sr testkit.ServerResponse
	if res.StatusCode != http.StatusOK || json.Unmarshal(body, &sr) != nil {
		t.Fatalf("GET %s at %s: want a 200 from the test server, got %d:\n%s", url, ip, res.StatusCode, body)
	}
	return sr
}

func TestAnExtraListenAddressServesWorktreesAsLoopbackDoes(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	writeGlobalConfig(t, d, `listen = ["`+extraAddr+`"]`+"\n")
	d.Restart()
	repo := routedProject(t, "listen-extra")
	d.MustRunCLI(repo.Dir, "link")

	if got := getServerAt(t, d, extraAddr, "https://listen-extra.localhost/"); got.Name != "web@listen-extra" {
		t.Fatalf("at %s the Domain reached %q, want the main Worktree's Primary Process", extraAddr, got.Name)
	}
	if got := d.GetServer("https://listen-extra.localhost/"); got.Name != "web@listen-extra" {
		t.Fatalf("on loopback the Domain reached %q, want the main Worktree's Primary Process", got.Name)
	}
}

// listenStatus is the state the daemon reports for the address listen
// names as name.
func listenStatus(t *testing.T, d *testkit.Daemon, name string) api.ListenAddress {
	t.Helper()
	info, err := d.Client().Daemon(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range info.Listen {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("the daemon reports no listen address %q: %+v", name, info)
	return api.ListenAddress{}
}

func TestTailscaleIsListenedOnOnceItHasAnAddress(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Platform.SetNoTailscale("Tailscale is stopped.")
	writeGlobalConfig(t, d, `listen = ["tailscale"]`+"\n")
	d.Restart()
	repo := routedProject(t, "listen-tailscale")
	d.MustRunCLI(repo.Dir, "link")

	// The daemon still serves on loopback, and says why it can't listen
	// on Tailscale.
	if got := d.GetServer("https://listen-tailscale.localhost/"); got.Name != "web@listen-tailscale" {
		t.Fatalf("on loopback the Domain reached %q", got.Name)
	}
	st := listenStatus(t, d, "tailscale")
	if st.Listening || !strings.Contains(st.Error, "Tailscale is stopped.") {
		t.Fatalf("tailscale with no address: %+v, want it not listening, saying why", st)
	}

	// Tailscale comes up after Laneway: no restart.
	d.Platform.SetTailscaleIPv4(extraAddr)
	testkit.WaitUntil(t, 5*time.Second, "Laneway to listen on Tailscale's address", func() bool {
		return listenStatus(t, d, "tailscale").Listening
	})
	if got := getServerAt(t, d, extraAddr, "https://listen-tailscale.localhost/"); got.Name != "web@listen-tailscale" {
		t.Fatalf("at Tailscale's address the Domain reached %q", got.Name)
	}
	if st := listenStatus(t, d, "tailscale"); st.IP != extraAddr || st.Error != "" {
		t.Fatalf("tailscale once up: %+v, want listening on %s", st, extraAddr)
	}
}

func TestWhenTailscalesAddressChangesTheOldOneIsNoLongerListenedOn(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Platform.SetTailscaleIPv4(extraAddr)
	writeGlobalConfig(t, d, `listen = ["tailscale"]`+"\n")
	d.Restart()
	repo := routedProject(t, "listen-moved")
	d.MustRunCLI(repo.Dir, "link")
	getServerAt(t, d, extraAddr, "https://listen-moved.localhost/")

	const moved = "127.0.0.3"
	d.Platform.SetTailscaleIPv4(moved)
	testkit.WaitUntil(t, 5*time.Second, "Laneway to follow Tailscale's address", func() bool {
		st := listenStatus(t, d, "tailscale")
		return st.IP == moved && st.Listening
	})

	if got := getServerAt(t, d, moved, "https://listen-moved.localhost/"); got.Name != "web@listen-moved" {
		t.Fatalf("at Tailscale's new address the Domain reached %q", got.Name)
	}
	if conn, err := net.DialTimeout("tcp", d.ProxyAddrAt(extraAddr), 2*time.Second); err == nil {
		conn.Close()
		t.Fatalf("the proxy still accepts connections at Tailscale's old address %s", extraAddr)
	}
}

func TestPlainHTTPOnAnExtraListenAddressRedirectsToHTTPS(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	writeGlobalConfig(t, d, `listen = ["`+extraAddr+`"]`+"\n")
	d.Restart()
	_, httpsPort, _ := net.SplitHostPort(d.ProxyAddr())
	req, err := http.NewRequest(http.MethodGet, "http://"+d.HTTPAddrAt(extraAddr)+"/orders?page=2", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "listen-redirect.localhost"
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}

	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if res.StatusCode != http.StatusPermanentRedirect {
		t.Fatalf("status = %d, want 308", res.StatusCode)
	}
	want := "https://listen-redirect.localhost:" + httpsPort + "/orders?page=2"
	if got := res.Header.Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestListenRefusesEveryInterfaceAndWhatIsntAnAddress(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	for _, tc := range []struct{ value, want string }{
		{"0.0.0.0", "never listens on every interface"},
		{"::", "never listens on every interface"},
		{"::ffff:0.0.0.0", "never listens on every interface"},
		{"::ffff:0:0", "never listens on every interface"},
		{"wireguard", `"wireguard" is neither an IP address nor "tailscale"`},
		{"100.101.102", `"100.101.102" is neither an IP address nor "tailscale"`},
	} {
		writeGlobalConfig(t, d, `listen = ["`+extraAddr+`", "`+tc.value+`"]`+"\n")
		d.Restart()

		info, err := d.Client().Daemon(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(info.ListenError, tc.want) || !strings.Contains(info.ListenError, "config.toml") {
			t.Fatalf("listen = [%q]: listenError = %q, want the config file and %q", tc.value, info.ListenError, tc.want)
		}
		// Nothing in a bad listen is listened on, not even its good
		// addresses; loopback still is.
		if len(info.Listen) != 0 {
			t.Fatalf("listen = [%q]: the daemon reports listen addresses %+v", tc.value, info.Listen)
		}
		if conn, err := net.DialTimeout("tcp", d.ProxyAddrAt(extraAddr), 2*time.Second); err == nil {
			conn.Close()
			t.Fatalf("listen = [%q]: the proxy listens on %s", tc.value, extraAddr)
		}
		// The proxy answers on loopback (with its page for a name nothing
		// is served at).
		if res := d.Get("https://listen-refused.localhost/"); res.StatusCode == 0 {
			t.Fatalf("listen = [%q]: no answer on loopback", tc.value)
		}
	}
}

func TestTheStatusReportsEachListenAddressAndWhyItIsntListenedOn(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	// 192.0.2.1 is reserved for documentation, so no machine has it and
	// it can't be bound.
	writeGlobalConfig(t, d, `listen = ["`+extraAddr+`", "192.0.2.1"]`+"\n")
	d.Restart()

	info, err := d.Client().Daemon(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if info.ListenError != "" || len(info.Listen) != 2 {
		t.Fatalf("got %+v, want both addresses", info)
	}
	if got, want := info.Listen[0], (api.ListenAddress{Name: extraAddr, IP: extraAddr, Listening: true}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	unbound := info.Listen[1]
	if unbound.Name != "192.0.2.1" || unbound.Listening || !strings.Contains(unbound.Error, "can't listen on 192.0.2.1") {
		t.Fatalf("got %+v, want 192.0.2.1 not listening, saying why", unbound)
	}
}

func TestAnAddressFromTailscaleThatMeansEveryInterfaceIsntListenedOn(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	// 0.0.0.0 written as an IPv4-mapped IPv6 address.
	d.Platform.SetTailscaleIPv4("::ffff:0.0.0.0")
	writeGlobalConfig(t, d, `listen = ["tailscale"]`+"\n")
	d.Restart()

	st := listenStatus(t, d, "tailscale")
	if st.Listening || !strings.Contains(st.Error, "never listens on every interface") {
		t.Fatalf("tailscale at ::ffff:0.0.0.0: %+v, want it not listening, saying why", st)
	}
}
