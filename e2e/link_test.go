package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/mrsafalpiya/portby.dev/internal/gitfixture"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

func TestLinkRegistersProjectNamedAfterDirectory(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "shop")

	res := d.MustRunCLI(repo.Dir, "link")
	assertContains(t, res.Stdout, `Linked Project "shop"`)

	projects, err := d.Client().Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Name != "shop" || projects[0].Path != repo.Dir {
		t.Fatalf("projects = %+v, want one Project shop at %s", projects, repo.Dir)
	}
}

func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("output does not contain %q:\n%s", want, got)
	}
}

func TestLinkUsesNameFromPortbyToml(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "shop-checkout").
		WriteFile("portby.toml", "name = \"shop\"\n").
		Commit("add portby.toml")

	res := d.MustRunCLI(repo.Dir, "link")
	assertContains(t, res.Stdout, `Linked Project "shop"`)

	status := d.MustRunCLI(repo.Dir, "status")
	assertContains(t, status.Stdout, "shop")
	assertNotContains(t, status.Stdout, "shop-checkout ")
}

func assertNotContains(t *testing.T, got, unwanted string) {
	t.Helper()
	if strings.Contains(got, unwanted) {
		t.Fatalf("output contains %q:\n%s", unwanted, got)
	}
}

func TestLinkRefusesTakenNameAndSuggestsNameFlag(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	first := gitfixture.New(t, "shop")
	second := gitfixture.New(t, "shop") // a different repository with the same directory name
	d.MustRunCLI(first.Dir, "link")

	res := d.RunCLI(second.Dir, "link")
	if res.ExitCode == 0 {
		t.Fatalf("linking a taken name succeeded:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `"shop" is already taken`)
	assertContains(t, res.Stderr, "--name")

	projects, err := d.Client().Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Path != first.Dir {
		t.Fatalf("projects = %+v, want only the first shop", projects)
	}
}

func TestLinkWithNameFlag(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	first := gitfixture.New(t, "shop")
	second := gitfixture.New(t, "shop")
	d.MustRunCLI(first.Dir, "link")

	res := d.MustRunCLI(second.Dir, "link", "--name", "shop-v2")
	assertContains(t, res.Stdout, `Linked Project "shop-v2"`)

	projects, err := d.Client().Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range projects {
		got[p.Name] = p.Path
	}
	if len(got) != 2 || got["shop"] != first.Dir || got["shop-v2"] != second.Dir {
		t.Fatalf("projects = %+v, want shop and shop-v2", projects)
	}
}

func TestLinkFromLinkedWorktreeRegistersOwningRepository(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "shop")
	wt := repo.AddWorktree("feature-x")

	res := d.MustRunCLI(wt.Dir, "link")
	assertContains(t, res.Stdout, `Linked Project "shop" (`+repo.Dir+`)`)
	assertContains(t, res.Stdout, `You're in Worktree "feature-x"`)

	projects, err := d.Client().Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Name != "shop" || projects[0].Path != repo.Dir {
		t.Fatalf("projects = %+v, want one Project shop at %s", projects, repo.Dir)
	}
}

func TestLinkFromSubdirectoryOfMainWorktree(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "shop").WriteFile("app/Models/.keep", "").Commit("add app")

	res := d.MustRunCLI(repo.Dir+"/app/Models", "link")
	assertContains(t, res.Stdout, `Linked Project "shop" (`+repo.Dir+`)`)
	assertContains(t, res.Stdout, "You're in the main Worktree")
}

func TestLinkingAgainFromAnotherWorktreeGivesTheSameProject(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "shop")
	wt := repo.AddWorktree("feature-x")
	d.MustRunCLI(repo.Dir, "link")

	res := d.MustRunCLI(wt.Dir, "link")
	assertContains(t, res.Stdout, `already Linked as Project "shop"`)
	assertContains(t, res.Stdout, `You're in Worktree "feature-x"`)

	projects, err := d.Client().Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("projects = %+v, want one", projects)
	}
}

func TestLinkOutsideGitRepositoryFailsClearly(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	dir := gitfixture.NotARepo(t, "notes")

	res := d.RunCLI(dir, "link")
	if res.ExitCode == 0 {
		t.Fatalf("linking a non-repository succeeded:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, dir+" is not inside a git repository")

	projects, err := d.Client().Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Fatalf("projects = %+v, want none", projects)
	}
}
