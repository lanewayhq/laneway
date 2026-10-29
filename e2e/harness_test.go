package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// portbyBin is a built `portby` for running generated Harness commands, built
// once for the whole run.
var portbyBin = sync.OnceValues(func() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "portby-test-bin-")
	if err != nil {
		return "", err
	}
	builtBinDir.Store(dir)
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "portby"), "./cmd/portby")
	cmd.Dir = filepath.Dir(strings.TrimSpace(string(out)))
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", errors.New(string(out))
	}
	return dir, nil
})

// hookResult is what a Harness saw from running a generated command.
type hookResult struct {
	Stdout, Stderr string
	ExitCode       int
}

// runHook runs a generated Harness command the way Claude Code and T3 Code
// do: through a shell in the Worktree, with the daemon's environment. With
// withPortby false, `portby` isn't on PATH (a teammate without Portby).
func runHook(t *testing.T, d *testkit.Daemon, dir, command string, withPortby bool) hookResult {
	t.Helper()
	path := d.Path
	if withPortby {
		bin, err := portbyBin()
		if err != nil {
			t.Fatalf("building portby: %v", err)
		}
		path = bin + string(os.PathListSeparator) + path
	}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = dir
	for _, kv := range d.Environ() {
		if !strings.HasPrefix(kv, "PATH=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("running %q: %v", command, err)
	}
	return hookResult{stdout.String(), stderr.String(), cmd.ProcessState.ExitCode()}
}

// readJSON decodes the JSON file at path.
func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(readFile(t, path)), &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

// sessionStartCommands returns the commands of every SessionStart hook in a
// Claude Code settings file.
func sessionStartCommands(t *testing.T, path string) []string {
	t.Helper()
	settings := readJSON(t, path)
	hooks, _ := settings["hooks"].(map[string]any)
	groups, _ := hooks["SessionStart"].([]any)
	var commands []string
	for _, g := range groups {
		group, _ := g.(map[string]any)
		handlers, _ := group["hooks"].([]any)
		for _, h := range handlers {
			handler, _ := h.(map[string]any)
			if handler["type"] == "command" {
				commands = append(commands, handler["command"].(string))
			}
		}
	}
	return commands
}

// portbyCommand returns the one command that mentions portby.
func portbyCommand(t *testing.T, commands []string) string {
	t.Helper()
	var found []string
	for _, c := range commands {
		if strings.Contains(c, "portby") {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want one portby command, got %q", commands)
	}
	return found[0]
}

func TestHarnessAddClaudeAddsASessionStartHookThatWaitsUntilTheWorktreeIsReady(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "harness-claude").
		WriteFile("portby.toml", "[worktree]\non_create = [\"true\"]\n").
		Commit("config")
	d.MustRunCLI(repo.Dir, "link")

	d.MustRunCLI(repo.Dir, "harness", "add", "claude")

	command := portbyCommand(t, sessionStartCommands(t, filepath.Join(repo.Dir, ".claude", "settings.json")))
	res := runHook(t, d, repo.Dir, command, true)
	if res.ExitCode != 0 {
		t.Fatalf("hook exited %d:\n%s%s", res.ExitCode, res.Stdout, res.Stderr)
	}
	// Claude Code adds a SessionStart hook's stdout to the agent's context.
	assertContains(t, res.Stdout, "is Ready: https://harness-claude.localhost/")
}

func TestTheGeneratedHookQuietlySucceedsWhenThereIsNothingToWaitFor(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "harness-guard")
	d.MustRunCLI(repo.Dir, "harness", "add", "claude")
	command := portbyCommand(t, sessionStartCommands(t, filepath.Join(repo.Dir, ".claude", "settings.json")))

	for _, tc := range []struct {
		name       string
		withPortby bool
		stopDaemon bool
	}{
		{"portby isn't installed", false, false},
		{"the repository isn't Linked", true, false},
		{"no daemon is running", true, true},
	} {
		if tc.stopDaemon {
			d.Stop()
		}
		res := runHook(t, d, repo.Dir, command, tc.withPortby)
		if res.ExitCode != 0 || res.Stdout != "" {
			t.Errorf("%s: hook exited %d with stdout %q, want 0 and nothing:\n%s", tc.name, res.ExitCode, res.Stdout, res.Stderr)
		}
	}
}

