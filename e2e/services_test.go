package e2e

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// serviceProject is a repo whose portby.toml declares the Services in
// services (TOML lines for the [services] table, such as `redis = "7"`),
// after config.
func serviceProject(t *testing.T, name, services, config string) *gitfixture.Repo {
	t.Helper()
	return gitfixture.New(t, name).
		WriteFile("portby.toml", config+"[services]\n"+services+"\n").
		Commit("declare Services")
}

// redisConn is a client connection to a Redis Service.
type redisConn struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

// dialRedis connects to the Service listening on port on the daemon's
// Service host.
func dialRedis(t *testing.T, d *testkit.Daemon, port int) *redisConn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(d.ServiceHost(), strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		t.Fatalf("connecting to the Service on port %d: %v", port, err)
	}
	t.Cleanup(func() { conn.Close() })
	return &redisConn{t: t, conn: conn, r: bufio.NewReader(conn)}
}

// send writes one command without waiting for its reply.
func (c *redisConn) send(args ...string) {
	c.t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := c.conn.Write([]byte(b.String())); err != nil {
		c.t.Fatalf("sending %v: %v", args, err)
	}
}

// reply reads one reply line, such as "+PONG", or the value of a bulk
// string.
func (c *redisConn) reply() string {
	c.t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := c.r.ReadString('\n')
	if err != nil {
		c.t.Fatalf("reading a reply: %v", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if strings.HasPrefix(line, "$") && line != "$-1" {
		value, err := c.r.ReadString('\n')
		if err != nil {
			c.t.Fatalf("reading a bulk reply: %v", err)
		}
		return strings.TrimRight(value, "\r\n")
	}
	return line
}

// do sends a command and returns its reply.
func (c *redisConn) do(args ...string) string {
	c.t.Helper()
	c.send(args...)
	return c.reply()
}

func TestTheFirstConnectionToAServiceStartsItAndIsForwardedOnceItIsReady(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := serviceProject(t, "svc-first", `redis = "7"`, "")
	d.MustRunCLI(repo.Dir, "link")
	if calls := d.Engine.Calls(); containsCall(calls, "start") {
		t.Fatalf("linking started a container: %v", calls)
	}

	release := d.Engine.HoldStarts()
	c := dialRedis(t, d, 6379)
	c.send("AUTH", "portby")
	eventually(t, "the container to be started", func() bool { return containsCall(d.Engine.Calls(), "start") })
	c.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if n, _ := c.r.Peek(1); len(n) > 0 {
		t.Fatalf("got a reply before the container was ready: %q", n)
	}
	release()

	if got := c.reply(); got != "+OK" {
		t.Fatalf("AUTH = %q, want +OK", got)
	}
	if got := c.do("PING"); got != "+PONG" {
		t.Fatalf("PING = %q, want +PONG", got)
	}
}

// containsCall reports whether one of the fake engine's calls starts with
// prefix, such as "start".
func containsCall(calls []string, prefix string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func TestConcurrentFirstConnectionsShareOneStart(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := serviceProject(t, "svc-concurrent", `redis = "7"`, "")
	d.MustRunCLI(repo.Dir, "link")

	release := d.Engine.HoldStarts()
	var conns []*redisConn
	for range 5 {
		c := dialRedis(t, d, 6379)
		c.send("AUTH", "portby")
		conns = append(conns, c)
	}
	eventually(t, "the container to be started", func() bool { return containsCall(d.Engine.Calls(), "start") })
	release()

	for i, c := range conns {
		if got := c.reply(); got != "+OK" {
			t.Fatalf("connection %d: AUTH = %q, want +OK", i, got)
		}
	}
	if n := countCalls(d.Engine.Calls(), "start"); n != 1 {
		t.Fatalf("the container was started %d times, want once: %v", n, d.Engine.Calls())
	}
}

func countCalls(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// serviceState is the state `portby services` shows for the Service of
// engine at version.
func serviceState(t *testing.T, d *testkit.Daemon, engine, version string) string {
	t.Helper()
	for _, line := range strings.Split(d.MustRunCLI(d.Home, "services").Stdout, "\n")[1:] {
		f := strings.Fields(line)
		if len(f) == 5 && f[0] == engine && f[1] == version {
			return f[4]
		}
	}
	t.Fatalf("`portby services` doesn't list %s:%s", engine, version)
	return ""
}

func waitForServiceState(t *testing.T, d *testkit.Daemon, engine, version, state string) {
	t.Helper()
	eventually(t, engine+":"+version+" to be "+state, func() bool { return serviceState(t, d, engine, version) == state })
}

func TestAServiceWithNoConnectionsStopsAfterItsIdleTimeout(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	repo := serviceProject(t, "svc-idle", `redis = "7"`, "")
	d.MustRunCLI(repo.Dir, "link")
	c := dialRedis(t, d, 6379)
	if got := c.do("AUTH", "portby"); got != "+OK" {
		t.Fatalf("AUTH = %q", got)
	}
	c.do("SET", "greeting", "hello")
	c.conn.Close()

	if delay := waitForTimer(t, clk); delay != 15*time.Minute {
		t.Fatalf("Idle scheduled after %s, want the default 15m", delay)
	}
	clk.Advance(15 * time.Minute)

	waitForServiceState(t, d, "redis", "7", "idle")
	if d.Engine.IsRunning("portby-redis-7") {
		t.Fatal("the container is still running")
	}
	// The next connection starts it again, with its data.
	c = dialRedis(t, d, 6379)
	c.do("AUTH", "portby")
	if got := c.do("GET", "greeting"); got != "hello" {
		t.Fatalf("GET after Idle = %q, want the value set before", got)
	}
	if n := countCalls(d.Engine.Calls(), "start"); n != 2 {
		t.Fatalf("started %d times, want twice: %v", n, d.Engine.Calls())
	}
}

func TestAnOpenConnectionKeepsAServiceRunning(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	repo := serviceProject(t, "svc-open", `redis = "7"`, "")
	d.MustRunCLI(repo.Dir, "link")
	c := dialRedis(t, d, 6379)
	c.do("AUTH", "portby")

	clk.Advance(time.Hour)

	if got := c.do("PING"); got != "+PONG" {
		t.Fatalf("PING an hour later = %q", got)
	}
	if state := serviceState(t, d, "redis", "7"); state != "running" {
		t.Fatalf("state %q with a connection open, want running", state)
	}
	c.conn.Close()
	if delay := waitForTimer(t, clk); delay != 15*time.Minute {
		t.Fatalf("Idle scheduled after %s once the connection closed, want 15m", delay)
	}
}

func TestAServiceStaysUpWhileAWorktreeUsingItRuns(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	repo, pidFile := pidProject(t, "svc-used", "idle_timeout = \"1h\"\n[services]\nredis = \"7\"\n")
	d.MustRunCLI(repo.Dir, "link")
	d.GetServer("https://svc-used.localhost/")
	waitForPID(t, pidFile)
	c := dialRedis(t, d, 6379)
	c.do("AUTH", "portby")
	c.conn.Close()

	// Only the Worktree's own idle timeout is pending: the Service waits
	// for the Worktree.
	if delay := waitForTimer(t, clk); delay != time.Hour {
		t.Fatalf("first timer after %s, want the Worktree's 1h idle_timeout", delay)
	}
	clk.Advance(30 * time.Minute)
	if state := serviceState(t, d, "redis", "7"); state != "running" {
		t.Fatalf("Service %s while its Worktree runs, want running", state)
	}

	clk.Advance(30 * time.Minute)
	waitForState(t, d, "svc-used.localhost", "idle")
	if delay := waitForTimer(t, clk); delay != 15*time.Minute {
		t.Fatalf("Service Idle scheduled after %s once the Worktree was Idle, want 15m", delay)
	}
	clk.Advance(15 * time.Minute)
	waitForServiceState(t, d, "redis", "7", "idle")
}

// alwaysServiceProject is a repo using Redis whose Processes are web, the
// Primary Process (with the given Start Mode), and worker, always kept
// running, and the files their pids are written to.
func alwaysServiceProject(t *testing.T, name, webMode string) (repo *gitfixture.Repo, webPID, workerPID string) {
	t.Helper()
	dir := gitfixture.TempDir(t)
	webPID, workerPID = filepath.Join(dir, "web.pid"), filepath.Join(dir, "worker.pid")
	repo = gitfixture.New(t, name).
		WriteFile("portby.toml", fmt.Sprintf("idle_timeout = \"1h\"\n[services]\nredis = \"7\"\n"+
			"[processes.web]\ncommand = %q\nprimary = true\nstart_mode = %q\n"+
			"[processes.worker]\ncommand = %q\nstart_mode = \"always\"\n",
			"echo $$ > "+webPID+"; exec "+testkit.TestServer(t), webMode,
			"echo $$ > "+workerPID+"; exec sleep 600")).
		Commit("declare Processes using Redis")
	return repo, webPID, workerPID
}

func TestAServiceStaysUpWhileAWorktreeRunsOnlyAlwaysProcesses(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	repo, _, workerPID := alwaysServiceProject(t, "svc-always", "always")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	waitForPID(t, workerPID)

	// Started by hand, with no Worktree using it, the Service would
	// schedule its idle timeout at once.
	d.MustRunCLI(d.Home, "services", "up", "redis")
	if n := clk.Pending(); n != 0 {
		t.Fatalf("%d timers pending while the Worktree's always Processes run, want none", n)
	}

	d.MustRunCLI(repo.Dir, "down")
	if delay := waitForTimer(t, clk); delay != 15*time.Minute {
		t.Fatalf("first timer after down is %s, want the Service's 15m", delay)
	}
	clk.Advance(15 * time.Minute)
	waitForServiceState(t, d, "redis", "7", "idle")
}

func TestAServiceStaysUpWhileAnIdleWorktreesAlwaysProcessesRun(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	repo, webPID, workerPID := alwaysServiceProject(t, "svc-idle-always", "lazy")
	d.MustRunCLI(repo.Dir, "link")
	d.GetServer("https://svc-idle-always.localhost/")
	web := waitForPID(t, webPID)
	waitForPID(t, workerPID)
	c := dialRedis(t, d, 6379)
	c.do("AUTH", "portby")
	c.conn.Close()

	if delay := waitForTimer(t, clk); delay != time.Hour {
		t.Fatalf("first timer after %s, want the Worktree's 1h idle_timeout", delay)
	}
	clk.Advance(time.Hour)
	waitForState(t, d, "svc-idle-always.localhost", "idle")
	// Idle lets go of the Services before it stops the lazy Processes.
	waitForExit(t, web)
	if n := clk.Pending(); n != 0 {
		t.Fatalf("%d timers pending while the Idle Worktree's always Process runs, want none", n)
	}
	if state := serviceState(t, d, "redis", "7"); state != "running" {
		t.Fatalf("Service %s while an always Process using it runs, want running", state)
	}
}

func TestDownReleasesTheWorktreesServices(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	repo, pidFile := pidProject(t, "svc-down", "idle_timeout = \"1h\"\n[services]\nredis = \"7\"\n")
	d.MustRunCLI(repo.Dir, "link")
	d.GetServer("https://svc-down.localhost/")
	waitForPID(t, pidFile)
	c := dialRedis(t, d, 6379)
	c.do("AUTH", "portby")
	c.conn.Close()

	d.MustRunCLI(repo.Dir, "down")

	// The Worktree no longer holds the Service up: its idle timer is the
	// only one pending.
	if delay := waitForTimer(t, clk); delay != 15*time.Minute {
		t.Fatalf("first timer after down is %s, want the Service's 15m", delay)
	}
	clk.Advance(15 * time.Minute)
	waitForServiceState(t, d, "redis", "7", "idle")
}

// serviceRow is the `portby services` row of engine at version: ENGINE
// VERSION HOST PORT STATE.
func serviceRow(t *testing.T, d *testkit.Daemon, engine, version string) []string {
	t.Helper()
	for _, line := range strings.Split(d.MustRunCLI(d.Home, "services").Stdout, "\n")[1:] {
		if f := strings.Fields(line); len(f) == 5 && f[0] == engine && f[1] == version {
			return f
		}
	}
	t.Fatalf("`portby services` doesn't list %s:%s", engine, version)
	return nil
}

func TestTheDefaultVersionGetsTheStandardPortAndOthersAStableDerivedOne(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(serviceProject(t, "svc-port-default", `redis = "7"`, "").Dir, "link")
	d.MustRunCLI(serviceProject(t, "svc-port-other", `redis = "6.2"`, "").Dir, "link")

	want := map[string][]string{
		"7":   {"redis", "7", d.ServiceHost(), "6379", "stopped"},
		"6.2": {"redis", "6.2", d.ServiceHost(), "63762", "stopped"},
	}
	for version, row := range want {
		if got := serviceRow(t, d, "redis", version); !slices.Equal(got, row) {
			t.Errorf("redis:%s row %v, want %v", version, got, row)
		}
	}
	c := dialRedis(t, d, 63762)
	c.do("AUTH", "portby")
	if got := c.do("PING"); got != "+PONG" {
		t.Fatalf("PING on the derived port = %q", got)
	}
	if !d.Engine.IsRunning("portby-redis-6.2") || d.Engine.IsRunning("portby-redis-7") {
		t.Fatalf("containers %v: want only portby-redis-6.2 running", d.Engine.Containers())
	}

	d.Restart()
	if d.Engine.IsRunning("portby-redis-6.2") {
		t.Fatal("the daemon stopping left its Service running")
	}
	for version, row := range want {
		if got := serviceRow(t, d, "redis", version); !slices.Equal(got, row) {
			t.Errorf("after a restart, redis:%s row %v, want %v", version, got, row)
		}
	}
	c = dialRedis(t, d, 63762)
	c.do("AUTH", "portby")
	if got := c.do("PING"); got != "+PONG" {
		t.Fatalf("PING on the derived port after a restart = %q", got)
	}
}

func TestServicesUpAndDownStartAndStopAService(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(serviceProject(t, "svc-updown", `redis = "7"`, "").Dir, "link")

	out := d.MustRunCLI(d.Home, "services", "up", "redis").Stdout
	assertContains(t, out, "redis:7 is running at "+d.ServiceHost()+":6379")
	if !d.Engine.IsRunning("portby-redis-7") {
		t.Fatal("`services up` didn't start the container")
	}
	c := dialRedis(t, d, 6379)
	c.do("AUTH", "portby")

	out = d.MustRunCLI(d.Home, "services", "down", "redis:7").Stdout
	assertContains(t, out, "redis:7 is stopped")
	if d.Engine.IsRunning("portby-redis-7") {
		t.Fatal("`services down` didn't stop the container")
	}
	c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.r.ReadByte(); err == nil {
		t.Fatal("a connection stayed open after `services down`")
	}

	// The next connection starts it again.
	c = dialRedis(t, d, 6379)
	c.do("AUTH", "portby")
	if got := c.do("PING"); got != "+PONG" {
		t.Fatalf("PING after down = %q", got)
	}
}

func TestServicesUpOfAnUnknownServiceNamesTheKnownOnes(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(serviceProject(t, "svc-unknown", `redis = "7"`, "").Dir, "link")

	res := d.RunCLI(d.Home, "services", "up", "memcached")
	if res.ExitCode != 1 {
		t.Fatalf("exit code %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, `there is no Service "memcached"; the Services are redis:7`)
}

func TestAServiceWithoutAContainerEngineFailsClearly(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Engine.SetUnavailable(errors.New("nothing answered at /run/user/1000/podman/podman.sock"))
	d.MustRunCLI(serviceProject(t, "svc-noengine", `redis = "7"`, "").Dir, "link")

	res := d.RunCLI(d.Home, "services", "up")
	if res.ExitCode != 1 {
		t.Fatalf("exit code %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, "Service redis:7 could not be started: no container engine is available")

	// A client is disconnected rather than left waiting.
	c := dialRedis(t, d, 6379)
	c.send("PING")
	c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.r.ReadByte(); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("reading from the Service: %v, want the connection closed", err)
	}
}

func TestServiceHostPortAndPasswordAreEnvPatchVariables(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := serviceProject(t, "svc-env", `redis = "6.2"`,
		"[env]\nREDIS_HOST = \"{redis_host}\"\nREDIS_PORT = \"{redis_port}\"\nREDIS_PASSWORD = \"{redis_password}\"\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	env := readFile(t, filepath.Join(repo.Dir, ".env"))
	for _, line := range []string{"REDIS_HOST=" + d.ServiceHost(), "REDIS_PORT=63762", "REDIS_PASSWORD=portby"} {
		assertContains(t, env, line+"\n")
	}
	// What the Env Patch says is what works.
	c := dialRedis(t, d, 63762)
	if got := c.do("AUTH", "portby"); got != "+OK" {
		t.Fatalf("AUTH with the Env Patch's password = %q", got)
	}
}

func TestAServiceVariableNeedsTheServiceDeclared(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "svc-env-undeclared").
		WriteFile("portby.toml", "[env]\nREDIS_PORT = \"{redis_port}\"\n").
		Commit("use redis without declaring it")

	res := d.RunCLI(repo.Dir, "link", "--yes")
	if res.ExitCode == 0 {
		t.Fatalf("link succeeded:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "{redis_port} has no value for this Worktree")
}

func TestLinkRejectsAnUnknownServiceNamingTheFileAndKey(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := serviceProject(t, "svc-bad-engine", `memcached = "1.6"`, "")

	res := d.RunCLI(repo.Dir, "link")
	if res.ExitCode == 0 {
		t.Fatalf("link succeeded:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, filepath.Join(repo.Dir, "portby.toml")+`: services.memcached: Portby has no Service "memcached"`)
}

func TestAServiceWhosePortIsTakenSaysSo(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	ln, err := net.Listen("tcp", net.JoinHostPort(d.ServiceHost(), "6379"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	d.MustRunCLI(serviceProject(t, "svc-port-taken", `redis = "7"`, "").Dir, "link")

	out := d.MustRunCLI(d.Home, "services").Stdout
	assertContains(t, out, "redis:7: Portby can't listen on port 6379")
	assertContains(t, out, "address already in use")
}

func TestAServiceStoppedBehindPortbysBackStartsOnTheNextConnection(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(serviceProject(t, "svc-killed", `redis = "7"`, "").Dir, "link")
	c := dialRedis(t, d, 6379)
	c.do("AUTH", "portby")
	c.conn.Close()

	d.Engine.Kill("portby-redis-7")

	c = dialRedis(t, d, 6379)
	c.do("AUTH", "portby")
	if got := c.do("PING"); got != "+PONG" {
		t.Fatalf("PING after the container was stopped = %q", got)
	}
	if n := countCalls(d.Engine.Calls(), "start"); n != 2 {
		t.Fatalf("started %d times, want twice: %v", n, d.Engine.Calls())
	}
}

func TestDeclaringAServicePullsItsImageAheadOfTheFirstConnection(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(serviceProject(t, "svc-pull", `redis = "7"`, "").Dir, "link")

	eventually(t, "the image to be pulled", func() bool {
		return slices.Contains(d.Engine.Calls(), "pull docker.io/library/redis:7")
	})
	if containsCall(d.Engine.Calls(), "start") {
		t.Fatalf("a container was started without a connection: %v", d.Engine.Calls())
	}
}

func TestTheServiceIdleTimeoutComesFromTheGlobalConfig(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	global := filepath.Join(d.Home, ".config", "portby", "config.toml")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte("service_idle_timeout = \"1h\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d.MustRunCLI(serviceProject(t, "svc-global-idle", `redis = "7"`, "").Dir, "link")
	c := dialRedis(t, d, 6379)
	c.do("AUTH", "portby")
	c.conn.Close()

	if delay := waitForTimer(t, clk); delay != time.Hour {
		t.Fatalf("Idle scheduled after %s, want the global service_idle_timeout of 1h", delay)
	}
}
