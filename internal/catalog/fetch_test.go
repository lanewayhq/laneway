package catalog

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Downloads through Source.Fetch and Source.Load, from a test server whose
// responses each test scripts.

// noWait is the default download policy without the waits between attempts, so
// tests don't sleep through real backoff.
var noWait = func() downloadPolicy {
	p := defaultDownloads
	p.backoff = 0
	return p
}()

// testServer is a test server that counts the requests it is sent.
type testServer struct {
	*httptest.Server
	requests atomic.Int32
}

// Requests is how many requests the server has been sent.
func (s *testServer) Requests() int { return int(s.requests.Load()) }

// server serves every request with respond, given the number of the request
// (from 1), until the test ends.
func server(t *testing.T, respond func(n int, w http.ResponseWriter, r *http.Request)) *testServer {
	t.Helper()
	s := &testServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respond(int(s.requests.Add(1)), w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

// hijack takes over the connection w answers on. If it can't, the test
// fails, and the handler is aborted rather than left without a connection.
func hijack(t *testing.T, w http.ResponseWriter) net.Conn {
	t.Helper()
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		t.Errorf("hijacking the connection: %v", err)
		panic(http.ErrAbortHandler)
	}
	return conn
}

// testKey is a new Catalog signing key.
func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub, key
}

// testSource is a Source fetching from s, downloading as policy says.
func testSource(t *testing.T, s *testServer, key ed25519.PublicKey, policy downloadPolicy) *Source {
	t.Helper()
	src := NewSource(Location{URL: s.URL + "/catalog.json", Key: key}, t.TempDir())
	src.downloads = policy
	return src
}

// testRelease is a release archive of a Plugin directory holding only a
// manifest, and the Release listing it.
func testRelease(t *testing.T) ([]byte, Release) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name": "kv", "version": "0.1.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	archive, err := Pack(dir)
	if err != nil {
		t.Fatal(err)
	}
	return archive, Release{Version: "0.1.0", URL: "releases/kv-0.1.0.tar.gz", SHA256: SHA256(archive)}
}

func TestAReleaseIsFetchedWhenItsDownloadFailsTemporarilyAFewTimesFirst(t *testing.T) {
	archive, r := testRelease(t)
	s := server(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		if n <= 2 {
			http.Error(w, "upstream timed out", http.StatusGatewayTimeout)
			return
		}
		w.Write(archive)
	})
	dir := t.TempDir()

	err := testSource(t, s, nil, noWait).Fetch(context.Background(), r, dir)

	if err != nil {
		t.Fatalf("Fetch after two 504s: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugin.json")); err != nil {
		t.Fatalf("the release wasn't unpacked: %v", err)
	}
}

func TestAReleaseIsFetchedWhenItsConnectionIsDroppedFirst(t *testing.T) {
	archive, r := testRelease(t)
	for _, tc := range []struct {
		name string
		drop func(t *testing.T, w http.ResponseWriter)
	}{
		{"closed unanswered", func(t *testing.T, w http.ResponseWriter) { hijack(t, w).Close() }},
		{"reset", func(t *testing.T, w http.ResponseWriter) {
			conn := hijack(t, w).(*net.TCPConn)
			conn.SetLinger(0)
			conn.Close()
		}},
		{"cut off partway", func(t *testing.T, w http.ResponseWriter) {
			conn := hijack(t, w)
			fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", len(archive))
			conn.Write(archive[:len(archive)/2])
			conn.Close()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := server(t, func(n int, w http.ResponseWriter, _ *http.Request) {
				if n == 1 {
					tc.drop(t, w)
					return
				}
				w.Write(archive)
			})

			err := testSource(t, s, nil, noWait).Fetch(context.Background(), r, t.TempDir())

			if err != nil {
				t.Fatalf("Fetch after the connection was %s: %v", tc.name, err)
			}
		})
	}
}

func TestAReleaseIsFetchedWhenAnAttemptAtItHangs(t *testing.T) {
	archive, r := testRelease(t)
	s := server(t, func(n int, w http.ResponseWriter, req *http.Request) {
		if n == 1 {
			<-req.Context().Done() // until Laneway gives up on it
			return
		}
		w.Write(archive)
	})
	policy := noWait
	policy.release.attempt = 100 * time.Millisecond

	err := testSource(t, s, nil, policy).Fetch(context.Background(), r, t.TempDir())

	if err != nil {
		t.Fatalf("Fetch after an attempt hung: %v", err)
	}
}