func TestTheGeneratedHookFailsWhenSetupFailed(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "harness-failed").
		WriteFile("portby.toml", "[worktree]\non_create = [\"echo broken-step >&2; exit 5\"]\n").
		Commit("config")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "harness", "add", "claude")
	command := portbyCommand(t, sessionStartCommands(t, filepath.Join(repo.Dir, ".claude", "settings.json")))

	res := runHook(t, d, repo.Dir, command, true)

	if res.ExitCode == 0 {
		t.Fatalf("hook succeeded although setup failed:\n%s%s", res.Stdout, res.Stderr)
	}
	assertContains(t, res.Stderr, "broken-step")
	assertContains(t, res.Stderr, "--retry")
}

func TestHarnessAddClaudeKeepsExistingSettingsAndChangesNothingTheSecondTime(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	const existing = `{
    "permissions": {
        "allow": ["Bash(npm test:*)"]
    },
    "hooks": {
        "SessionStart": [
            {
                "matcher": "startup",
                "hooks": [{"type": "command", "command": "echo mine", "timeout": 5}]
            }
        ],
        "Stop": [{"hooks": [{"type": "command", "command": "say done"}]}]
    },
    "model": "opus"
}
`
	repo := gitfixture.New(t, "harness-merge").
		WriteFile(".claude/settings.json", existing).
		Commit("claude settings")
	path := filepath.Join(repo.Dir, ".claude", "settings.json")

	res := d.MustRunCLI(repo.Dir, "harness", "add", "claude")

	assertContains(t, res.Stdout, "Updated .claude/settings.json")
	merged := readFile(t, path)
	settings := readJSON(t, path)
	if got := settings["permissions"].(map[string]any)["allow"].([]any)[0]; got != "Bash(npm test:*)" {
		t.Errorf("permissions.allow lost: %v", got)
	}
	if settings["model"] != "opus" {
		t.Errorf("model lost: %v", settings["model"])
	}
	stop := settings["hooks"].(map[string]any)["Stop"].([]any)
	if len(stop) != 1 {
		t.Errorf("Stop hooks changed: %v", stop)
	}
	commands := sessionStartCommands(t, path)
	if len(commands) != 2 || commands[0] != "echo mine" {
		t.Errorf("SessionStart commands = %q, want the existing one first, then Portby's", commands)
	}
	portbyCommand(t, commands)
	// Keys stay in their order and the file keeps its indentation.
	if !(strings.Index(merged, `"permissions"`) < strings.Index(merged, `"hooks"`) &&
		strings.Index(merged, `"hooks"`) < strings.Index(merged, `"model"`)) {
		t.Errorf("keys reordered:\n%s", merged)
	}
	assertContains(t, merged, "\n    \"permissions\": {\n        \"allow\"")

	again := d.MustRunCLI(repo.Dir, "harness", "add", "claude")

	assertContains(t, again.Stdout, ".claude/settings.json is up to date")
	assertFile(t, path, merged)
}

func TestHarnessAddClaudeRepairsItsOwnHookInPlace(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "harness-repair")
	path := filepath.Join(repo.Dir, ".claude", "settings.json")
	d.MustRunCLI(repo.Dir, "harness", "add", "claude")
	generated := readFile(t, path)
	writeTestFile(t, path, strings.Replace(generated, "portby worktree ready", "portby worktree ready --timeout 1s", 1))

	res := d.MustRunCLI(repo.Dir, "harness", "add", "claude")

	assertContains(t, res.Stdout, "Updated .claude/settings.json")
	assertFile(t, path, generated)
}

