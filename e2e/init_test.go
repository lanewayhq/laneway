package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInitWritesPortbyTomlWithoutTheDaemon(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	d.Stop()
	repo := gitfixture.New(t, "init-fresh")

	res := d.MustRunCLI(repo.Dir, "init")

	path := filepath.Join(repo.Dir, "portby.toml")
	assertContains(t, res.Stdout, path)
	// No Recipe matches yet, so the file only explains how to add Processes.
	assertContains(t, readFile(t, path), "[processes.")
}

func TestInitRefusesToOverwritePortbyToml(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	const existing = "# hand-written\n[processes.web]\ncommand = \"serve\"\n"
	repo := gitfixture.New(t, "init-exists").
		WriteFile("portby.toml", existing).
		Commit("add config")

	res := d.RunCLI(repo.Dir, "init")

	if res.ExitCode == 0 {
		t.Fatalf("init overwrote portby.toml:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "--force")
	if got := readFile(t, filepath.Join(repo.Dir, "portby.toml")); got != existing {
		t.Fatalf("portby.toml changed to:\n%s", got)
	}
}

func TestInitForceRewritesTheResolvedCommittedConfig(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	server := testkit.TestServer(t)
	repo := gitfixture.New(t, "init-force").
		WriteFile("portby.toml", "name = \"init-forced\"\n\n"+
			"[processes.web]\ncommand = '"+server+` --name "web \"quoted\""`+"'\n\n"+
			"[processes.scheduler]\ndisabled = true\n").
		Commit("add config").
		WriteFile("portby.local.toml", "[processes.web]\ncommand = \"personal\"\n")

	d.MustRunCLI(repo.Dir, "init", "--force")

	got := readFile(t, filepath.Join(repo.Dir, "portby.toml"))
	assertContains(t, got, "name = \"init-forced\"")
	// The only Process is the Primary Process, so init says so.
	assertContains(t, got, "[processes.web]\ncommand = ")
	assertContains(t, got, "primary = true")
	assertContains(t, got, "[processes.scheduler]\ndisabled = true\n")
	if strings.Contains(got, `"personal"`) {
		t.Fatalf("init wrote portby.local.toml's settings into portby.toml:\n%s", got)
	}

	// The rewritten file means the same: the committed command, quotes
	// and all, serves the Domain.
	repo.WriteFile("portby.local.toml", "")
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
		WriteFile("portby.toml", existing).
		Commit("add config")

	res := d.RunCLI(repo.Dir, "init", "--force")

	if res.ExitCode == 0 {
		t.Fatalf("init succeeded with invalid config:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "portby.toml")
	assertContains(t, res.Stderr, "processes.web.primary")
	if got := readFile(t, filepath.Join(repo.Dir, "portby.toml")); got != existing {
		t.Fatalf("portby.toml changed to:\n%s", got)
	}
}

func TestInitWritesAtTheWorktreeRootFromASubdirectory(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "init-subdir").
		WriteFile("app/Http/.keep", "").
		Commit("add app")

	d.MustRunCLI(filepath.Join(repo.Dir, "app", "Http"), "init")

	readFile(t, filepath.Join(repo.Dir, "portby.toml"))
	if _, err := os.Stat(filepath.Join(repo.Dir, "app", "Http", "portby.toml")); err == nil {
		t.Fatal("init wrote portby.toml in the subdirectory")
	}
}