func TestEndingTheContextStopsFetchingAReleaseAtOnce(t *testing.T) {
	_, r := testRelease(t)
	for _, tc := range []struct {
		name    string
		respond func(w http.ResponseWriter, req *http.Request)
	}{
		{"while an attempt hangs", func(_ http.ResponseWriter, req *http.Request) { <-req.Context().Done() }},
		{"while waiting to try again", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := server(t, func(_ int, w http.ResponseWriter, req *http.Request) {
				tc.respond(w, req)
			})
			policy := defaultDownloads
			policy.backoff = time.Hour
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			start := time.Now()

			err := testSource(t, s, nil, policy).Fetch(ctx, r, t.TempDir())

			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Fetch = %v, want the context's error", err)
			}
			if took := time.Since(start); took > 5*time.Second {
				t.Fatalf("Fetch took %s after its context ended", took)
			}
			if got := s.Requests(); got != 1 {
				t.Fatalf("downloaded %d times, want 1", got)
			}
		})
	}
}

func TestTheCatalogIsLoadedWhenItsDownloadFailsTemporarilyFirst(t *testing.T) {
	pub, key := testKey(t)
	data := []byte(`{"format": 1, "plugins": [{"name": "kv", "releases": []}]}`)
	var catalogRequests atomic.Int32
	s := server(t, func(_ int, w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/catalog.json":
			if catalogRequests.Add(1) == 1 {
				http.Error(w, "bad gateway", http.StatusBadGateway)
				return
			}
			w.Write(data)
		case "/catalog.json" + SignatureSuffix:
			w.Write(Sign(data, key))
		}
	})

	cat, err := testSource(t, s, pub, noWait).Load(context.Background())

	if err != nil {
		t.Fatalf("Load after a 502: %v", err)
	}
	if len(cat.Plugins) != 1 || cat.Plugins[0].Name != "kv" {
		t.Fatalf("Load = %+v, want the Catalog served", cat)
	}
}

func TestACatalogThatFailsPermanentlyIsntDownloadedAgain(t *testing.T) {
	pub, key := testKey(t)
	_, forger := testKey(t)
	data := []byte(`{"format": 1, "plugins": []}`)
	for _, tc := range []struct {
		name string
		sig  []byte
		want string
	}{
		{"signature too large", append(Sign(data, key), make([]byte, 8<<10)...), "larger than"},
		{"not signed with its key", Sign(data, forger), "isn't signed with Laneway's Catalog key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := server(t, func(_ int, w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/catalog.json" {
					w.Write(data)
					return
				}
				w.Write(tc.sig)
			})

			_, err := testSource(t, s, pub, noWait).Load(context.Background())

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load = %v, want an error saying %q", err, tc.want)
			}
			if got := s.Requests(); got != 2 {
				t.Fatalf("made %d requests, want 2: the Catalog and its signature, once each", got)
			}
		})
	}
}

func TestAReleaseDownloadThatKeepsFailingTemporarilyIsGivenUpOnAfterAFewAttempts(t *testing.T) {
	_, r := testRelease(t)
	s := server(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})

	err := testSource(t, s, nil, noWait).Fetch(context.Background(), r, t.TempDir())

	if err == nil {
		t.Fatal("Fetch from a server that only answers 503 succeeded")
	}
	if errors.Is(err, ErrRefused) {
		t.Fatalf("a release that couldn't be fetched was refused: %v", err)
	}
	if !strings.Contains(err.Error(), "503 Service Unavailable") {
		t.Fatalf("the error doesn't say why: %v", err)
	}
	if got := s.Requests(); got != 3 {
		t.Fatalf("downloaded %d times, want 3", got)
	}
}