func TestHarnessAddRefusesAFileItCannotMerge(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	const broken = "{\n  \"hooks\": [\n"
	repo := gitfixture.New(t, "harness-broken").
		WriteFile(".claude/settings.json", broken).
		Commit("broken settings")

	res := d.RunCLI(repo.Dir, "harness", "add", "claude")

	if res.ExitCode == 0 {
		t.Fatalf("harness add succeeded on a broken file:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, ".claude/settings.json")
	assertFile(t, filepath.Join(repo.Dir, ".claude", "settings.json"), broken)
}

// t3Scripts returns the scripts in a t3.json.
func t3Scripts(t *testing.T, path string) []map[string]any {
	t.Helper()
	list, _ := readJSON(t, path)["scripts"].([]any)
	var scripts []map[string]any
	for _, s := range list {
		scripts = append(scripts, s.(map[string]any))
	}
	return scripts
}

func TestHarnessAddT3AddsAWorktreeCreateScriptThatHoldsTheAgentUntilReady(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "harness-t3").
		WriteFile("portby.toml", "[worktree]\non_create = [\"true\"]\n").
		Commit("config")
	d.MustRunCLI(repo.Dir, "link")

	res := d.MustRunCLI(repo.Dir, "harness", "add", "t3")

	assertContains(t, res.Stdout, "Created t3.json")
	if schema := readJSON(t, filepath.Join(repo.Dir, "t3.json"))["$schema"]; schema != "https://t3.codes/schema/t3.json" {
		t.Errorf("$schema = %v, want T3 Code's schema", schema)
	}
	scripts := t3Scripts(t, filepath.Join(repo.Dir, "t3.json"))
	if len(scripts) != 1 {
		t.Fatalf("want one script, got %v", scripts)
	}
	script := scripts[0]
	if script["runOnWorktreeCreate"] != true || script["async"] != false {
		t.Errorf("script = %v, want runOnWorktreeCreate: true and async: false", script)
	}
	// T3 Code runs it in the new Worktree.
	run := runHook(t, d, repo.Dir, script["command"].(string), true)
	if run.ExitCode != 0 {
		t.Fatalf("script exited %d:\n%s%s", run.ExitCode, run.Stdout, run.Stderr)
	}
	assertContains(t, run.Stdout, "is Ready: https://harness-t3.localhost/")
	// Without Portby it does nothing.
	if run := runHook(t, d, repo.Dir, script["command"].(string), false); run.ExitCode != 0 || run.Stdout != "" {
		t.Errorf("without portby, script exited %d with %q", run.ExitCode, run.Stdout)
	}
}

func TestHarnessAddT3KeepsExistingScriptsAndChangesNothingTheSecondTime(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	const existing = `{
  "$schema": "https://t3.codes/schema/t3.json",
  "defaultThreadEnvMode": "worktree",
  "scripts": [
    {"name": "Tests", "command": "npm test", "icon": "test"}
  ]
}
`
	repo := gitfixture.New(t, "harness-t3-merge").
		WriteFile("t3.json", existing).
		Commit("t3 config")
	path := filepath.Join(repo.Dir, "t3.json")

	res := d.MustRunCLI(repo.Dir, "harness", "add", "t3")

	assertContains(t, res.Stdout, "Updated t3.json")
	if res.Stderr != "" {
		t.Errorf("unexpected stderr: %s", res.Stderr)
	}
	doc := readJSON(t, path)
	if doc["defaultThreadEnvMode"] != "worktree" || doc["$schema"] != "https://t3.codes/schema/t3.json" {
		t.Errorf("top-level keys lost: %v", doc)
	}
	scripts := t3Scripts(t, path)
	if len(scripts) != 2 || scripts[0]["name"] != "Tests" || scripts[0]["command"] != "npm test" {
		t.Fatalf("scripts = %v, want the existing one kept first", scripts)
	}
	assertContains(t, scripts[1]["command"].(string), "portby worktree ready")
	merged := readFile(t, path)

	again := d.MustRunCLI(repo.Dir, "harness", "add", "t3")

	assertContains(t, again.Stdout, "t3.json is up to date")
	assertFile(t, path, merged)
}

func TestHarnessAddT3WarnsWhenAnotherScriptAlreadyRunsOnWorktreeCreate(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "harness-t3-setup").
		WriteFile("t3.json", `{"scripts": [{"name": "Setup Workspace", "command": "./setup", "runOnWorktreeCreate": true}]}`).
		Commit("t3 config")

	res := d.MustRunCLI(repo.Dir, "harness", "add", "t3")

	// T3 Code only runs the first runOnWorktreeCreate script.
	assertContains(t, res.Stderr, "Setup Workspace")
	assertContains(t, res.Stderr, "portby worktree ready")
	scripts := t3Scripts(t, filepath.Join(repo.Dir, "t3.json"))
	if len(scripts) != 2 || scripts[0]["name"] != "Setup Workspace" || scripts[0]["runOnWorktreeCreate"] != true {
		t.Fatalf("scripts = %v, want the existing setup script kept as it was", scripts)
	}
}

