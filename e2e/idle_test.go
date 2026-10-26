package e2e

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/clock"
	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// idleDaemon starts a daemon on a fake clock, so tests move time past the
// idle timeout instead of waiting for it.
func idleDaemon(t *testing.T) (*testkit.Daemon, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	return testkit.StartDaemon(t, testkit.WithClock(clk)), clk
}

// pidProject is a repo whose web Process writes its pid to a file outside
// the repo before serving, and the file's path. config is put before the
// Process in portby.toml.
func pidProject(t *testing.T, name, config string) (*gitfixture.Repo, string) {
	t.Helper()
	pidFile := filepath.Join(gitfixture.TempDir(t), "pid")
	repo := gitfixture.New(t, name).
		WriteFile("portby.toml", config+processConfig("echo $$ > "+pidFile+"; exec "+testkit.TestServer(t))).
		Commit("declare web Process")
	return repo, pidFile
}

// worktreeState is the state the API reports for the Worktree served at
// domain.
func worktreeState(t *testing.T, d *testkit.Daemon, domain string) api.Worktree {
	t.Helper()
	projects, err := d.Client().Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		for _, w := range p.Worktrees {
			if w.Domain == domain {
				return w
			}
		}
	}
	t.Fatalf("no Worktree is served at %s", domain)
	return api.Worktree{}
}

// waitForState waits until the Worktree served at domain is in state.
func waitForState(t *testing.T, d *testkit.Daemon, domain, state string) {
	t.Helper()
	var w api.Worktree
	eventually(t, domain+" to be "+state, func() bool {
		w = worktreeState(t, d, domain)
		return w.State == state
	})
}

// waitForExit waits until the process pid has exited.
func waitForExit(t *testing.T, pid int) {
	t.Helper()
	eventually(t, "the Process to stop", func() bool { return syscall.Kill(pid, 0) != nil })
}

func TestWorktreeWithNoTrafficGoesIdleAndItsProcessesStop(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	repo, pidFile := pidProject(t, "idle-basic", "idle_timeout = \"10m\"\n")
	d.MustRunCLI(repo.Dir, "link")
	d.GetServer("https://idle-basic.localhost/")
	pid := waitForPID(t, pidFile)
	waitForState(t, d, "idle-basic.localhost", "running")

	if delay := waitForTimer(t, clk); delay != 10*time.Minute {
		t.Fatalf("Idle scheduled after %s, want the 10m idle_timeout", delay)
	}
	clk.Advance(10 * time.Minute)

	waitForExit(t, pid)
	waitForState(t, d, "idle-basic.localhost", "idle")
	lines := strings.Split(strings.TrimRight(d.MustRunCLI(d.Home, "status").Stdout, "\n"), "\n")
	if got, want := strings.Fields(lines[len(lines)-1]), []string{"idle-basic", "idle-basic.localhost", repo.Dir, "idle"}; !slices.Equal(got, want) {
		t.Fatalf("status row %q, want %v", lines[len(lines)-1], want)
	}
}

func TestRequestsPostponeIdleAndTheNextRequestStartsItAgain(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	repo, pidFile := pidProject(t, "idle-busy", "idle_timeout = \"10m\"\n")
	d.MustRunCLI(repo.Dir, "link")
	d.GetServer("https://idle-busy.localhost/")
	pid := waitForPID(t, pidFile)
	waitForTimer(t, clk)

	clk.Advance(6 * time.Minute)
	d.GetServer("https://idle-busy.localhost/")
	clk.Advance(4 * time.Minute)

	// 10 minutes after the start, but only 4 after the last request.
	if delay := waitForTimer(t, clk); delay != 6*time.Minute {
		t.Fatalf("Idle rescheduled after %s, want 6m: 10m after the last request", delay)
	}
	if state := worktreeState(t, d, "idle-busy.localhost").State; state != "running" {
		t.Fatalf("state %q 4m after a request, want running", state)
	}
	clk.Advance(6 * time.Minute)
	waitForExit(t, pid)
	waitForState(t, d, "idle-busy.localhost", "idle")

	d.GetServer("https://idle-busy.localhost/")
	if newPID := waitForPID(t, pidFile); newPID == pid {
		t.Fatalf("request to an Idle Worktree was served by the stopped Process %d", pid)
	}
	waitForState(t, d, "idle-busy.localhost", "running")
}