func TestADownloadThatKeepsHangingIsGivenUpOnWhenItsTimeRunsOut(t *testing.T) {
	_, r := testRelease(t)
	s := server(t, func(_ int, _ http.ResponseWriter, req *http.Request) {
		<-req.Context().Done()
	})
	policy := noWait
	policy.release.attempt = 200 * time.Millisecond
	policy.release.budget = 300 * time.Millisecond
	start := time.Now()

	err := testSource(t, s, nil, policy).Fetch(context.Background(), r, t.TempDir())

	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Fetch took %s, though its download may take 300ms", took)
	}
	if err == nil || !strings.Contains(err.Error(), "it took longer than 300ms (tried 2 times)") {
		t.Fatalf("Fetch = %v, want an error saying it took longer than 300ms, over 2 attempts", err)
	}
	if got := s.Requests(); got != 2 {
		t.Fatalf("downloaded %d times, want 2: the second attempt cut short when the time ran out", got)
	}
}

func TestADownloadWhoseTimeRunsOutWhileWaitingToTryAgainSaysSo(t *testing.T) {
	_, r := testRelease(t)
	s := server(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	policy := defaultDownloads
	policy.backoff = time.Hour
	policy.release.budget = 200 * time.Millisecond

	err := testSource(t, s, nil, policy).Fetch(context.Background(), r, t.TempDir())

	want := "503 Service Unavailable (tried once, until it took longer than 200ms)"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Fetch = %v, want an error saying %q", err, want)
	}
}

func TestTheSignatureIsGivenItsOwnTimeAfterTheCatalogTookMostOfIts(t *testing.T) {
	pub, key := testKey(t)
	data := []byte(`{"format": 1, "plugins": []}`)
	var catalogRequests, sigRequests atomic.Int32
	s := server(t, func(_ int, w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/catalog.json":
			if catalogRequests.Add(1) == 1 {
				<-req.Context().Done()
				return
			}
			w.Write(data)
		case "/catalog.json" + SignatureSuffix:
			if sigRequests.Add(1) == 1 {
				<-req.Context().Done()
				return
			}
			w.Write(Sign(data, key))
		}
	})
	policy := noWait
	policy.catalog.attempt, policy.catalog.budget = 300*time.Millisecond, 500*time.Millisecond
	policy.signature.attempt, policy.signature.budget = 300*time.Millisecond, 500*time.Millisecond

	_, err := testSource(t, s, pub, policy).Load(context.Background())

	if err != nil {
		t.Fatalf("Load after an attempt at both the Catalog and its signature hung: %v", err)
	}
}

func TestTheFirstAttemptAtADownloadMayTakeAsLongAsTheWholeDownloadCouldBefore(t *testing.T) {
	// Before Laneway tried a download again, the Catalog (with its
	// signature) could take 15 seconds and a release 2 minutes.
	for _, tc := range []struct {
		name   string
		dl     download
		before time.Duration
	}{
		{"Catalog", defaultDownloads.catalog, 15 * time.Second},
		{"release", defaultDownloads.release, 2 * time.Minute},
	} {
		if tc.dl.attempt < tc.before {
			t.Errorf("the first attempt at a %s may take %s, want at least %s", tc.name, tc.dl.attempt, tc.before)
		}
		if tc.dl.budget <= tc.dl.attempt {
			t.Errorf("a %s download may take %s, leaving no time to try again after an attempt that takes %s", tc.name, tc.dl.budget, tc.dl.attempt)
		}
	}
}

func TestAReleaseThatFailsPermanentlyIsntDownloadedAgain(t *testing.T) {
	archive, r := testRelease(t)
	for _, tc := range []struct {
		name    string
		respond func(w http.ResponseWriter)
		want    string
	}{
		{"404", func(w http.ResponseWriter) { http.Error(w, "not found", http.StatusNotFound) }, "404 Not Found"},
		{"403", func(w http.ResponseWriter) { http.Error(w, "no", http.StatusForbidden) }, "403 Forbidden"},
		{"wrong SHA-256", func(w http.ResponseWriter) { w.Write(append(slices.Clone(archive), 0)) }, "its SHA-256 is"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := server(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
				tc.respond(w)
			})

			err := testSource(t, s, nil, noWait).Fetch(context.Background(), r, t.TempDir())

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Fetch = %v, want an error saying %q", err, tc.want)
			}
			if got := s.Requests(); got != 1 {
				t.Fatalf("downloaded %d times, want 1", got)
			}
		})
	}
}
