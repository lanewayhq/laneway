package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// listeningDaemon starts a daemon that listens on extraAddr beside
// loopback, as one set up for other devices does.
func listeningDaemon(t *testing.T) *testkit.Daemon {
	t.Helper()
	d := testkit.StartDaemon(t)
	writeGlobalConfig(t, d, `listen = ["`+extraAddr+`"]`+"\n")
	d.Restart()
	return d
}

func TestSettingTheRootDomainWithWorkingDNSSwitchesEveryNameLive(t *testing.T) {
	t.Parallel()
	d := listeningDaemon(t)
	repo, log := appURLProject(t, "rdc-set")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	if got := d.GetServer("https://rdc-set.localhost/").Name; got != "https://rdc-set.localhost" {
		t.Fatalf("the main Worktree's Process started with APP_URL %q", got)
	}
	started := starts(t, log)
	d.Platform.SetHost("*.rdc-set.example.test", extraAddr)

	out := d.MustRunCLI(repo.Dir, "root-domain", "set", "rdc-set.example.test").Stdout

	if got := getServerAt(t, d, extraAddr, "https://rdc-set.rdc-set.example.test/").Name; got != "https://rdc-set.rdc-set.example.test" {
		t.Fatalf("after set, the main Worktree's Process has APP_URL %q, want the new one", got)
	}
	if n := starts(t, log) - started; n != 1 {
		t.Errorf("the Process started %d times for the change, want once: no daemon restart", n)
	}
	for _, want := range []string{
		"Root Domain is now rdc-set.example.test (was localhost)",
		"https://laneway.rdc-set.example.test/",
		"Applied the Env Patch of 1 Worktree again",
		"Restarted rdc-set.rdc-set.example.test: web",
		"http://laneway.rdc-set.example.test/ca",
	} {
		assertContains(t, out, want)
	}
	assertContains(t, readFile(t, globalConfig(d)), `root_domain = "rdc-set.example.test"`)
}

func TestSettingTheRootDomainWithoutWorkingDNSChangesNothingAndNamesTheRecord(t *testing.T) {
	t.Parallel()
	d := listeningDaemon(t)
	repo := routedProject(t, "rdc-nodns")
	d.MustRunCLI(repo.Dir, "link")
	configBefore := readFile(t, globalConfig(d))

	for name, setup := range map[string]func(){
		"missing":   func() {},
		"elsewhere": func() { d.Platform.SetHost("*.rdc-nodns.example.test", "203.0.113.9") },
		"only the Dashboard's name": func() {
			d.Platform.SetHost("*.rdc-nodns.example.test")
			d.Platform.SetHost("laneway.rdc-nodns.example.test", extraAddr)
		},
	} {
		setup()

		res := d.RunCLI(repo.Dir, "root-domain", "set", "rdc-nodns.example.test")

		if res.ExitCode == 0 {
			t.Fatalf("DNS %s: set succeeded, want it to fail:\n%s", name, res.Stdout)
		}
		assertContains(t, res.Stderr, "*.rdc-nodns.example.test A "+extraAddr)
		assertContains(t, res.Stderr, "Nothing was changed")
		if got := readFile(t, globalConfig(d)); got != configBefore {
			t.Errorf("DNS %s: the global config changed to:\n%s", name, got)
		}
		if got := d.GetServer("https://rdc-nodns.localhost/").Name; got != "web@rdc-nodns" {
			t.Fatalf("DNS %s: the Domain under localhost reached %q, want the Worktree still served there", name, got)
		}
	}
}

func TestSettingTheRootDomainWithTheDNSCheckSkippedSwitchesAnyway(t *testing.T) {
	t.Parallel()
	d := listeningDaemon(t)
	repo := routedProject(t, "rdc-skip")
	d.MustRunCLI(repo.Dir, "link")

	out := d.MustRunCLI(repo.Dir, "root-domain", "set", "--skip-dns-check", "rdc-skip.example.test").Stdout

	assertContains(t, out, "Root Domain is now rdc-skip.example.test")
	if got := d.GetServer("https://rdc-skip.rdc-skip.example.test/").Name; got != "web@rdc-skip" {
		t.Fatalf("the Domain under the new Root Domain reached %q, want the main Worktree", got)
	}
}

