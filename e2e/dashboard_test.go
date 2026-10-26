package e2e

import (
	"bufio"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/daemon"
	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// Every daemon in this test binary serves a Dashboard, and Caddy's
// certificate cache is shared by name (see "Serving Domains in tests"), so
// only TestTheDashboardIsServedAtPortbyLocalhost uses the default Domain;
// the other tests give theirs a unique one with dashboardDaemon.

func TestTheDashboardIsServedAtPortbyLocalhost(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)

	page := d.Navigate("https://portby.localhost/")

	if page.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200:\n%s", page.StatusCode, page.Body)
	}
	if ct := page.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type %q, want HTML", ct)
	}
	if !strings.Contains(page.Body, "<title>Portby</title>") {
		t.Fatalf("not the Dashboard:\n%s", page.Body)
	}
}

// dashboardDaemon starts a daemon serving its Dashboard at
// https://<name>.localhost, and returns that URL.
func dashboardDaemon(t *testing.T, name string) (*testkit.Daemon, string) {
	t.Helper()
	domain := name + ".localhost"
	d := testkit.StartDaemon(t, func(c *daemon.Config) { c.DashboardDomain = domain })
	return d, "https://" + domain
}

// dashboardRequest sends a request to the Dashboard at url (its own path
// included) with the given Origin header ("" = none), as a browser's fetch
// does, and returns the status and body.
func dashboardRequest(t *testing.T, d *testkit.Daemon, method, url, origin string, body any) (int, string) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.HTTPS().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func TestTheDashboardRejectsRequestsThatChangeStateFromOtherOrigins(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-origin")
	repo, _ := pidProject(t, "dash-origin-shop", "")
	d.MustRunCLI(repo.Dir, "link")
	up := api.ControlRequest{Dir: repo.Dir}

	for _, origin := range []string{
		"",                                   // curl, or a form from nowhere
		"https://dash-origin-shop.localhost", // a Project's own app
		"https://evil.example",
		"http://dash-origin.localhost", // not HTTPS
		"null",
	} {
		status, body := dashboardRequest(t, d, http.MethodPost, dash+"/api/v1/worktree/up", origin, up)
		if status != http.StatusForbidden {
			t.Errorf("Origin %q: status %d, want 403:\n%s", origin, status, body)
		}
	}
	if w := worktreeState(t, d, "dash-origin-shop.localhost"); w.State != "stopped" {
		t.Fatalf("a rejected request started the Worktree: state %q", w.State)
	}

	status, body := dashboardRequest(t, d, http.MethodPost, dash+"/api/v1/worktree/up", dash, up)
	if status != http.StatusOK {
		t.Fatalf("from the Dashboard's own origin: status %d, want 200:\n%s", status, body)
	}
	if w := worktreeState(t, d, "dash-origin-shop.localhost"); w.State != "running" {
		t.Fatalf("after up from the Dashboard: state %q, want running", w.State)
	}
}

func TestTheDashboardReadsTheSameAPIAsTheCLI(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-read")
	repo := gitfixture.New(t, "dash-read-shop")
	d.MustRunCLI(repo.Dir, "link")

	// Reading needs no Origin: a page elsewhere can send the request but
	// can't read the answer.
	status, body := dashboardRequest(t, d, http.MethodGet, dash+"/api/v1/projects", "", nil)
	if status != http.StatusOK {
		t.Fatalf("status %d:\n%s", status, body)
	}
	var list api.ProjectList
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Projects) != 1 || list.Projects[0].Name != "dash-read-shop" ||
		list.Projects[0].Worktrees[0].Domain != "dash-read-shop.localhost" {
		t.Fatalf("projects: %+v", list.Projects)
	}
}

// projectEvents opens the Dashboard's live stream at url and returns its
// `projects` events as they arrive. The channel is closed when the stream
// ends.
func projectEvents(t *testing.T, d *testkit.Daemon, url string) <-chan api.ProjectList {
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
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status %d, Content-Type %q:\n%s", url, resp.StatusCode, resp.Header.Get("Content-Type"), b)
	}
	events := make(chan api.ProjectList, 100)
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
			case strings.HasPrefix(line, "data: ") && event == "projects":
				var list api.ProjectList
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &list); err != nil {
					t.Errorf("bad projects event %q: %v", line, err)
					return
				}
				events <- list
			}
		}
	}()
	return events
}

