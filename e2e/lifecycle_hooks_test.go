package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// The lifecycle events other than Preparing, Ready and Worktree removed,
// through Seam 1 with the hooked test Plugin (see hooks_test.go). Their
// hooks all run in the background, and their failures are warnings.

// hookingDaemon is a daemon with the hooked test Plugin added, its manifest
// hooking only events.
func hookingDaemon(t *testing.T, events ...string) *testkit.Daemon {
	t.Helper()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", hookingPlugin(t, events...))
	return d
}

// hookingPlugin is the hooked test Plugin, its manifest hooking only events.
func hookingPlugin(t *testing.T, events ...string) string {
	t.Helper()
	return withManifest(t, testkit.Plugin(t, "hooked"), fmt.Sprintf(`{
  "name": "hooked",
  "version": "0.1.0",
  "api_version": 1,
  "recipes": [{"name": "hooked", "detect": {"file": "hooked"}}],
  "hooks": [%s],
  "capabilities": {"effects": ["env", "exec", "warning"]}
}`, `"`+strings.Join(events, `", "`)+`"`))
}

// appendLine is a hooked Plugin effect appending line (after substitution)
// to the file at path.
func appendLine(path, line string) map[string]any {
	return map[string]any{"kind": "exec", "command": []string{"sh", "-c", "echo " + line + " >> " + path}}
}

// hookEffects is the hooked Plugin's hooks.json running effects for each
// of events.
func hookEffects(t *testing.T, events []string, effects ...map[string]any) string {
	t.Helper()
	scripts := map[string]any{}
	for _, e := range events {
		scripts[e] = map[string]any{"effects": effects}
	}
	return hooksJSON(t, scripts)
}

// hookedProject is a Project the hooked test Plugin's Recipe detects, with
// laneway.toml config, whose hooks do what hooks (the hooks.json) says.
func hookedProject(t *testing.T, name, config, hooks string) *gitfixture.Repo {
	t.Helper()
	return gitfixture.New(t, name).
		WriteFile(".gitignore", ".env\n").
		WriteFile("hooked", "").
		WriteFile("laneway.toml", config).
		WriteFile("hooks.json", hooks).
		Commit("a Project using the hooked Plugin")
}

func TestALinkHookRunsItsCommandsInTheMainWorktreeOnceTheProjectIsLinked(t *testing.T) {
	t.Parallel()
	d := hookingDaemon(t, "link")
	ran := filepath.Join(gitfixture.TempDir(t), "ran")
	repo := hookedRepo(t, "hook-link", hookEffects(t, []string{"link"}, appendLine(ran, "{event} {project} {project_path} $(pwd)")))

	d.MustRunCLI(repo.Dir, "link", "--yes")

	waitForFile(t, ran, fmt.Sprintf("link hook-link %s %s\n", repo.Dir, repo.Dir))
}

func TestAnUnlinkHookRunsItsCommandsInTheMainWorktreeOnceTheProjectIsUnlinked(t *testing.T) {
	t.Parallel()
	d := hookingDaemon(t, "unlink")
	ran := filepath.Join(gitfixture.TempDir(t), "ran")
	repo := hookedRepo(t, "hook-unlink", hooksJSON(t, map[string]any{"unlink": map[string]any{"effects": []any{
		appendLine(ran, "{event} {project} $(pwd)"),
		map[string]any{"kind": "env", "env": map[string]string{"UNLINKED": "yes"}},
	}}}))
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	assertNoFile(t, ran)

	d.MustRunCLI(repo.Dir, "unlink")

	waitForFile(t, ran, "unlink hook-unlink "+repo.Dir+"\n")
	// The Project's .env isn't Laneway's to change any more.
	if env := readFileOr(filepath.Join(repo.Dir, ".env"), ""); strings.Contains(env, "UNLINKED") {
		t.Errorf("an Unlink hook's env effect changed the Unlinked Project's .env:\n%s", env)
	}
}

func TestAPrimaryDomainChangedHookIsToldTheNewAndPreviousDomain(t *testing.T) {
	t.Parallel()
	d := hookingDaemon(t, "primary_domain_changed")
	ran := filepath.Join(gitfixture.TempDir(t), "ran")
	repo := hookedRepo(t, "hook-moved", hookEffects(t, []string{"primary_domain_changed"},
		appendLine(ran, "{event} {worktree} {previous_domain} {domain} {url}")))
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	waitForWorktree(t, d, "hook-moved", "feature")

	d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")

	waitForFile(t, ran, "primary_domain_changed feature feature.hook-moved.localhost "+
		"checkout.hook-moved.localhost https://checkout.hook-moved.localhost\n")

	d.MustRunCLI(wt.Dir, "worktree", "alias", "--remove", "checkout")

	waitForLine(t, ran, "primary_domain_changed feature checkout.hook-moved.localhost "+
		"feature.hook-moved.localhost https://feature.hook-moved.localhost")
}

func TestProcessHooksAreToldWhenAProcessStartsAndIsStopped(t *testing.T) {
	t.Parallel()
	d := hookingDaemon(t, "process_started", "process_stopped")
	ran := filepath.Join(gitfixture.TempDir(t), "ran")
	server := testkit.TestServer(t)
	repo := hookedProject(t, "hook-procs", processConfig(server+" --port {port}"),
		hookEffects(t, []string{"process_started", "process_stopped"},
			appendLine(ran, "{event} {worktree_path} {process} {process_port} {process_command}")))
	d.MustRunCLI(repo.Dir, "link", "--yes")

	d.GetServer("https://hook-procs.localhost/")

	var port string
	testkit.WaitUntil(t, 10*time.Second, "the Process started hook to run", func() bool {
		fields := strings.Fields(readFileOr(ran, ""))
		if len(fields) < 4 || fields[0] != "process_started" {
			return false
		}
		port = fields[3]
		return true
	})
	waitForLine(t, ran, fmt.Sprintf("process_started %s web %s %s --port %s", repo.Dir, port, server, port))

	d.MustRunCLI(repo.Dir, "down")

	waitForLine(t, ran, fmt.Sprintf("process_stopped %s web %s %s --port %s", repo.Dir, port, server, port))
}