func TestSettingAnInvalidRootDomainIsRefused(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	writeGlobalConfig(t, d, "# mine\nidle_timeout = \"1h\"\n")

	for _, root := range []string{"https://lw.example.test", "lw.example.test:8443", "lw.example.test/x",
		"lw.example.test.", "100.101.102.103", "LW.example.test", "lw..example.test", "-lw.example.test",
		"lw.localhost", tooLongRoot} {
		res := d.RunCLI(d.Home, "root-domain", "set", "--skip-dns-check", "--", root)

		if res.ExitCode == 0 {
			t.Errorf("set %q succeeded, want it refused:\n%s", root, res.Stdout)
		}
		assertContains(t, res.Stderr, "Root Domain")
	}
	if got := readFile(t, globalConfig(d)); got != "# mine\nidle_timeout = \"1h\"\n" {
		t.Errorf("the global config changed to:\n%s", got)
	}
}

func TestResettingTheRootDomainServesLocalhostNamesAgain(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo, log := appURLProject(t, "rdc-reset")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("t3-5c1d")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	d.MustRunCLI(repo.Dir, "root-domain", "set", "--skip-dns-check", "rdc-reset.example.test")
	if got := d.GetServer("https://rdc-reset.rdc-reset.example.test/").Name; got != "https://rdc-reset.rdc-reset.example.test" {
		t.Fatalf("under the Root Domain, the Process started with APP_URL %q", got)
	}
	started := starts(t, log)

	out := d.MustRunCLI(repo.Dir, "root-domain", "reset").Stdout

	if got := d.GetServer("https://rdc-reset.localhost/").Name; got != "https://rdc-reset.localhost" {
		t.Fatalf("after reset, the Process has APP_URL %q, want the .localhost one", got)
	}
	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "APP_URL=https://t3-5c1d.rdc-reset.localhost\n")
	if n := starts(t, log) - started; n != 1 {
		t.Errorf("Processes started %d times for reset, want 1: only the running one restarts", n)
	}
	assertContains(t, out, "Root Domain is now localhost (was rdc-reset.example.test)")
	assertContains(t, out, "Applied the Env Patch of 2 Worktrees again")
	assertContains(t, out, "Restarted rdc-reset.localhost: web\n")
	if strings.Contains(out, "t3-5c1d") || strings.Contains(out, "/ca") {
		t.Errorf("reset names a Worktree that wasn't running, or the CA page:\n%s", out)
	}
	assertContains(t, readFile(t, globalConfig(d)), `root_domain = "localhost"`)
}

func TestSettingTheRootDomainKeepsTheConfigsOtherKeysAndComments(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	for i, tt := range []struct{ name, before, after string }{
		{"no file", "", "root_domain = \"%s\"\n"},
		{"keys, comments and a table",
			"# Laneway, for me\nidle_timeout = \"1h\" # long\nlisten = [\n  \"127.0.0.2\", # [not a table]\n]\n\n# Plugins' settings\n[extra]\nkey = \"[x]\"\n",
			"# Laneway, for me\nidle_timeout = \"1h\" # long\nlisten = [\n  \"127.0.0.2\", # [not a table]\n]\nroot_domain = \"%s\"\n\n# Plugins' settings\n[extra]\nkey = \"[x]\"\n"},
		{"an earlier root_domain",
			"idle_timeout = \"1h\"\nroot_domain = \"old.example.test\" # mine\nplugins = []\n",
			"idle_timeout = \"1h\"\nroot_domain = \"%s\" # mine\nplugins = []\n"},
		{"only a table",
			"# Plugins' settings\n[extra]\nroot_domain = \"not this one\"\n",
			"root_domain = \"%s\"\n\n# Plugins' settings\n[extra]\nroot_domain = \"not this one\"\n"},
		{"no newline at the end", "idle_timeout = \"1h\"", "idle_timeout = \"1h\"\nroot_domain = \"%s\"\n"},
		{"a multi-line string", "note = \"\"\"\n[not a table]\nroot_domain = \"no\"\n\"\"\"\n",
			"note = \"\"\"\n[not a table]\nroot_domain = \"no\"\n\"\"\"\nroot_domain = \"%s\"\n"},
	} {
		root := fmt.Sprintf("rdc-keep-%d.example.test", i)
		if tt.before == "" {
			os.Remove(globalConfig(d))
		} else {
			writeGlobalConfig(t, d, tt.before)
		}

		d.MustRunCLI(d.Home, "root-domain", "set", "--skip-dns-check", root)

		if got, want := readFile(t, globalConfig(d)), fmt.Sprintf(tt.after, root); got != want {
			t.Errorf("%s: the global config is\n%s\nwant\n%s", tt.name, got, want)
		}
	}
}

