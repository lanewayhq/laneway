package e2e

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// Generators through Seam 1: the blocks test Plugin's Generator returns the
// effects listed in the Project's blocks.json, and `lane generate blocks`
// merges them into the Worktree's files.

// blocksRepo is a Project whose blocks.json lists the effects the blocks
// test Plugin's Generator returns, with the Plugin added.
func blocksRepo(t *testing.T, d *testkit.Daemon, name, effects string) *gitfixture.Repo {
	t.Helper()
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "blocks"))
	return gitfixture.New(t, name).WriteFile("blocks.json", effects).Commit("blocks")
}

func TestGenerateWritesTheMarkdownBlockAGeneratorReturns(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := blocksRepo(t, d, "generate-markdown", `[
  {"kind": "file", "file": {"path": "docs/NOTES.md", "markdown": "Use lane exec.\n"}}
]`)

	res := d.MustRunCLI(repo.Dir, "generate", "blocks")

	assertContains(t, res.Stdout, "Created docs/NOTES.md")
	doc := readFile(t, filepath.Join(repo.Dir, "docs", "NOTES.md"))
	assertContains(t, lanewayBlock(t, doc), "\nUse lane exec.\n<!-- laneway:end -->")
}

func TestGenerateMergesJSONBlocksIntoAFileKeepingWhatLanewayDoesntOwn(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := blocksRepo(t, d, "generate-json", `[
  {"kind": "file", "file": {"path": "tool.json", "json": {"new": {"$schema": "https://example.com/tool.json"}, "entries": [
    {"at": ["servers", "laneway"], "value": {"command": "lane", "args": ["mcp"]}},
    {"at": ["tasks"], "match": {"name": "laneway"}, "value": {"name": "laneway", "run": "lane worktree ready"}}
  ]}}}
]`)
	const mine = `{
    "servers": {"mine": {"command": "my-server"}},
    "tasks": [{"name": "lint", "run": "make lint"}, {"name": "laneway", "run": "old", "cwd": "."}],
    "theme": "dark"
}
`
	repo.WriteFile("tool.json", mine).Commit("tool config")
	path := filepath.Join(repo.Dir, "tool.json")

	res := d.MustRunCLI(repo.Dir, "generate", "blocks")

	assertContains(t, res.Stdout, "Updated tool.json")
	merged := readFile(t, path)
	doc := readJSON(t, path)
	if _, ok := doc["$schema"]; ok {
		t.Errorf("json.new was added to an existing file:\n%s", merged)
	}
	servers := doc["servers"].(map[string]any)
	if servers["mine"] == nil || servers["laneway"].(map[string]any)["command"] != "lane" {
		t.Errorf("servers = %v, want mine kept and Laneway's set", servers)
	}
	tasks := doc["tasks"].([]any)
	if len(tasks) != 2 || tasks[0].(map[string]any)["name"] != "lint" {
		t.Fatalf("tasks = %v, want lint kept first and Laneway's in place", tasks)
	}
	if own := tasks[1].(map[string]any); own["run"] != "lane worktree ready" || own["cwd"] != "." {
		t.Errorf("Laneway's task = %v, want its run set and the developer's cwd kept", own)
	}
	if doc["theme"] != "dark" {
		t.Errorf("theme lost:\n%s", merged)
	}
	assertContains(t, merged, "\n    \"servers\": {")

	again := d.MustRunCLI(repo.Dir, "generate", "blocks")

	assertContains(t, again.Stdout, "tool.json is up to date")
	assertFile(t, path, merged)
}

func TestGenerateStartsANewJSONFileFromTheBlocksNewObject(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := blocksRepo(t, d, "generate-json-new", `[
  {"kind": "file", "file": {"path": "tool.json", "json": {"new": {"$schema": "https://example.com/tool.json"}, "entries": [
    {"at": ["tasks"], "match": {"name": "laneway"}, "value": {"name": "laneway", "run": "lane worktree ready"}}
  ]}}}
]`)

	res := d.MustRunCLI(repo.Dir, "generate", "blocks")

	assertContains(t, res.Stdout, "Created tool.json")
	assertFile(t, filepath.Join(repo.Dir, "tool.json"), `{
  "$schema": "https://example.com/tool.json",
  "tasks": [
    {
      "name": "laneway",
      "run": "lane worktree ready"
    }
  ]
}
`)
}