func TestAProcessCrashedHookIsToldHowItExitedAndWhenItWontBeRestarted(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", hookingPlugin(t, "process_crashed"))
	ran := filepath.Join(gitfixture.TempDir(t), "ran")
	repo := hookedProject(t, "hook-crash", processConfig(testkit.TestServer(t))+
		"[processes.worker]\ncommand = \"exit 3\"\nstart_mode = \"always\"\n",
		hookEffects(t, []string{"process_crashed"}, appendLine(ran, "{event} {process} {exit} {failed}")))
	d.MustRunCLI(repo.Dir, "link", "--yes")

	waitForLine(t, ran, "process_crashed worker exit status 3 false")

	// It fails for good on its fifth crash.
	for i := 1; i < 5; i++ {
		clk.Advance(waitForRestart(t, clk))
	}
	waitForLine(t, ran, "process_crashed worker exit status 3 true")
}

func TestIdleAndWokenHooksFireWhenAWorktreeGoesIdleAndARequestStartsItAgain(t *testing.T) {
	t.Parallel()
	d, clk := idleDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", hookingPlugin(t, "idle", "woken"))
	ran := filepath.Join(gitfixture.TempDir(t), "ran")
	repo := hookedProject(t, "hook-idle", "idle_timeout = \"10m\"\n"+processConfig(testkit.TestServer(t)),
		hookEffects(t, []string{"idle", "woken"}, appendLine(ran, "{event} {worktree_path} {domain}")))
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.GetServer("https://hook-idle.localhost/")
	waitForState(t, d, "hook-idle.localhost", "running")
	waitForTimer(t, clk)

	clk.Advance(10 * time.Minute)

	waitForFile(t, ran, "idle "+repo.Dir+" hook-idle.localhost\n")

	d.GetServer("https://hook-idle.localhost/")

	waitForFile(t, ran, "idle "+repo.Dir+" hook-idle.localhost\nwoken "+repo.Dir+" hook-idle.localhost\n")
}

func TestServiceHooksFireInTheProjectsThatUseTheService(t *testing.T) {
	t.Parallel()
	d := hookingDaemon(t, "service_started", "service_stopped")
	ran := filepath.Join(gitfixture.TempDir(t), "ran")
	hooks := hookEffects(t, []string{"service_started", "service_stopped"},
		appendLine(ran, "{event} {project} $(pwd) {service} {service_host}:{service_port}"))
	user := hookedProject(t, "hook-svc", "[services]\nredis = \"7\"\n", hooks)
	other := hookedRepo(t, "hook-no-svc", hooks)
	d.MustRunCLI(other.Dir, "link", "--yes")
	d.MustRunCLI(user.Dir, "link", "--yes")

	d.MustRunCLI(d.Home, "services", "up", "redis")

	started := fmt.Sprintf("service_started hook-svc %s redis:7 %s:6379\n", user.Dir, d.ServiceHost())
	waitForFile(t, ran, started)

	d.MustRunCLI(d.Home, "services", "down", "redis:7")

	waitForFile(t, ran, started+fmt.Sprintf("service_stopped hook-svc %s redis:7 %s:6379\n", user.Dir, d.ServiceHost()))
}

func TestAFailingHookIsAWarningAndBlocksNothing(t *testing.T) {
	t.Parallel()
	d := hookingDaemon(t, "link", "process_started")
	repo := hookedProject(t, "hook-warn", processConfig(testkit.TestServer(t)), `{
  "link": {"error": "cannot reach {project}'s tracker"},
  "process_started": {"effects": [
    {"kind": "exec", "command": ["sh", "-c", "echo no-tracker; exit 5"]},
    {"kind": "warning", "message": "{process} started"}
  ]}
}`)

	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.GetServer("https://hook-warn.localhost/")

	var res testkit.CLIResult
	testkit.WaitUntil(t, 10*time.Second, "the Process started hook's failure to show as a warning", func() bool {
		res = d.MustRunCLI(repo.Dir, "worktree", "ready")
		return strings.Contains(res.Stderr, "exited with status 5")
	})
	assertContains(t, res.Stderr, "warning: Plugin hooked's Process started hook: `sh -c echo no-tracker; exit 5` exited with status 5")
	assertContains(t, res.Stderr, "no-tracker")
	assertSetupState(t, d, repo.Dir, "ready")
	waitForState(t, d, "hook-warn.localhost", "running")
}

func TestAHookThatHangsHoldsUpNothing(t *testing.T) {
	t.Parallel()
	d := hookingDaemon(t, "link", "process_started")
	hang := map[string]any{"effects": []any{map[string]any{"kind": "exec", "command": []string{"sleep", "60"}}}}
	repo := hookedProject(t, "hook-hang", processConfig(testkit.TestServer(t)),
		hooksJSON(t, map[string]any{"link": hang, "process_started": hang}))

	start := time.Now()
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.GetServer("https://hook-hang.localhost/")

	if took := time.Since(start); took > 30*time.Second {
		t.Fatalf("linking and the first request took %s with hooks that hang", took)
	}
}

// readFileOr is the file at path, or def if it can't be read.
func readFileOr(path, def string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return def
	}
	return string(data)
}
