package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// Hooks through Seam 1, with the hooked test Plugin
// (internal/testkit/testplugins/hooked): its Recipe detects Projects with a
// hooked file, and each of its hooks returns what the Worktree's hooks.json
// says for the event.

// hookedRepo is a Project the hooked test Plugin's Recipe detects, whose
// hooks do what hooks (the hooks.json) says.
func hookedRepo(t *testing.T, name, hooks string) *gitfixture.Repo {
	t.Helper()
	return gitfixture.New(t, name).
		WriteFile(".gitignore", ".env\n").
		WriteFile("hooked", "").
		WriteFile("hooks.json", hooks).
		Commit("a Project using the hooked Plugin")
}

// hookedDaemon is a daemon with the hooked test Plugin added.
func hookedDaemon(t *testing.T) *testkit.Daemon {
	t.Helper()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "hooked"))
	return d
}

func TestAddingAPluginShowsWhichEventsItHooks(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)

	res := d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "hooked"))

	assertContains(t, res.Stdout, "hooks Preparing, Ready and Worktree removed")
}

func TestAPluginHookingAnEventLanewayDoesNotHaveIsRefused(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := withManifest(t, testkit.Plugin(t, "hooked"), `{
  "name": "hooked",
  "version": "0.1.0",
  "api_version": 1,
  "hooks": ["ready", "deployed"]
}`)

	res := d.RunCLI(d.Home, "plugin", "add", "--yes", plugin)

	if res.ExitCode == 0 {
		t.Fatalf("adding a Plugin hooking an unknown event exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `hooks[1]: "deployed" isn't an event a Plugin can hook; use preparing, ready, worktree_removed`)
}

func TestAPreparingHookSetsEnvKeysInTheWorktreeBeforeItIsReady(t *testing.T) {
	t.Parallel()
	d := hookedDaemon(t)
	repo := hookedRepo(t, "hook-env", `{"preparing": {"effects": [
  {"kind": "env", "env": {"HOOKED_WORKTREE": "{worktree}", "HOOKED_URL": "{url}"}}
]}}`)
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	d.MustRunCLI(wt.Dir, "worktree", "ready")

	env := readFile(t, filepath.Join(wt.Dir, ".env"))
	assertContains(t, env, "HOOKED_WORKTREE=feature\n")
	assertContains(t, env, "HOOKED_URL=https://feature.hook-env.localhost\n")
}

func TestAFailingPreparingHookFailsSetupWithItsOutputAndCanBeRetried(t *testing.T) {
	t.Parallel()
	d := hookedDaemon(t)
	// Fails until the marker exists, like a hook whose command needs a fix.
	marker := filepath.Join(gitfixture.TempDir(t), "fixed")
	failing := "echo checking-deps; test -e " + marker + " || { echo missing-dep >&2; exit 3; }"
	repo := hookedRepo(t, "hook-fail", hooksJSON(t, map[string]any{"preparing": map[string]any{"effects": []any{
		map[string]any{"kind": "exec", "command": failing},
		map[string]any{"kind": "env", "env": map[string]string{"HOOK_DONE": "yes"}},
	}}}))
	d.MustRunCLI(repo.Dir, "link", "--yes")

	res := d.RunCLI(repo.Dir, "worktree", "ready")

	if res.ExitCode != 1 {
		t.Fatalf("worktree ready exited %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, `failed at step "Preparing hook of Plugin hooked"`)
	assertContains(t, res.Stderr, "exit status 3")
	assertContains(t, res.Stderr, "checking-deps")
	assertContains(t, res.Stderr, "missing-dep")
	assertSetupState(t, d, repo.Dir, "failed")
	if env, _ := os.ReadFile(filepath.Join(repo.Dir, ".env")); strings.Contains(string(env), "HOOK_DONE") {
		t.Errorf("an effect after the failed command was applied:\n%s", env)
	}

	writeTestFile(t, marker, "")
	d.MustRunCLI(repo.Dir, "worktree", "ready", "--retry")

	assertSetupState(t, d, repo.Dir, "ready")
	assertContains(t, readFile(t, filepath.Join(repo.Dir, ".env")), "HOOK_DONE=yes\n")
}

func TestAPreparingHookThatReturnsAnErrorFailsSetup(t *testing.T) {
	t.Parallel()
	d := hookedDaemon(t)
	repo := hookedRepo(t, "hook-error", `{"preparing": {"error": "no database for {project}"}}`)
	d.MustRunCLI(repo.Dir, "link", "--yes")

	res := d.RunCLI(repo.Dir, "worktree", "ready")

	if res.ExitCode != 1 {
		t.Fatalf("worktree ready exited %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, `failed at step "Preparing hook of Plugin hooked"`)
	assertContains(t, res.Stderr, "no database for {project}")
}

func TestAReadyHookRunsItsCommandsInTheWorktreeOnceItIsReady(t *testing.T) {
	t.Parallel()
	d := hookedDaemon(t)
	ran := filepath.Join(gitfixture.TempDir(t), "ran")
	repo := hookedRepo(t, "hook-ready", hooksJSON(t, map[string]any{"ready": map[string]any{"effects": []any{
		map[string]any{"kind": "exec", "command": "echo {event} {worktree} $(pwd) $LANEWAY_DOMAIN >> " + ran},
	}}}))
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	d.MustRunCLI(wt.Dir, "worktree", "ready")

	// The main Worktree's Ready hook writes its own line.
	waitForLine(t, ran, "ready feature "+wt.Dir+" feature.hook-ready.localhost")
}

func TestAFailingReadyHookIsAWarningAndTheWorktreeStaysReady(t *testing.T) {
	t.Parallel()
	d := hookedDaemon(t)
	repo := hookedRepo(t, "hook-ready-fail", `{"ready": {"effects": [
  {"kind": "exec", "command": "echo cannot-notify; exit 4"},
  {"kind": "warning", "message": "{worktree_path} looks odd"}
]}}`)
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	var res testkit.CLIResult
	testkit.WaitUntil(t, 10*time.Second, "the Ready hook's failure to show as a warning", func() bool {
		res = d.MustRunCLI(repo.Dir, "worktree", "ready")
		return strings.Contains(res.Stderr, "exit status 4")
	})

	assertContains(t, res.Stderr, "warning: Plugin hooked's Ready hook: the command \"echo cannot-notify; exit 4\" failed: exit status 4")
	assertContains(t, res.Stderr, "cannot-notify")
	assertContains(t, res.Stdout, "is Ready")
	assertSetupState(t, d, repo.Dir, "ready")
	if strings.Contains(res.Stderr, "looks odd") {
		t.Errorf("an effect after the failed command was applied:\n%s", res.Stderr)
	}
}

func TestAWorktreeRemovedHookRunsItsCommandsInTheMainWorktree(t *testing.T) {
	t.Parallel()
	d := hookedDaemon(t)
	ran := filepath.Join(gitfixture.TempDir(t), "ran")
	repo := hookedRepo(t, "hook-removed", hooksJSON(t, map[string]any{"worktree_removed": map[string]any{"effects": []any{
		map[string]any{"kind": "exec", "command": "echo {event} {worktree} $(pwd) $LANEWAY_WORKTREE_PATH >> " + ran},
	}}}))
	wt := repo.AddWorktree("doomed")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	waitForWorktree(t, d, "hook-removed", "doomed")

	repo.Git("worktree", "remove", wt.Dir)

	waitForFile(t, ran, "worktree_removed doomed "+repo.Dir+" "+wt.Dir+"\n")
}

func TestAnEffectTheManifestDoesNotDeclareIsRefusedWithAWarning(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := withManifest(t, testkit.Plugin(t, "hooked"), `{
  "name": "hooked",
  "version": "0.1.0",
  "api_version": 1,
  "recipes": [{"name": "hooked", "detect": {"file": "hooked"}}],
  "hooks": ["preparing"],
  "capabilities": {"effects": ["env"]}
}`)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", plugin)
	ran := filepath.Join(gitfixture.TempDir(t), "ran")
	repo := hookedRepo(t, "hook-undeclared", hooksJSON(t, map[string]any{"preparing": map[string]any{"effects": []any{
		map[string]any{"kind": "exec", "command": "touch " + ran},
		map[string]any{"kind": "env", "env": map[string]string{"HOOKED": "yes"}},
	}}}))
	d.MustRunCLI(repo.Dir, "link", "--yes")

	res := d.MustRunCLI(repo.Dir, "worktree", "ready")

	assertContains(t, res.Stderr, "warning: Plugin hooked's Preparing hook returned an effect of kind exec, "+
		"but the Plugin's manifest doesn't declare \"exec\" under capabilities.effects, so Laneway refused it")
	assertNoFile(t, ran)
	assertContains(t, readFile(t, filepath.Join(repo.Dir, ".env")), "HOOKED=yes\n")
}

func TestAnEnvEffectLeavesKeysTheEnvPatchSetsToIt(t *testing.T) {
	t.Parallel()
	d := hookedDaemon(t)
	repo := gitfixture.New(t, "hook-owned").
		WriteFile(".gitignore", ".env\n").
		WriteFile("hooked", "").
		WriteFile("laneway.toml", "[env]\nAPP_URL = \"{url}\"\n").
		WriteFile("hooks.json", `{"preparing": {"effects": [{"kind": "env", "env": {"APP_URL": "https://elsewhere.test", "OTHER": "set"}}]}}`).
		Commit("a Project whose Env Patch sets APP_URL")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	res := d.MustRunCLI(repo.Dir, "worktree", "ready")

	env := readFile(t, filepath.Join(repo.Dir, ".env"))
	assertContains(t, env, "APP_URL=https://hook-owned.localhost\n")
	assertContains(t, env, "OTHER=set\n")
	assertContains(t, res.Stderr, "warning: Plugin hooked's Preparing hook set APP_URL, which the Env Patch sets")
}

func TestAPluginsHooksFireForProjectsItsIntegrationDetects(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := withManifest(t, testkit.Plugin(t, "hooked"), `{
  "name": "hooked",
  "version": "0.1.0",
  "api_version": 1,
  "integrations": [{"name": "hooked-tool", "detect": {"file": "hooked-tool"}}],
  "hooks": ["preparing"],
  "capabilities": {"effects": ["env"]}
}`)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", plugin)
	repo := gitfixture.New(t, "hook-integration").
		WriteFile(".gitignore", ".env\n").
		WriteFile("hooked-tool", "").
		WriteFile("hooks.json", `{"preparing": {"effects": [{"kind": "env", "env": {"HOOKED": "{project}"}}]}}`).
		Commit("a Project using the hooked-tool Integration")
	d.MustRunCLI(repo.Dir, "link", "--yes")

	d.MustRunCLI(repo.Dir, "worktree", "ready")

	assertContains(t, readFile(t, filepath.Join(repo.Dir, ".env")), "HOOKED=hook-integration\n")
}

func TestAPluginsHooksDoNotFireForProjectsThatDoNotUseIt(t *testing.T) {
	t.Parallel()
	d := hookedDaemon(t)
	ran := filepath.Join(gitfixture.TempDir(t), "ran")
	hooks := hooksJSON(t, map[string]any{
		"preparing": map[string]any{"effects": []any{map[string]any{"kind": "env", "env": map[string]string{"HOOKED": "{project}"}}}},
		"ready":     map[string]any{"effects": []any{map[string]any{"kind": "exec", "command": "echo {project} >> " + ran}}},
	})
	// Both have the same hooks.json, but only one has the file the
	// Plugin's Recipe detects.
	other := gitfixture.New(t, "hook-unused").
		WriteFile(".gitignore", ".env\n").
		WriteFile("laneway.toml", "[env]\nAPP_URL = \"{url}\"\n").
		WriteFile("hooks.json", hooks).
		Commit("a Project not using the hooked Plugin")
	user := hookedRepo(t, "hook-used", hooks)
	d.MustRunCLI(other.Dir, "link", "--yes")
	d.MustRunCLI(other.Dir, "worktree", "ready")
	d.MustRunCLI(user.Dir, "link", "--yes")
	d.MustRunCLI(user.Dir, "worktree", "ready")

	waitForFile(t, ran, "hook-used\n")
	if env := readFile(t, filepath.Join(other.Dir, ".env")); strings.Contains(env, "HOOKED") {
		t.Errorf("the Preparing hook fired for a Project not using the Plugin:\n%s", env)
	}
	if got := readFile(t, ran); got != "hook-used\n" {
		t.Errorf("the Ready hook ran for %q, want only the Project using the Plugin", got)
	}
}

// hooksJSON is v as the hooked Plugin's hooks.json.
func hooksJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// waitForLine waits until the file at path has the line want.
func waitForLine(t *testing.T, path, want string) {
	t.Helper()
	testkit.WaitUntil(t, 10*time.Second, fmt.Sprintf("%s to have the line %q", path, want), func() bool {
		data, err := os.ReadFile(path)
		return err == nil && slices.Contains(strings.Split(string(data), "\n"), want)
	})
}
