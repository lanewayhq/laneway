package e2e

import (
	"strings"
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

func TestStatusWithNoProjects(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)

	res := d.MustRunCLI(d.Home, "status")
	assertContains(t, res.Stdout, "No Projects linked yet")
}

func TestStatusListsProjects(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	shop := gitfixture.New(t, "shop")
	blog := gitfixture.New(t, "blog")
	d.MustRunCLI(shop.Dir, "link")
	d.MustRunCLI(blog.Dir, "link")

	res := d.MustRunCLI(d.Home, "status")
	assertStatusRows(t, res.Stdout, [][2]string{{"blog", blog.Dir}, {"shop", shop.Dir}})
}

func TestProjectsSurviveDaemonRestart(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	shop := gitfixture.New(t, "shop")
	blog := gitfixture.New(t, "blog-src").WriteFile("portby.toml", `name = "blog"`).Commit("config")
	d.MustRunCLI(shop.Dir, "link")
	d.MustRunCLI(blog.Dir, "link")

	d.Restart()

	res := d.MustRunCLI(d.Home, "status")
	assertStatusRows(t, res.Stdout, [][2]string{{"blog", blog.Dir}, {"shop", shop.Dir}})

	// Uniqueness is enforced against the restored Registry too.
	again := gitfixture.New(t, "shop")
	if res := d.RunCLI(again.Dir, "link"); res.ExitCode == 0 {
		t.Fatalf("linking a taken name after restart succeeded:\n%s", res.Output())
	}
}

// assertStatusRows checks `portby status` lists exactly rows of (Project, path)
// in order, after its header line.
func assertStatusRows(t *testing.T, out string, rows [][2]string) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != len(rows)+1 {
		t.Fatalf("status has %d lines, want header + %d rows:\n%s", len(lines), len(rows), out)
	}
	for i, row := range rows {
		fields := strings.Fields(lines[i+1])
		if len(fields) != 2 || fields[0] != row[0] || fields[1] != row[1] {
			t.Fatalf("status row %d = %q, want %s %s\n%s", i, lines[i+1], row[0], row[1], out)
		}
	}
}
