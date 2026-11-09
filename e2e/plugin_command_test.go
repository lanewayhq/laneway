package e2e

import (
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