func TestRootDomainWithNoArgumentsReportsTheCurrentState(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Platform.SetNoTailscale("Tailscale is stopped.")
	writeGlobalConfig(t, d, `listen = ["`+extraAddr+`", "tailscale"]`+"\n")
	d.Restart()

	out := d.MustRunCLI(d.Home, "root-domain").Stdout

	for _, want := range []string{
		"Root Domain:  localhost\n",
		"Dashboard:    https://laneway.localhost/\n",
		"127.0.0.1 (loopback)\n",
		extraAddr + " (listening)\n",
		"tailscale (not listening: Tailscale is stopped.)\n",
		"DNS:          not needed",
	} {
		assertContains(t, out, want)
	}

	d.Platform.SetHost("*.rdc-show.example.test", extraAddr)
	d.MustRunCLI(d.Home, "root-domain", "set", "rdc-show.example.test")

	out = d.MustRunCLI(d.Home, "root-domain").Stdout

	assertContains(t, out, "Root Domain:  rdc-show.example.test\n")
	assertContains(t, out, "CA page:      http://laneway.rdc-show.example.test/ca\n")
	assertContains(t, out, "DNS:          names under rdc-show.example.test resolve to "+extraAddr+", where Laneway listens\n")

	d.Platform.SetHost("*.rdc-show.example.test", "203.0.113.9")

	out = d.MustRunCLI(d.Home, "root-domain").Stdout

	assertContains(t, out, "resolves to 203.0.113.9, where Laneway doesn't listen")
	assertContains(t, out, "create the record *.rdc-show.example.test A "+extraAddr+"\n")
}

// daemonEvents are the `daemon` events of the Dashboard's live updates at
// url.
func daemonEvents(t *testing.T, d *testkit.Daemon, url string) <-chan api.DaemonInfo {
	t.Helper()
	client := d.HTTPS()
	client.Timeout = 0
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	events := make(chan api.DaemonInfo, 100)
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
			case strings.HasPrefix(line, "data: ") && event == "daemon":
				var info api.DaemonInfo
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &info); err != nil {
					t.Errorf("bad daemon event %q: %v", line, err)
					return
				}
				events <- info
			}
		}
	}()
	return events
}

func TestTheDashboardsLiveUpdatesAnnounceANewRootDomain(t *testing.T) {
	t.Parallel()
	d := personalRootDomainDaemon(t, "rdc-live.example.test")
	events := daemonEvents(t, d, "https://laneway.rdc-live.example.test/api/v1/events")

	d.MustRunCLI(d.Home, "root-domain", "set", "--skip-dns-check", "rdc-live2.example.test")

	timeout := time.After(discoveryTimeout)
	for {
		select {
		case info, ok := <-events:
			if !ok {
				t.Fatal("the stream ended before announcing the new Root Domain")
			}
			if info.RootDomain == "rdc-live2.example.test" {
				if info.Dashboard != "https://laneway.rdc-live2.example.test/" || info.CAPage != "http://laneway.rdc-live2.example.test/ca" {
					t.Errorf("the event gives the Dashboard %q and CA page %q, want them under the new Root Domain", info.Dashboard, info.CAPage)
				}
				return
			}
		case <-timeout:
			t.Fatal("no daemon event announced the new Root Domain")
		}
	}
}