// waitForEvent reads events until one satisfies cond, failing the test if
// none does within a few seconds.
func waitForEvent(t *testing.T, events <-chan api.ProjectList, what string, cond func(api.ProjectList) bool) {
	t.Helper()
	timeout := time.After(discoveryTimeout)
	for {
		select {
		case list, ok := <-events:
			if !ok {
				t.Fatalf("the stream ended while waiting for %s", what)
			}
			if cond(list) {
				return
			}
		case <-timeout:
			t.Fatalf("timed out after %s waiting for an event with %s", discoveryTimeout, what)
		}
	}
}

// worktreeIn finds the Worktree served at domain in list.
func worktreeIn(list api.ProjectList, domain string) (api.Worktree, bool) {
	for _, p := range list.Projects {
		for _, w := range p.Worktrees {
			if w.Domain == domain {
				return w, true
			}
		}
	}
	return api.Worktree{}, false
}

func TestTheDashboardIsToldAboutChangesAsTheyHappen(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-live")
	events := projectEvents(t, d, dash+"/api/v1/events")
	waitForEvent(t, events, "no Projects", func(l api.ProjectList) bool { return len(l.Projects) == 0 })

	repo, _ := pidProject(t, "dash-live-shop", "")
	d.MustRunCLI(repo.Dir, "link")
	waitForEvent(t, events, "the Linked Project, stopped", func(l api.ProjectList) bool {
		w, ok := worktreeIn(l, "dash-live-shop.localhost")
		return ok && w.State == "stopped"
	})

	d.MustRunCLI(repo.Dir, "up")
	waitForEvent(t, events, "its Worktree running", func(l api.ProjectList) bool {
		w, ok := worktreeIn(l, "dash-live-shop.localhost")
		return ok && w.State == "running" && len(w.Processes) == 1 && w.Processes[0].State == "running"
	})

	d.MustRunCLI(repo.Dir, "worktree", "alias", "api")
	waitForEvent(t, events, "its Alias", func(l api.ProjectList) bool {
		w, _ := worktreeIn(l, "dash-live-shop.localhost")
		return slices.Equal(w.Aliases, []string{"api.dash-live-shop.localhost"})
	})

	repo.AddWorktree("feature")
	waitForEvent(t, events, "the new Worktree, Ready", func(l api.ProjectList) bool {
		w, ok := worktreeIn(l, "feature.dash-live-shop.localhost")
		return ok && w.Setup != nil && w.Setup.State == api.SetupReady
	})

	// The stream ends when the daemon stops, rather than holding it up.
	d.Stop()
	for range events {
	}
}

