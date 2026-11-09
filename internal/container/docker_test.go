package container

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAPI is a Docker Engine API on a Unix socket that answers each request
// with handle and records it as "METHOD /path" (without the API version).
type fakeAPI struct {
	mu       sync.Mutex
	requests []string
}

func (f *fakeAPI) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func startFakeAPI(t *testing.T, handle func(w http.ResponseWriter, call string)) (*Docker, *fakeAPI) {
	t.Helper()
	dir, err := os.MkdirTemp("", "engine")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "api.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeAPI{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := r.Method + " " + strings.TrimPrefix(r.URL.Path, "/"+APIVersion)
		f.mu.Lock()
		f.requests = append(f.requests, call)
		f.mu.Unlock()
		handle(w, call)
	}))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return NewDocker(socket), f
}

func answer(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if message != "" {
		w.Write([]byte(`{"message":"` + message + `"}`))
	}
}

// podmanStuckStop is rootless Podman's answer when a forced delete's stop
// times out waiting for the container's process to die.
const podmanStuckStop = "cannot remove container abc as it could not be stopped: given PID did not die within timeout"

func fastRemoveRetries(t *testing.T) {
	old := removeRetryDelay
	removeRetryDelay = time.Millisecond
	t.Cleanup(func() { removeRetryDelay = old })
}

func TestRemoveKillsAndRetriesWhenTheContainerCouldNotBeStopped(t *testing.T) {
	fastRemoveRetries(t)
	deletes := 0
	engine, api := startFakeAPI(t, func(w http.ResponseWriter, call string) {
		switch call {
		case "DELETE /containers/svc":
			deletes++
			if deletes == 1 {
				answer(w, http.StatusInternalServerError, podmanStuckStop)
				return
			}
			answer(w, http.StatusNoContent, "")
		default:
			answer(w, http.StatusNoContent, "")
		}
	})

	if err := engine.Remove(context.Background(), "svc", "vol"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	want := []string{"DELETE /containers/svc", "POST /containers/svc/kill", "DELETE /containers/svc", "DELETE /volumes/vol"}
	if got := api.seen(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests = %q, want %q", got, want)
	}
}

func TestRemoveIgnoresAKillOfAContainerThatIsNoLongerRunning(t *testing.T) {
	fastRemoveRetries(t)
	for _, kill := range []int{http.StatusNotFound, http.StatusConflict} {
		deletes := 0
		engine, _ := startFakeAPI(t, func(w http.ResponseWriter, call string) {
			switch call {
			case "DELETE /containers/svc":
				deletes++
				if deletes == 1 {
					answer(w, http.StatusInternalServerError, podmanStuckStop)
					return
				}
				answer(w, http.StatusNoContent, "")
			case "POST /containers/svc/kill":
				answer(w, kill, "container svc is not running")
			default:
				answer(w, http.StatusNoContent, "")
			}
		})
		if err := engine.Remove(context.Background(), "svc", ""); err != nil {
			t.Errorf("Remove with kill answering HTTP %d: %v", kill, err)
		}
	}
}

func TestRemoveGivesUpWhenTheContainerStillCannotBeStopped(t *testing.T) {
	fastRemoveRetries(t)
	engine, api := startFakeAPI(t, func(w http.ResponseWriter, call string) {
		if call == "DELETE /containers/svc" {
			answer(w, http.StatusInternalServerError, podmanStuckStop)
			return
		}
		answer(w, http.StatusNoContent, "")
	})

	err := engine.Remove(context.Background(), "svc", "vol")
	if err == nil || !strings.Contains(err.Error(), "could not be stopped") {
		t.Fatalf("Remove = %v, want the engine's could-not-be-stopped error", err)
	}
	deletes := 0
	for _, call := range api.seen() {
		if call == "DELETE /volumes/vol" {
			t.Errorf("Remove deleted the volume of a container it couldn't remove")
		}
		if call == "DELETE /containers/svc" {
			deletes++
		}
	}
	if deletes != removeAttempts {
		t.Errorf("Remove tried the delete %d times, want %d", deletes, removeAttempts)
	}
}