// portbyBlock returns the marked Portby block in a Markdown file, markers
// included, failing unless there is exactly one.
func portbyBlock(t *testing.T, doc string) string {
	t.Helper()
	if n := strings.Count(doc, "<!-- portby:begin"); n != 1 {
		t.Fatalf("want one portby:begin marker, got %d in:\n%s", n, doc)
	}
	begin := strings.Index(doc, "<!-- portby:begin")
	end := strings.Index(doc, "<!-- portby:end -->")
	if end < begin {
		t.Fatalf("no portby:end marker after the begin marker in:\n%s", doc)
	}
	return doc[begin : end+len("<!-- portby:end -->")]
}

func TestBothHarnessesAddOneMarkedBlockToAgentsMd(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	const mine = "# Agent notes\n\nRun the linter before committing.\n"
	repo := gitfixture.New(t, "harness-agents").
		WriteFile("AGENTS.md", mine).
		Commit("agent notes")
	path := filepath.Join(repo.Dir, "AGENTS.md")

	res := d.MustRunCLI(repo.Dir, "harness", "add", "t3")

	assertContains(t, res.Stdout, "Updated AGENTS.md")
	doc := readFile(t, path)
	if !strings.HasPrefix(doc, mine) {
		t.Errorf("the developer's part of AGENTS.md changed:\n%s", doc)
	}
	block := portbyBlock(t, doc)
	assertContains(t, block, "portby info --json")
	assertContains(t, block, "portby exec")

	res = d.MustRunCLI(repo.Dir, "harness", "add", "claude")

	assertContains(t, res.Stdout, "AGENTS.md is up to date")
	assertFile(t, path, doc)
}

func TestHarnessAddCreatesAgentsMdAndRestoresAnEditedBlock(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "harness-agents-new")
	path := filepath.Join(repo.Dir, "AGENTS.md")

	res := d.MustRunCLI(repo.Dir, "harness", "add", "claude")

	assertContains(t, res.Stdout, "Created AGENTS.md")
	generated := readFile(t, path)
	block := portbyBlock(t, generated)
	edited := "# Notes\n\n" + strings.Replace(generated, "portby exec", "something else", 1) + "\nMore of mine.\n"
	writeTestFile(t, path, edited)

	d.MustRunCLI(repo.Dir, "harness", "add", "claude")

	assertFile(t, path, "# Notes\n\n"+generated+"\nMore of mine.\n")
	if got := portbyBlock(t, readFile(t, path)); got != block {
		t.Errorf("block = %q, want %q", got, block)
	}
}

