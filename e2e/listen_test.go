package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

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
