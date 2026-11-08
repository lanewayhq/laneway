package e2e

import (
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

func TestLocalConfigOverridesCommittedConfig(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	repo := gitfixture.New(t, "config-local").
		WriteFile("laneway.toml", processConfig(server+" --name committed")).
		Commit("declare web Process").
		WriteFile("laneway.local.toml", "[processes.web]\ncommand = \""+server+" --name local\"\n")

	d.MustRunCLI(repo.Dir, "link")

	if res := d.GetServer("https://config-local.localhost/"); res.Name != "local" {
		t.Fatalf("served by the %q Process command, want the one from laneway.local.toml", res.Name)
	}
}

func TestConfigChangesTakeEffectWhenProcessesNextStart(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	repo := gitfixture.New(t, "config-change").
		WriteFile("laneway.toml", processConfig(server+" --name before")).
		Commit("declare web Process")
	d.MustRunCLI(repo.Dir, "link")
	if res := d.GetServer("https://config-change.localhost/"); res.Name != "before" {
		t.Fatalf("served by %q, want before", res.Name)
	}

	repo.WriteFile("laneway.toml", processConfig(server+" --name after"))
	d.Restart()

	if res := d.GetServer("https://config-change.localhost/"); res.Name != "after" {
		t.Fatalf("served by %q after the Processes restarted, want after", res.Name)
	}
}

func TestLinkReportsInvalidConfigNamingTheFileAndKey(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "config-invalid").
		WriteFile("laneway.toml", processConfig("serve")).
		Commit("declare web Process").
		WriteFile("laneway.local.toml", "[processes.web]\ncommand = 8080\n")

	res := d.RunCLI(repo.Dir, "link")

	if res.ExitCode == 0 {
		t.Fatalf("link succeeded with invalid config:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "laneway.local.toml")
	assertContains(t, res.Stderr, "processes.web.command")
}