func TestHarnessAddClaudeImportsAgentsMdIntoAnExistingClaudeMd(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	const mine = "# Project\n\nUse tabs.\n"
	repo := gitfixture.New(t, "harness-claude-md").
		WriteFile("CLAUDE.md", mine).
		Commit("claude notes")
	path := filepath.Join(repo.Dir, "CLAUDE.md")

	res := d.MustRunCLI(repo.Dir, "harness", "add", "claude")

	assertContains(t, res.Stdout, "Updated CLAUDE.md")
	doc := readFile(t, path)
	if !strings.HasPrefix(doc, mine) {
		t.Errorf("the developer's part of CLAUDE.md changed:\n%s", doc)
	}
	// Claude Code's import syntax, on a line of its own.
	assertContains(t, portbyBlock(t, doc), "\n@AGENTS.md\n")

	res = d.MustRunCLI(repo.Dir, "harness", "add", "claude")

	assertContains(t, res.Stdout, "CLAUDE.md is up to date")
	assertFile(t, path, doc)
}

func TestHarnessAddLeavesClaudeMdAloneWhenItNeedsNoImport(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)

	t.Run("no CLAUDE.md", func(t *testing.T) {
		repo := gitfixture.New(t, "harness-no-claude-md")
		d.MustRunCLI(repo.Dir, "harness", "add", "claude")
		assertNoFile(t, filepath.Join(repo.Dir, "CLAUDE.md"))
	})
	t.Run("already imports AGENTS.md", func(t *testing.T) {
		const mine = "# Project\n\nSee @AGENTS.md for the rules.\n"
		repo := gitfixture.New(t, "harness-imported").WriteFile("CLAUDE.md", mine).Commit("claude notes")
		d.MustRunCLI(repo.Dir, "harness", "add", "claude")
		assertFile(t, filepath.Join(repo.Dir, "CLAUDE.md"), mine)
	})
	t.Run("t3 doesn't import it", func(t *testing.T) {
		const mine = "# Project\n"
		repo := gitfixture.New(t, "harness-t3-claude-md").WriteFile("CLAUDE.md", mine).Commit("claude notes")
		d.MustRunCLI(repo.Dir, "harness", "add", "t3")
		assertFile(t, filepath.Join(repo.Dir, "CLAUDE.md"), mine)
	})
	t.Run("CLAUDE.md is a link to AGENTS.md", func(t *testing.T) {
		repo := gitfixture.New(t, "harness-linked-claude-md").WriteFile("AGENTS.md", "# Rules\n").Commit("rules")
		if err := os.Symlink("AGENTS.md", filepath.Join(repo.Dir, "CLAUDE.md")); err != nil {
			t.Fatal(err)
		}
		d.MustRunCLI(repo.Dir, "harness", "add", "claude")
		doc := readFile(t, filepath.Join(repo.Dir, "AGENTS.md"))
		portbyBlock(t, doc)
		assertNotContains(t, doc, "@AGENTS.md")
		if target, err := os.Readlink(filepath.Join(repo.Dir, "CLAUDE.md")); err != nil || target != "AGENTS.md" {
			t.Errorf("CLAUDE.md is no longer a link to AGENTS.md: %q, %v", target, err)
		}
	})
}

// boostComposer is the composer.json of a Project that uses Laravel Boost.
const boostComposer = `{"require": {"laravel/framework": "^13.0"}, "require-dev": {"laravel/boost": "^2.0"}}`

func TestWithLaravelBoostBothHarnessesAddACustomGuidelineInsteadOfEditingAgentsMd(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	const claudeMd = "# Project\n\n<laravel-boost-guidelines>\nBoost's rules.\n</laravel-boost-guidelines>\n"
	repo := gitfixture.New(t, "harness-boost").
		WriteFile("composer.json", boostComposer).
		WriteFile("CLAUDE.md", claudeMd).
		Commit("boost")
	guideline := filepath.Join(repo.Dir, ".ai", "guidelines", "portby.md")

	res := d.MustRunCLI(repo.Dir, "harness", "add", "claude")

	// Boost includes every file in .ai/guidelines in the guideline files it
	// writes, but only when boost:install or boost:update runs.
	assertContains(t, res.Stdout, "Created .ai/guidelines/portby.md")
	assertContains(t, res.Stderr, "php artisan boost:update")
	doc := readFile(t, guideline)
	block := portbyBlock(t, doc)
	assertContains(t, block, "portby info --json")
	assertContains(t, block, "portby exec")
	assertNoFile(t, filepath.Join(repo.Dir, "AGENTS.md"))
	assertFile(t, filepath.Join(repo.Dir, "CLAUDE.md"), claudeMd)

	res = d.MustRunCLI(repo.Dir, "harness", "add", "t3")

	assertContains(t, res.Stdout, ".ai/guidelines/portby.md is up to date")
	assertNotContains(t, res.Stderr, "boost:update")
	assertFile(t, guideline, doc)
	assertNoFile(t, filepath.Join(repo.Dir, "AGENTS.md"))
}