func TestGenerateWritesNothingWhenOneFileCannotBeMerged(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := blocksRepo(t, d, "generate-broken", `[
  {"kind": "file", "file": {"path": "NOTES.md", "markdown": "Use lane exec.\n"}},
  {"kind": "file", "file": {"path": "tool.json", "json": {"entries": [{"at": ["laneway"], "value": true}]}}}
]`)
	const broken = "{\n  \"servers\": [\n"
	repo.WriteFile("tool.json", broken).Commit("broken tool config")

	res := d.RunCLI(repo.Dir, "generate", "blocks")

	if res.ExitCode == 0 {
		t.Fatalf("generate succeeded on a broken file:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "tool.json")
	assertFile(t, filepath.Join(repo.Dir, "tool.json"), broken)
	assertNoFile(t, filepath.Join(repo.Dir, "NOTES.md"))
}

func TestGenerateRefusesAnEffectThePluginDoesntDeclareAndWritesNothing(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := withManifest(t, testkit.Plugin(t, "blocks"), `{
  "name": "blocks",
  "version": "0.1.0",
  "api_version": 1,
  "generators": [{"name": "blocks"}],
  "capabilities": {"effects": ["file"]}
}`)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", plugin)
	repo := gitfixture.New(t, "generate-undeclared").WriteFile("blocks.json", `[
  {"kind": "file", "file": {"path": "NOTES.md", "markdown": "Use lane exec.\n"}},
  {"kind": "warning", "warning": "something to know"}
]`).Commit("blocks")

	res := d.RunCLI(repo.Dir, "generate", "blocks")

	if res.ExitCode == 0 {
		t.Fatalf("generate applied an undeclared effect:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `"warning" effect`)
	assertContains(t, res.Stderr, "capabilities.effects")
	assertNoFile(t, filepath.Join(repo.Dir, "NOTES.md"))
}

func TestGenerateRefusesAFileBlockOutsideTheWorktree(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := blocksRepo(t, d, "generate-outside", `[
  {"kind": "file", "file": {"path": "../escaped.md", "markdown": "Use lane exec.\n"}}
]`)

	res := d.RunCLI(repo.Dir, "generate", "blocks")

	if res.ExitCode == 0 {
		t.Fatalf("generate wrote outside the Worktree:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "../escaped.md")
	assertNoFile(t, filepath.Join(filepath.Dir(repo.Dir), "escaped.md"))
}

func TestGenerateShowsTheGeneratorsWarnings(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := blocksRepo(t, d, "generate-warnings", `[
  {"kind": "file", "file": {"path": "NOTES.md", "markdown": "Use lane exec.\n", "warning": "run the docs build"}},
  {"kind": "warning", "warning": "something to know"}
]`)

	res := d.MustRunCLI(repo.Dir, "generate", "blocks")

	assertContains(t, res.Stderr, "warning: something to know")
	assertContains(t, res.Stderr, "warning: run the docs build")
	again := d.MustRunCLI(repo.Dir, "generate", "blocks")
	assertContains(t, again.Stderr, "warning: something to know")
	assertNotContains(t, again.Stderr, "docs build")
}

func TestGenerateNamesAPluginThatIsntInstalled(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "generate-unknown")

	res := d.RunCLI(repo.Dir, "generate", "cursor")

	if res.ExitCode == 0 {
		t.Fatalf("generate succeeded for a Plugin that isn't installed:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "cursor")
	assertContains(t, res.Stderr, "lane plugin add")
}

func TestGenerateRunsTheBuiltInHarnessGeneratorsLikeHarnessAdd(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	for _, harness := range []string{"claude", "t3"} {
		viaHarness := gitfixture.New(t, "harness-add-"+harness).WriteFile("CLAUDE.md", "# Project\n").Commit("notes")
		viaGenerate := gitfixture.New(t, "generate-"+harness).WriteFile("CLAUDE.md", "# Project\n").Commit("notes")

		want := d.MustRunCLI(viaHarness.Dir, "harness", "add", harness)
		got := d.MustRunCLI(viaGenerate.Dir, "generate", harness)

		if got.Stdout != want.Stdout {
			t.Errorf("lane generate %s said %q, want what lane harness add said: %q", harness, got.Stdout, want.Stdout)
		}
		for _, file := range []string{".claude/settings.json", "t3.json", "AGENTS.md", "CLAUDE.md"} {
			a, b := filepath.Join(viaHarness.Dir, file), filepath.Join(viaGenerate.Dir, file)
			if readFileOrNone(t, a) != readFileOrNone(t, b) {
				t.Errorf("%s: lane generate %s wrote %q, lane harness add %q", file, harness, readFileOrNone(t, b), readFileOrNone(t, a))
			}
		}
	}
}

func TestGenerateBoostGivesBoostTheAgentInstructions(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "generate-boost").WriteFile("composer.json", boostComposer).Commit("boost")

	res := d.MustRunCLI(repo.Dir, "generate", "boost")

	assertContains(t, res.Stdout, "Created .ai/guidelines/laneway.md")
	assertContains(t, res.Stderr, "php artisan boost:update")
	assertContains(t, lanewayBlock(t, readFile(t, filepath.Join(repo.Dir, ".ai", "guidelines", "laneway.md"))), "lane info --json")
	assertNoFile(t, filepath.Join(repo.Dir, "AGENTS.md"))
}

// readFileOrNone returns the file at path, or "(none)" if it doesn't exist.
func readFileOrNone(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "(none)"
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAddingAGeneratorPluginShowsItsGeneratorsAndListShowsThem(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := testkit.Plugin(t, "blocks")

	preview := d.RunCLIWithInput(d.Home, "n\n", "plugin", "add", plugin)

	assertContains(t, preview.Stdout, "adds the Generator blocks, run with `lane generate blocks`")
	assertContains(t, preview.Stdout, "may merge blocks into files")
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", plugin)
	assertContains(t, lineWith(t, d.MustRunCLI(d.Home, "plugin", "list").Stdout, "blocks"), "Generator blocks")
}

func TestAPluginCannotTakeTheNameOfABuiltInPlugin(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := withManifest(t, testkit.Plugin(t, "blocks"), `{
  "name": "claude",
  "version": "0.1.0",
  "api_version": 1,
  "generators": [{"name": "blocks"}],
  "capabilities": {"effects": ["file"]}
}`)

	res := d.RunCLI(d.Home, "plugin", "add", "--yes", plugin)

	if res.ExitCode == 0 {
		t.Fatalf("a Plugin took the name of a built-in one:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "built-in Plugin named claude")
}

func TestAGeneratorPluginMustDeclareFileEffects(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := withManifest(t, testkit.Plugin(t, "blocks"), `{
  "name": "blocks",
  "version": "0.1.0",
  "api_version": 1,
  "generators": [{"name": "blocks"}]
}`)

	res := d.RunCLI(d.Home, "plugin", "add", "--yes", plugin)

	if res.ExitCode == 0 {
		t.Fatalf("a Generator Plugin without file effects was added:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `capabilities.effects doesn't include "file"`)
}