func TestTheDashboardStartsStopsAndRestartsSingleProcesses(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-procs")
	repo, _, otherPID := modesProject(t, "dash-procs-shop", "manual")
	d.MustRunCLI(repo.Dir, "link")
	domain := "dash-procs-shop.localhost"
	events := projectEvents(t, d, dash+"/api/v1/events")

	// The Dashboard is told every Process the config declares, so it can
	// offer to start the manual one before anything runs.
	waitForEvent(t, events, "the declared Processes", func(l api.ProjectList) bool {
		w, ok := worktreeIn(l, domain)
		return ok && len(w.Declared) == 2 &&
			w.Declared[0] == api.DeclaredProcess{Name: "other", StartMode: "manual", HTTP: true} &&
			w.Declared[1] == api.DeclaredProcess{Name: "web", StartMode: "lazy", Primary: true, HTTP: true}
	})

	control := func(action, process string) {
		t.Helper()
		status, body := dashboardRequest(t, d, http.MethodPost, dash+"/api/v1/worktree/"+action, dash,
			api.ControlRequest{Dir: repo.Dir, Process: process})
		if status != http.StatusOK {
			t.Fatalf("%s %s: status %d:\n%s", action, process, status, body)
		}
	}
	processState := func(l api.ProjectList, name string) string {
		w, _ := worktreeIn(l, domain)
		for _, p := range w.Processes {
			if p.Name == name {
				return p.State
			}
		}
		return "stopped"
	}

	control("up", "other")
	first := waitForPID(t, otherPID)
	waitForEvent(t, events, "other running, web not", func(l api.ProjectList) bool {
		return processState(l, "other") == "running" && processState(l, "web") == "stopped"
	})

	control("restart", "other")
	waitForNewPID(t, otherPID, first)
	waitForExit(t, first)

	control("down", "other")
	waitForEvent(t, events, "other stopped", func(l api.ProjectList) bool {
		return processState(l, "other") == "stopped"
	})

	control("up", "")
	waitForEvent(t, events, "the Worktree running", func(l api.ProjectList) bool {
		w, _ := worktreeIn(l, domain)
		return w.State == "running" && processState(l, "web") == "running"
	})
	control("down", "")
	waitForEvent(t, events, "the Worktree stopped", func(l api.ProjectList) bool {
		w, _ := worktreeIn(l, domain)
		return w.State == "stopped" && len(w.Processes) == 0
	})
}

func TestAProjectCannotTakeTheDashboardsDomain(t *testing.T) {
	t.Parallel()
	d, _ := dashboardDaemon(t, "dash-taken")
	repo := gitfixture.New(t, "dash-taken-repo")

	res := d.RunCLI(repo.Dir, "link", "--name", "Dash-Taken")

	if res.ExitCode == 0 || !strings.Contains(res.Stderr, "https://dash-taken.localhost") {
		t.Fatalf("link: exit %d, want an error naming the Dashboard:\n%s", res.ExitCode, res.Output())
	}
	if projects, _ := d.Client().Projects(t.Context()); len(projects) != 0 {
		t.Fatalf("the Project was Linked anyway: %+v", projects)
	}
}

// assetRef matches the files a Dashboard page or module refers to: src and
// href attributes, and JavaScript imports.
var assetRef = regexp.MustCompile(`(?:src|href)="(/[^"]*)"|(?:from|import)\s*"(\.{0,2}/[^"]+)"`)

func TestEveryFileTheDashboardLoadsIsBuiltIn(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-files")

	contentTypes := map[string]string{".js": "text/javascript", ".css": "text/css", ".html": "text/html"}
	seen := map[string]bool{}
	queue := []string{"/"}
	for len(queue) > 0 {
		file := queue[0]
		queue = queue[1:]
		if seen[file] {
			continue
		}
		seen[file] = true
		res := d.Get(dash + file)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", file, res.StatusCode)
		}
		if want, ok := contentTypes[path.Ext(file)]; ok && !strings.HasPrefix(res.Header.Get("Content-Type"), want) {
			t.Errorf("%s: Content-Type %q, want %s", file, res.Header.Get("Content-Type"), want)
		}
		for _, m := range assetRef.FindAllStringSubmatch(res.Body, -1) {
			ref := m[1] + m[2]
			if !strings.HasPrefix(ref, "/") { // relative to this module
				ref = path.Join(path.Dir(file), ref)
			}
			queue = append(queue, path.Clean(ref))
		}
	}
	for _, want := range []string{"/app.js", "/style.css", "/panels/index.js", "/panels/worktrees.js", "/panels/logs.js",
		"/panels/services.js", "/panels/orphans.js", "/confirm.js", "/time.js"} {
		if !seen[want] {
			t.Errorf("the Dashboard never loads %s (loaded: %v)", want, slices.Sorted(maps.Keys(seen)))
		}
	}
}

func TestTheDashboardCannotBeFramedByOtherPages(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-frame")

	// A page that framed the Dashboard could trick a click on its buttons,
	// which would then come from the Dashboard's own origin.
	page := d.Navigate(dash + "/")

	if got := page.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options %q, want DENY", got)
	}
	if csp := page.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("Content-Security-Policy %q, want frame-ancestors 'none'", csp)
	}
}

