package e2e

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/testkit"
)

// The http effect through Seam 1: the hooked test Plugin's hooks return
// http effects, which Laneway sends to a local HTTP server standing in for
// a notification service.

// received is one request a requestLog's server got.
type received struct {
	Method, Path, Body string
	Header             http.Header
}

// requestLog is a local HTTP server that records the requests it gets and
// answers them with status.
type requestLog struct {
	*httptest.Server
	mu       sync.Mutex
	requests []received
	status   int
}

func newRequestLog(t *testing.T) *requestLog {
	t.Helper()
	l := &requestLog{status: http.StatusOK}
	l.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		l.mu.Lock()
		l.requests = append(l.requests, received{Method: r.Method, Path: r.URL.Path, Body: string(body), Header: r.Header})
		status := l.status
		l.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(l.Close)
	return l
}

// host is the server's host:port, as a manifest lists it.
func (l *requestLog) host() string {
	u, _ := url.Parse(l.URL)
	return u.Host
}

func (l *requestLog) answer(status int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.status = status
}

func (l *requestLog) received() []received {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]received(nil), l.requests...)
}

// waitForRequests waits until the server has had n requests, and returns
// them.
func (l *requestLog) waitForRequests(t *testing.T, n int) []received {
	t.Helper()
	testkit.WaitUntil(t, 10*time.Second, fmt.Sprintf("%d requests to %s", n, l.URL), func() bool {
		return len(l.received()) >= n
	})
	return l.received()
}

// httpHooked is the hooked test Plugin with a manifest that hooks
// Preparing and Ready and may send http effects to hosts.
func httpHooked(t *testing.T, hosts ...string) string {
	t.Helper()
	return withManifest(t, testkit.Plugin(t, "hooked"), fmt.Sprintf(`{
  "name": "hooked",
  "version": "0.1.0",
  "api_version": 1,
  "recipes": [{"name": "hooked", "detect": {"file": "hooked"}}],
  "hooks": ["preparing", "ready"],
  "capabilities": {"effects": ["http", "warning"], "http": %s}
}`, hooksJSON(t, hosts)))
}

func TestAnHTTPEffectSendsItsMethodHeadersAndBodyToAHostTheManifestLists(t *testing.T) {
	t.Parallel()
	server := newRequestLog(t)
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", httpHooked(t, server.host()))
	repo := hookedRepo(t, "http-sent", hooksJSON(t, map[string]any{"preparing": map[string]any{"effects": []any{
		map[string]any{"kind": "http", "http": map[string]any{
			"method": "PUT", "url": server.URL + "/topics/{project}",
			"headers": map[string]string{"Title": "{project} is Preparing"},
			"body":    "see {url}",
		}},
	}}}))
	d.MustRunCLI(repo.Dir, "link", "--yes")

	d.MustRunCLI(repo.Dir, "worktree", "ready")

	got := server.received()
	if len(got) != 1 {
		t.Fatalf("the server got %d requests, want 1: %+v", len(got), got)
	}
	r := got[0]
	if r.Method != "PUT" || r.Path != "/topics/http-sent" || r.Body != "see https://http-sent.localhost" {
		t.Errorf("the server got %s %s %q, want PUT /topics/http-sent \"see https://http-sent.localhost\"", r.Method, r.Path, r.Body)
	}
	if title := r.Header.Get("Title"); title != "http-sent is Preparing" {
		t.Errorf("the Title header is %q, want %q", title, "http-sent is Preparing")
	}
}

func TestAnHTTPEffectToAHostTheManifestDoesNotListIsRefusedWithAWarning(t *testing.T) {
	t.Parallel()
	listed, unlisted := newRequestLog(t), newRequestLog(t)
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", httpHooked(t, listed.host()))
	repo := hookedRepo(t, "http-unlisted", hooksJSON(t, map[string]any{"preparing": map[string]any{"effects": []any{
		map[string]any{"kind": "http", "http": map[string]any{"method": "POST", "url": unlisted.URL + "/steal", "body": "secrets"}},
		map[string]any{"kind": "http", "http": map[string]any{"method": "POST", "url": listed.URL + "/ok"}},
	}}}))
	d.MustRunCLI(repo.Dir, "link", "--yes")

	res := d.MustRunCLI(repo.Dir, "worktree", "ready")

	assertContains(t, res.Stderr, "warning: Plugin hooked's Preparing hook returned an http effect that Laneway refused: it goes to "+
		unlisted.host()+", but its manifest lists only "+listed.host()+" under capabilities.http")
	if got := unlisted.received(); len(got) != 0 {
		t.Errorf("the unlisted host got %d requests: %+v", len(got), got)
	}
	if got := listed.received(); len(got) != 1 {
		t.Errorf("the listed host got %d requests, want 1: %+v", len(got), got)
	}
}

func TestAnHTTPEffectThatGetsAnErrorResponseFailsAPreparingHook(t *testing.T) {
	t.Parallel()
	server := newRequestLog(t)
	server.answer(http.StatusServiceUnavailable)
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", httpHooked(t, server.host()))
	repo := hookedRepo(t, "http-failing", hooksJSON(t, map[string]any{"preparing": map[string]any{"effects": []any{
		map[string]any{"kind": "http", "http": map[string]any{"method": "POST", "url": server.URL + "/hook"}},
	}}}))
	d.MustRunCLI(repo.Dir, "link", "--yes")

	res := d.RunCLI(repo.Dir, "worktree", "ready")

	if res.ExitCode != 1 {
		t.Fatalf("worktree ready exited %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, `failed at step "Preparing hook of Plugin hooked"`)
	assertContains(t, res.Stderr, "POST "+server.URL+"/hook answered 503 Service Unavailable")

	server.answer(http.StatusOK)
	d.MustRunCLI(repo.Dir, "worktree", "ready", "--retry")
	assertSetupState(t, d, repo.Dir, "ready")
}

func TestAnHTTPEffectIsNotRedirectedToAHostTheManifestDoesNotList(t *testing.T) {
	t.Parallel()
	unlisted := newRequestLog(t)
	redirector := httptest.NewServer(http.RedirectHandler(unlisted.URL+"/steal", http.StatusFound))
	t.Cleanup(redirector.Close)
	d := testkit.StartDaemon(t)
	redirectorHost, _ := url.Parse(redirector.URL)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", httpHooked(t, redirectorHost.Host))
	repo := hookedRepo(t, "http-redirect", hooksJSON(t, map[string]any{"preparing": map[string]any{"effects": []any{
		map[string]any{"kind": "http", "http": map[string]any{"method": "GET", "url": redirector.URL + "/go"}},
	}}}))
	d.MustRunCLI(repo.Dir, "link", "--yes")

	res := d.RunCLI(repo.Dir, "worktree", "ready")

	if res.ExitCode != 1 {
		t.Fatalf("worktree ready exited %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, "redirected to "+unlisted.host()+", a host the Plugin's manifest doesn't list under capabilities.http")
	if got := unlisted.received(); len(got) != 0 {
		t.Errorf("the unlisted host got %d requests: %+v", len(got), got)
	}
}

func TestAPluginWhoseManifestListsAURLInsteadOfAHostIsRefused(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)

	res := d.RunCLI(d.Home, "plugin", "add", "--yes", httpHooked(t, "ntfy.sh", "https://ntfy.sh/topic"))

	if res.ExitCode == 0 {
		t.Fatalf("adding the Plugin exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `capabilities.http[1]: "https://ntfy.sh/topic" isn't a host; list hosts such as "ntfy.sh" or "localhost:8080", without a scheme or path`)
}