func TestWithLaravelBoostHarnessAddTakesOutTheBlocksAnEarlierRunAdded(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	const mine = "# Agent notes\n\nRun the linter before committing.\n"
	repo := gitfixture.New(t, "harness-boost-earlier").
		WriteFile("AGENTS.md", mine).
		WriteFile("CLAUDE.md", mine).
		Commit("agent notes")
	d.MustRunCLI(repo.Dir, "harness", "add", "claude")
	portbyBlock(t, readFile(t, filepath.Join(repo.Dir, "AGENTS.md")))
	portbyBlock(t, readFile(t, filepath.Join(repo.Dir, "CLAUDE.md")))
	writeTestFile(t, filepath.Join(repo.Dir, "composer.json"), boostComposer)

	res := d.MustRunCLI(repo.Dir, "harness", "add", "claude")

	assertContains(t, res.Stdout, "Updated AGENTS.md")
	assertContains(t, res.Stdout, "Updated CLAUDE.md")
	assertFile(t, filepath.Join(repo.Dir, "AGENTS.md"), mine)
	assertFile(t, filepath.Join(repo.Dir, "CLAUDE.md"), mine)
	portbyBlock(t, readFile(t, filepath.Join(repo.Dir, ".ai", "guidelines", "portby.md")))

	res = d.MustRunCLI(repo.Dir, "harness", "add", "claude")

	assertNotContains(t, res.Stdout, "AGENTS.md")
	assertNotContains(t, res.Stdout, "CLAUDE.md")
}

func TestHarnessAddNeedsAKnownHarnessAndAGitRepository(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "harness-usage")

	for _, args := range [][]string{
		{"harness"},
		{"harness", "add"},
		{"harness", "add", "cursor"},
		{"harness", "add", "claude", "t3"},
		{"harness", "remove", "claude"},
	} {
		res := d.RunCLI(repo.Dir, args...)
		if res.ExitCode != 2 {
			t.Errorf("portby %s exited %d, want 2:\n%s", strings.Join(args, " "), res.ExitCode, res.Output())
		}
		assertContains(t, res.Stderr, "claude")
	}
	if out := repo.Git("status", "--porcelain"); out != "" {
		t.Errorf("usage errors changed files:\n%s", out)
	}

	res := d.RunCLI(gitfixture.NotARepo(t, "harness-not-a-repo"), "harness", "add", "claude")
	if res.ExitCode != 1 {
		t.Errorf("outside a git repository: exited %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, "not inside a git repository")
}

func TestHarnessAddKeepsWindowsLineEndingsInMarkdown(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "harness-crlf").
		WriteFile("AGENTS.md", "# Rules\r\nNo tabs.").
		Commit("rules")
	path := filepath.Join(repo.Dir, "AGENTS.md")

	d.MustRunCLI(repo.Dir, "harness", "add", "claude")

	doc := readFile(t, path)
	if !strings.HasPrefix(doc, "# Rules\r\nNo tabs.\r\n\r\n<!-- portby:begin") || strings.Count(doc, "\n") != strings.Count(doc, "\r\n") {
		t.Errorf("line endings not kept:\n%q", doc)
	}
	d.MustRunCLI(repo.Dir, "harness", "add", "claude")
	assertFile(t, path, doc)
}

