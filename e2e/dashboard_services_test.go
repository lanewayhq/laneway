package e2e

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// liveEvents opens the Dashboard's live stream at url and returns the data
// of its events called name, decoded into T, as they arrive. The channel is
// closed when the stream ends.
func liveEvents[T any](t *testing.T, d *testkit.Daemon, url, name string) <-chan T {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	client := d.HTTPS()
	client.Timeout = 0
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status %d:\n%s", url, resp.StatusCode, b)
	}
	events := make(chan T, 100)
	go func() {
		defer close(events)
		event := ""
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: ") && event == name:
				var v T
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &v); err != nil {
					t.Errorf("bad %s event %q: %v", name, line, err)
					return
				}
				events <- v
			}
		}
	}()
	return events
}

// waitForLive reads events until one satisfies cond and returns it,
// failing the test if none does within a few seconds.
func waitForLive[T any](t *testing.T, events <-chan T, what string, cond func(T) bool) T {
	t.Helper()
	timeout := time.After(discoveryTimeout)
	for {
		select {
		case v, ok := <-events:
			if !ok {
				t.Fatalf("the stream ended while waiting for %s", what)
			}
			if cond(v) {
				return v
			}
		case <-timeout:
			t.Fatalf("timed out after %s waiting for an event with %s", discoveryTimeout, what)
		}
	}
}

// serviceIn finds the Service called name ("redis:7") in list.
func serviceIn(list api.ServiceList, name string) (api.Service, bool) {
	for _, s := range list.Services {
		if s.Name == name {
			return s, true
		}
	}
	return api.Service{}, false
}

func TestTheDashboardShowsServicesLiveAndStartsAndStopsThem(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-services")
	d.MustRunCLI(serviceProject(t, "dash-services-shop", `redis = "7"`, "").Dir, "link")
	events := liveEvents[api.ServiceList](t, d, dash+"/api/v1/events", "services")
	waitForLive(t, events, "redis:7 on 6379, stopped", func(l api.ServiceList) bool {
		s, ok := serviceIn(l, "redis:7")
		return ok && s.Engine == "redis" && s.Version == "7" && s.Port == 6379 && s.State == "stopped"
	})

	redis := api.ServiceRequest{Name: "redis:7"}
	for _, action := range []string{"up", "down"} {
		for _, origin := range []string{"", "https://dash-services-shop.localhost", "https://evil.example"} {
			status, body := dashboardRequest(t, d, http.MethodPost, dash+"/api/v1/services/"+action, origin, redis)
			if status != http.StatusForbidden {
				t.Errorf("services/%s from Origin %q: status %d, want 403:\n%s", action, origin, status, body)
			}
		}
	}
	if d.Engine.IsRunning("portby-redis-7") {
		t.Fatal("a rejected request started redis:7")
	}

	if status, body := dashboardRequest(t, d, http.MethodPost, dash+"/api/v1/services/up", dash, redis); status != http.StatusOK {
		t.Fatalf("services/up from the Dashboard: status %d:\n%s", status, body)
	}
	waitForLive(t, events, "redis:7 running", func(l api.ServiceList) bool {
		s, _ := serviceIn(l, "redis:7")
		return s.State == "running"
	})
	if !d.Engine.IsRunning("portby-redis-7") {
		t.Fatal("services/up from the Dashboard didn't start the container")
	}

	if status, body := dashboardRequest(t, d, http.MethodPost, dash+"/api/v1/services/down", dash, redis); status != http.StatusOK {
		t.Fatalf("services/down from the Dashboard: status %d:\n%s", status, body)
	}
	waitForLive(t, events, "redis:7 stopped again", func(l api.ServiceList) bool {
		s, _ := serviceIn(l, "redis:7")
		return s.State == "stopped"
	})
	if d.Engine.IsRunning("portby-redis-7") {
		t.Fatal("services/down from the Dashboard didn't stop the container")
	}
}

func TestTheDashboardShowsOrphansLiveAndDropsThemAsGcDoes(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-gc")
	repo := postgresProject(t, "dash-gc-shop", "")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	events := liveEvents[api.OrphanList](t, d, dash+"/api/v1/events", "orphans")
	waitForLive(t, events, "no Orphans", func(l api.OrphanList) bool { return len(l.Orphans) == 0 })

	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	repo.Git("worktree", "remove", wt.Dir)
	got := waitForLive(t, events, "the removed Worktree's database", func(l api.OrphanList) bool {
		return len(l.Orphans) == 1
	}).Orphans[0]
	if got.Database != "dash_gc_shop__feature" || got.Service != "postgres:18" || got.Project != "dash-gc-shop" ||
		got.Worktree != "feature" || got.Kind != "database" {
		t.Fatalf("the Orphan: %+v", got)
	}
	if got.Since.IsZero() || !got.Expires.Equal(got.Since.Add(24*time.Hour)) {
		t.Fatalf("the Orphan was made at %v and is dropped at %v, want 24 hours later", got.Since, got.Expires)
	}

	// Like `portby gc`: a preview lists what would go and drops nothing,
	// then exactly the previewed list is sent.
	status, body := dashboardRequest(t, d, http.MethodPost, dash+"/api/v1/gc", dash, api.GCRequest{Preview: true})
	if status != http.StatusOK {
		t.Fatalf("gc preview from the Dashboard: status %d:\n%s", status, body)
	}
	var preview api.GCResponse
	if err := json.Unmarshal([]byte(body), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Orphans) != 1 || preview.Orphans[0].Database != "dash_gc_shop__feature" {
		t.Fatalf("gc preview: %+v", preview)
	}
	waitForOrphans(t, d, "dash_gc_shop__feature")

	status, body = dashboardRequest(t, d, http.MethodPost, dash+"/api/v1/gc", dash, api.GCRequest{
		Databases: []api.OrphanRef{{Service: preview.Orphans[0].Service, Database: preview.Orphans[0].Database}},
	})
	if status != http.StatusOK {
		t.Fatalf("gc from the Dashboard: status %d:\n%s", status, body)
	}
	waitForLive(t, events, "no Orphans after the drop", func(l api.OrphanList) bool { return len(l.Orphans) == 0 })
	if dbs := postgresDatabases(t, d, 5432); slices.Contains(dbs, "dash_gc_shop__feature") {
		t.Fatalf("the dropped Orphan is still in Postgres: %v", dbs)
	}
}
