package e2e

import (
	"slices"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
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
	blog := gitfixture.New(t, "blog-src").WriteFile("laneway.toml", `name = "blog"`).Commit("config")
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

func TestStatusListsWorktreesUnderTheirProjects(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	shop := gitfixture.New(t, "shop")
	blog := gitfixture.New(t, "blog")
	d.MustRunCLI(shop.Dir, "link")
	d.MustRunCLI(blog.Dir, "link")
	wt := shop.AddWorktree("checkout-v2")
	d.MustRunCLI(wt.Dir, "link") // finds the new Worktree straight away
	for _, dir := range []string{blog.Dir, shop.Dir, wt.Dir} {
		d.MustRunCLI(dir, "worktree", "ready")
	}

	res := d.MustRunCLI(d.Home, "status")
	want := [][]string{
		{"PROJECT", "DOMAIN", "PATH", "READINESS", "STATE", "PROCESSES"},
		{"blog", "blog.localhost", blog.Dir, "ready", "stopped"},
		{"shop", "shop.localhost", shop.Dir, "ready", "stopped"},
		{"checkout-v2", "checkout-v2.shop.localhost", wt.Dir, "ready", "stopped"},
	}
	lines := strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("status has %d lines, want %d:\n%s", len(lines), len(want), res.Stdout)
	}
	for i, fields := range want {
		if got := strings.Fields(lines[i]); !slices.Equal(got, fields) {
			t.Fatalf("status line %d = %q, want %v\n%s", i, lines[i], fields, res.Stdout)
		}
	}
	if !strings.HasPrefix(lines[3], "  ") {
		t.Fatalf("Worktree row %q is not indented under its Project:\n%s", lines[3], res.Stdout)
	}
}

// assertStatusRows checks `lane status` lists exactly rows of (Project, path)
// in order, after its header line. Each row's Domain sits between the two,
// then its setup state, and nothing has started them.
func assertStatusRows(t *testing.T, out string, rows [][2]string) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != len(rows)+1 {
		t.Fatalf("status has %d lines, want header + %d rows:\n%s", len(lines), len(rows), out)
	}
	for i, row := range rows {
		fields := strings.Fields(lines[i+1])
		if len(fields) != 5 || fields[0] != row[0] || fields[2] != row[1] || fields[4] != "stopped" {
			t.Fatalf("status row %d = %q, want %s <domain> %s <setup> stopped\n%s", i, lines[i+1], row[0], row[1], out)
		}
	}
}