// openWebSocket opens a WebSocket to the test server's /ws at domain through
// the proxy, as a live-reload client does, and checks it echoes.
func openWebSocket(t *testing.T, d *testkit.Daemon, domain string) *webSocket {
	t.Helper()
	conn, err := tls.Dial("tcp", d.ProxyAddr(), &tls.Config{
		RootCAs: d.CAPool(), ServerName: domain, NextProtos: []string{"http/1.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", domain)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("WebSocket upgrade: status %d", resp.StatusCode)
	}
	ws := &webSocket{Conn: conn, br: br}
	ws.echo(t, "hello")
	return ws
}

type webSocket struct {
	*tls.Conn
	br *bufio.Reader
}

// echo checks the WebSocket's server sends msg back.
func (ws *webSocket) echo(t *testing.T, msg string) {
	t.Helper()
	if _, err := ws.Write([]byte(msg)); err != nil {
		t.Fatalf("writing to the WebSocket: %v", err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(ws.br, got); err != nil || string(got) != msg {
		t.Fatalf("echo of %q: got %q, %v", msg, got, err)
	}
}

func TestAnOpenWebSocketKeepsTheWorktreeActive(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	repo, pidFile := pidProject(t, "idle-ws", "idle_timeout = \"10m\"\n")
	d.MustRunCLI(repo.Dir, "link")
	d.GetServer("https://idle-ws.localhost/")
	pid := waitForPID(t, pidFile)
	waitForTimer(t, clk)

	ws := openWebSocket(t, d, "idle-ws.localhost")
	clk.Advance(time.Hour)

	ws.echo(t, "still there")
	ws.Close()
	// Idle is counted from when the connection closed.
	if delay := waitForTimer(t, clk); delay != 10*time.Minute {
		t.Fatalf("Idle scheduled %s after the WebSocket closed, want 10m", delay)
	}
	if state := worktreeState(t, d, "idle-ws.localhost").State; state != "running" {
		t.Fatalf("state %q after an hour with a WebSocket open, want running", state)
	}
	clk.Advance(10 * time.Minute)
	waitForExit(t, pid)
	waitForState(t, d, "idle-ws.localhost", "idle")
}

func TestTheIdleTimeoutIsFifteenMinutesByDefault(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	repo, _ := pidProject(t, "idle-default", "")
	d.MustRunCLI(repo.Dir, "link")
	d.GetServer("https://idle-default.localhost/")

	if delay := waitForTimer(t, clk); delay != 15*time.Minute {
		t.Fatalf("Idle scheduled after %s, want 15m", delay)
	}
}

func TestTheGlobalIdleTimeoutAppliesUnlessTheProjectSetsItsOwn(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	global := filepath.Join(d.Home, ".config", "portby", "config.toml")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte("idle_timeout = \"5m\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plain, plainPID := pidProject(t, "idle-global", "")
	own, _ := pidProject(t, "idle-own", "idle_timeout = \"1h\"\n")
	d.MustRunCLI(plain.Dir, "link")
	d.MustRunCLI(own.Dir, "link")

	d.GetServer("https://idle-global.localhost/")
	if delay := waitForTimer(t, clk); delay != 5*time.Minute {
		t.Fatalf("Idle scheduled after %s, want the global 5m", delay)
	}
	clk.Advance(5 * time.Minute)
	waitForExit(t, waitForPID(t, plainPID))

	d.GetServer("https://idle-own.localhost/")
	if delay := waitForTimer(t, clk); delay != time.Hour {
		t.Fatalf("Idle scheduled after %s, want the Project's 1h", delay)
	}
}
