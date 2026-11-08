package e2e

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

func TestOpenOpensTheWorktreeDomainInTheBrowser(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "open-shop").WriteFile("app/readme.txt", "hi").Commit("app dir")
	d.MustRunCLI(repo.Dir, "link")

	res := d.MustRunCLI(filepath.Join(repo.Dir, "app"), "open")

	want := []string{"https://open-shop.localhost/"}
	if got := d.Platform.OpenedURLs(); !slices.Equal(got, want) {
		t.Fatalf("opened %q, want %q", got, want)
	}
	assertContains(t, res.Stdout, "https://open-shop.localhost/")
}

func TestOpenOutsideALinkedRepositoryFails(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "open-unlinked")

	res := d.RunCLI(repo.Dir, "open")

	if res.ExitCode == 0 {
		t.Fatalf("open in an unlinked repository succeeded:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, "lane link")
	if got := d.Platform.OpenedURLs(); len(got) != 0 {
		t.Fatalf("opened %q, want nothing", got)
	}
}

func TestOpenInALinkedWorktreeOpensItsWorktreeDomain(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	repo := gitfixture.New(t, "open-wt")
	d.MustRunCLI(repo.Dir, "link")
	wt := repo.AddWorktree("feature-x")
	waitForWorktree(t, d, "open-wt", "feature-x")

	d.MustRunCLI(wt.Dir, "open")

	want := []string{"https://feature-x.open-wt.localhost/"}
	if got := d.Platform.OpenedURLs(); !slices.Equal(got, want) {
		t.Fatalf("opened %q, want %q", got, want)
	}
}