// assertTellsAgentsToNameTheirWorktree checks that the agent instructions in
// block tell an agent to name a linked worktree after its task.
func assertTellsAgentsToNameTheirWorktree(t *testing.T, block string) {
	t.Helper()
	for _, want := range []string{
		"portby worktree alias <name>", // the command
		"`aliases`",                    // only while it has no Alias
		"portby worktree ready",        // once it is Ready
		"kebab-case",                   // a valid name the first time
		"checkout-redesign",
		"taken",         // another name if it's taken
		"`url`",         // the URL to use from then on
		"main worktree", // never in the main Worktree
	} {
		assertContains(t, block, want)
	}
}

func TestTheAgentInstructionsTellAgentsToNameALinkedWorktreeAfterTheirTask(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)

	for _, harness := range []string{"claude", "t3"} {
		t.Run(harness, func(t *testing.T) {
			repo := gitfixture.New(t, "harness-naming-"+harness)
			d.MustRunCLI(repo.Dir, "harness", "add", harness)
			assertTellsAgentsToNameTheirWorktree(t, portbyBlock(t, readFile(t, filepath.Join(repo.Dir, "AGENTS.md"))))
		})
	}
	t.Run("Laravel Boost", func(t *testing.T) {
		repo := gitfixture.New(t, "harness-naming-boost").
			WriteFile("composer.json", boostComposer).
			Commit("boost")
		d.MustRunCLI(repo.Dir, "harness", "add", "claude")
		assertTellsAgentsToNameTheirWorktree(t, portbyBlock(t, readFile(t, filepath.Join(repo.Dir, ".ai", "guidelines", "portby.md"))))
		assertNoFile(t, filepath.Join(repo.Dir, "AGENTS.md"))
	})
}

// agentsBlockBeforeNaming is the block an earlier Portby wrote into AGENTS.md,
// before the agent instructions said to name a Worktree.
const agentsBlockBeforeNaming = "<!-- portby:begin (generated by `portby harness add`; edits between these markers are overwritten) -->\n" +
	"## Local environment (Portby)\n\n" +
	"This repository is served by Portby when it's installed. Every git worktree gets its own HTTPS URL, database and `.env`, so work only with this worktree's.\n\n" +
	"- Find this worktree's URL, its Processes and their states, its databases and its Service connection details with `portby info --json`. Use that URL rather than starting your own server or guessing a port.\n" +
	"- Run the project's commands (tests, artisan, composer, npm…) with `portby exec <command>`, such as `portby exec php artisan test`, so they use this worktree's PHP and Node versions.\n" +
	"- `portby worktree ready` waits until this worktree is Ready (Portby prepares each new worktree: its database, `.env` and setup commands). If it reports that preparing it failed, show the user its output; `portby worktree ready --retry` prepares it again once the problem is fixed.\n" +
	"- If there is no `portby` command, Portby isn't installed: ignore this section.\n" +
	"<!-- portby:end -->\n"

func TestHarnessAddAgainGivesAnExistingProjectTheNamingInstructionInPlace(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	const before, after = "# Agent notes\n\nRun the linter before committing.\n\n", "\nMore of mine.\n"
	repo := gitfixture.New(t, "harness-naming-upgrade").
		WriteFile("AGENTS.md", before+agentsBlockBeforeNaming+after).
		Commit("agent notes with an earlier Portby block")
	path := filepath.Join(repo.Dir, "AGENTS.md")

	res := d.MustRunCLI(repo.Dir, "harness", "add", "t3")

	assertContains(t, res.Stdout, "Updated AGENTS.md")
	doc := readFile(t, path)
	block := portbyBlock(t, doc)
	assertTellsAgentsToNameTheirWorktree(t, block)
	if doc != before+block+"\n"+after {
		t.Errorf("AGENTS.md outside the block changed:\n%s", doc)
	}
}
