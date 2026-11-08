package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInitWritesLanewayTomlWithoutTheDaemon(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Stop()
	repo := gitfixture.New(t, "init-fresh")

	res := d.MustRunCLI(repo.Dir, "init")

	path := filepath.Join(repo.Dir, "laneway.toml")
	assertContains(t, res.Stdout, path)
	// No Recipe matches yet, so the file only explains how to add Processes.
	assertContains(t, readFile(t, path), "[processes.")
}

func TestInitRefusesToOverwriteLanewayToml(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	const existing = "# hand-written\n[processes.web]\ncommand = \"serve\"\n"
	repo := gitfixture.New(t, "init-exists").
		WriteFile("laneway.toml", existing).
		Commit("add config")

	res := d.RunCLI(repo.Dir, "init")

	if res.ExitCode == 0 {
		t.Fatalf("init overwrote laneway.toml:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "--force")
	if got := readFile(t, filepath.Join(repo.Dir, "laneway.toml")); got != existing {
		t.Fatalf("laneway.toml changed to:\n%s", got)
	}
}

func TestInitForceRewritesTheResolvedCommittedConfig(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	repo := gitfixture.New(t, "init-force").
		WriteFile("laneway.toml", "name = \"init-forced\"\n\n"+
			"[processes.web]\ncommand = '"+server+` --name "web \"quoted\""`+"'\n\n"+
			"[processes.scheduler]\ndisabled = true\n").
		Commit("add config").
		WriteFile("laneway.local.toml", "[processes.web]\ncommand = \"personal\"\n")

	d.MustRunCLI(repo.Dir, "init", "--force")

	got := readFile(t, filepath.Join(repo.Dir, "laneway.toml"))
	assertContains(t, got, "name = \"init-forced\"")
	// The only Process is the Primary Process, so init says so.
	assertContains(t, got, "[processes.web]\ncommand = ")
	assertContains(t, got, "primary = true")
	assertContains(t, got, "[processes.scheduler]\ndisabled = true\n")
	if strings.Contains(got, `"personal"`) {
		t.Fatalf("init wrote laneway.local.toml's settings into laneway.toml:\n%s", got)
	}

	// The rewritten file means the same: the committed command, quotes
	// and all, serves the Domain.
	repo.WriteFile("laneway.local.toml", "")
	d.MustRunCLI(repo.Dir, "link")
	if res := d.GetServer("https://init-forced.localhost/"); res.Name != `web "quoted"` {
		t.Fatalf("served by %q, want the committed web Process", res.Name)
	}
}

func TestInitReportsInvalidConfigNamingTheFileAndKey(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	const existing = "[processes.web]\nprimary = \"yes\"\n"
	repo := gitfixture.New(t, "init-invalid").
		WriteFile("laneway.toml", existing).
		Commit("add config")

	res := d.RunCLI(repo.Dir, "init", "--force")

	if res.ExitCode == 0 {
		t.Fatalf("init succeeded with invalid config:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "laneway.toml")
	assertContains(t, res.Stderr, "processes.web.primary")
	if got := readFile(t, filepath.Join(repo.Dir, "laneway.toml")); got != existing {
		t.Fatalf("laneway.toml changed to:\n%s", got)
	}
}

func TestInitWritesAtTheWorktreeRootFromASubdirectory(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "init-subdir").
		WriteFile("app/Http/.keep", "").
		Commit("add app")

	d.MustRunCLI(filepath.Join(repo.Dir, "app", "Http"), "init")

	readFile(t, filepath.Join(repo.Dir, "laneway.toml"))
	if _, err := os.Stat(filepath.Join(repo.Dir, "app", "Http", "laneway.toml")); err == nil {
		t.Fatal("init wrote laneway.toml in the subdirectory")
	}
}