// logStream opens a Process output stream (GET /v1/logs) through the
// Dashboard at url and returns its lines as they arrive. The channel is
// closed when the stream ends.
func logStream(t *testing.T, d *testkit.Daemon, url string) <-chan api.LogLine {
	t.Helper()
	client := d.HTTPS()
	client.Timeout = 0
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/x-ndjson") {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status %d, Content-Type %q:\n%s", url, resp.StatusCode, resp.Header.Get("Content-Type"), b)
	}
	lines := make(chan api.LogLine, 100)
	go func() {
		defer close(lines)
		dec := json.NewDecoder(resp.Body)
		for {
			var l api.LogLine
			if dec.Decode(&l) != nil {
				return
			}
			lines <- l
		}
	}()
	return lines
}

// waitForLogLines reads lines until every one of want ("<process> | <text>")
// has arrived, in any order (the output of several Processes is in the order
// Portby read it), failing the test if they don't within a few seconds.
func waitForLogLines(t *testing.T, lines <-chan api.LogLine, want ...string) {
	t.Helper()
	missing := map[string]bool{}
	for _, w := range want {
		missing[w] = true
	}
	timeout := time.After(discoveryTimeout)
	for len(missing) > 0 {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("the stream ended while waiting for %q", slices.Sorted(maps.Keys(missing)))
			}
			delete(missing, l.Process+" | "+l.Text)
		case <-timeout:
			t.Fatalf("timed out after %s waiting for %q", discoveryTimeout, slices.Sorted(maps.Keys(missing)))
		}
	}
}

// logsURL is the Dashboard's URL for the output of the Worktree at dir
// (one Process, or all of them when process is "").
func logsURL(dash, dir, process string, follow bool) string {
	q := url.Values{"dir": {dir}}
	if process != "" {
		q.Set("process", process)
	}
	if follow {
		q.Set("follow", "true")
	}
	return dash + "/api/v1/logs?" + q.Encode()
}

// logsProject is a Project with a Primary Process web and a Process worker
// that writes "worker went" once the file go exists in the Worktree.
func logsProject(t *testing.T, name string) *gitfixture.Repo {
	t.Helper()
	server := testkit.TestServer(t)
	return gitfixture.New(t, name).
		WriteFile("portby.toml", "[processes.web]\nprimary = true\ncommand = \"echo web started; exec "+server+"\"\n"+
			"[processes.worker]\ncommand = \"echo worker ready; while [ ! -e go ]; do sleep 0.01; done; echo worker went; exec sleep 600\"\n").
		Commit("declare web and worker")
}

func TestTheDashboardFollowsAllOfAWorktreesProcessesLabelledByName(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-logs-all")
	repo := logsProject(t, "dash-logs-all-shop")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "up")

	lines := logStream(t, d, logsURL(dash, repo.Dir, "", true))
	waitForLogLines(t, lines, "web | web started", "worker | worker ready")

	// New output arrives on the open stream, without asking again.
	repo.WriteFile("go", "")
	waitForLogLines(t, lines, "worker | worker went")

	// The stream ends when the daemon stops, rather than holding it up.
	d.Stop()
	for range lines {
	}
}

func TestTheDashboardShowsOneProcesssRecentOutputEvenAfterItStopped(t *testing.T) {
	t.Parallel()
	d, dash := dashboardDaemon(t, "dash-logs-one")
	repo := logsProject(t, "dash-logs-one-shop")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "up")
	lines := logStream(t, d, logsURL(dash, repo.Dir, "worker", true))
	waitForLogLines(t, lines, "worker | worker ready")
	repo.WriteFile("go", "")
	waitForLogLines(t, lines, "worker | worker went")

	d.MustRunCLI(repo.Dir, "down")

	var got []string
	for l := range logStream(t, d, logsURL(dash, repo.Dir, "worker", false)) {
		got = append(got, l.Process+" | "+l.Text)
	}
	if want := []string{"worker | worker ready", "worker | worker went"}; !slices.Equal(got, want) {
		t.Fatalf("worker's output after down:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
