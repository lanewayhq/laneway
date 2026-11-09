package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// Plugin commands (`lane <plugin> <command>`) through Seam 1, with the tasks
// test Plugin (internal/testkit/testplugins/tasks): each of its commands
// returns effects showing what Laneway does with them.

// tasksProject is a Linked Project with the tasks test Plugin added.
func tasksProject(t *testing.T, d *testkit.Daemon, name string) *gitfixture.Repo {
	t.Helper()
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "tasks"))
	repo := gitfixture.New(t, name).WriteFile("marker", "main\n").Commit("a marker")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	return repo
}

func TestAPluginCommandRunsTheCommandsItReturnsWithItsArguments(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := tasksProject(t, d, "tasks-greet")

	res := d.MustRunCLI(repo.Dir, "tasks", "greet", "Ada", "Grace")

	assertContains(t, res.Stdout, "hello Ada Grace\n")
}

func TestAPluginCommandGetsTheWorktreeItWasRunInAndItsCommandsRunThere(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := tasksProject(t, d, "tasks-worktree")
	wt := repo.AddWorktree("feature")
	wt.WriteFile("marker", "feature\n")
	sub := filepath.Join(wt.Dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	marker := d.MustRunCLI(sub, "tasks", "marker")
	where := d.MustRunCLI(sub, "tasks", "where")

	assertContains(t, marker.Stdout, "marker: feature\n")
	assertContains(t, where.Stdout, "dir: "+wt.Dir+"\n")
	assertContains(t, where.Stdout, "worktree: feature\n")
}

func TestAPluginCommandRunsInTheWorktreeNamedWithTheWorktreeFlag(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := tasksProject(t, d, "tasks-flag")
	wt := repo.AddWorktree("feature")
	wt.WriteFile("marker", "feature\n")
	waitForWorktree(t, d, "tasks-flag", "feature")

	res := d.MustRunCLI(repo.Dir, "tasks", "--worktree", "feature", "marker")

	assertContains(t, res.Stdout, "marker: feature\n")
}

func TestHelpForAPluginListsItsCommands(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "tasks"))

	res := d.MustRunCLI(d.Home, "tasks", "--help")

	for _, want := range []string{
		"Usage: lane tasks [--worktree <name>] <command> [args...]",
		"Commands from Plugin tasks 0.1.0:",
		"  greet   Say hello to each argument\n",
		"  marker  Print the Worktree's marker file\n",
	} {
		assertContains(t, res.Stdout, want)
	}
}

func TestAPluginCommandTheDeveloperMistypedListsThePluginsCommands(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := tasksProject(t, d, "tasks-typo")

	res := d.RunCLI(repo.Dir, "tasks", "gret")

	if res.ExitCode != 2 {
		t.Errorf("a command the Plugin doesn't have exited %d, want 2:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, "lane tasks: Plugin tasks has no command \"gret\"; `lane tasks --help` lists its commands")
}

func TestLaneHelpListsInstalledPluginsWithCommands(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "tasks"))

	res := d.MustRunCLI(d.Home, "--help")

	assertContains(t, res.Stdout, "  tasks    Commands from Plugin tasks (lane tasks --help lists them)\n")
}

func TestAPluginWithCommandsCantTakeTheNameOfOneOfLanewaysCommands(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := withManifest(t, testkit.Plugin(t, "tasks"), `{
  "name": "logs",
  "version": "0.1.0",
  "api_version": 1,
  "commands": [{"name": "tail", "summary": "Tail the logs"}]
}`)

	res := d.RunCLI(d.Home, "plugin", "add", "--yes", plugin)

	if res.ExitCode == 0 {
		t.Fatalf("adding a Plugin called logs with commands exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "Plugin logs adds commands, run as `lane logs <command>`, but `lane logs` is Laneway's own command; rename the Plugin")
	if list := d.MustRunCLI(d.Home, "plugin", "list"); !strings.Contains(list.Stdout, "No Plugins are installed") {
		t.Errorf("the refused Plugin was added anyway:\n%s", list.Stdout)
	}
}

func TestAddingAPluginShowsTheCommandsItAddsAndTheListNamesThem(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)

	add := d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "tasks"))
	list := d.MustRunCLI(d.Home, "plugin", "list")

	assertContains(t, add.Stdout, "adds the command `lane tasks greet`: Say hello to each argument\n")
	assertContains(t, list.Stdout, "command greet, command marker")
}

func TestAPluginCommandsWarningsAreShown(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := tasksProject(t, d, "tasks-warn")

	res := d.MustRunCLI(repo.Dir, "tasks", "warn", "the", "cache", "is", "stale")

	assertContains(t, res.Stderr, "lane tasks: warning: the cache is stale\n")
}

func TestACommandAPluginCommandRunsThatFailsStopsTheRestAndTheCommandFails(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := tasksProject(t, d, "tasks-stop")

	res := d.RunCLI(repo.Dir, "tasks", "stop")

	if res.ExitCode != 1 {
		t.Errorf("exited %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stdout, "before\n")
	if strings.Contains(res.Stdout, "after") {
		t.Errorf("the effect after the failed command was applied:\n%s", res.Stdout)
	}
	assertContains(t, res.Stderr, "lane tasks: stop: `sh -c echo before; exit 3` exited with status 3\n")
}

func TestAPluginCommandThatFailsSaysWhy(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := tasksProject(t, d, "tasks-fail")

	res := d.RunCLI(repo.Dir, "tasks", "fail")

	if res.ExitCode != 1 {
		t.Errorf("exited %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, "lane tasks: fail: the command `lane tasks fail`: tasks can't do that here\n")
}

func TestAnEffectKindAPluginDoesntDeclareIsRefusedWithAWarning(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := tasksProject(t, d, "tasks-env")

	res := d.MustRunCLI(repo.Dir, "tasks", "env")

	assertContains(t, res.Stderr, "lane tasks: warning: Plugin tasks returned an env effect, but its manifest doesn't declare it may set keys in Worktrees' env files, so Laneway refused it\n")
	assertContains(t, res.Stdout, "still ran\n")
	if env, _ := os.ReadFile(filepath.Join(repo.Dir, ".env")); strings.Contains(string(env), "TASKS") {
		t.Errorf("the refused env effect was applied:\n%s", env)
	}
}

func TestAPluginCommandOutsideALinkedProjectSaysToLinkIt(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "tasks"))
	repo := gitfixture.New(t, "tasks-unlinked")

	res := d.RunCLI(repo.Dir, "tasks", "greet")

	if res.ExitCode != 1 {
		t.Errorf("exited %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, "is not Linked; run `lane link` there first")
}

func TestAPluginCommandMustSayWhatItDoes(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	plugin := withManifest(t, testkit.Plugin(t, "tasks"), `{
  "name": "tasks",
  "version": "0.1.0",
  "api_version": 1,
  "commands": [{"name": "greet"}]
}`)

	res := d.RunCLI(d.Home, "plugin", "add", "--yes", plugin)

	if res.ExitCode == 0 {
		t.Fatalf("adding a Plugin with a command without a summary exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "commands[0] (greet): summary isn't set; say what the command does, for `lane tasks --help`")
}
