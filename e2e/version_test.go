package e2e

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

func TestLaneVersionSaysItIsADevelopmentBuildWhenNoVersionWasStampedIn(t *testing.T) {
	t.Parallel()
	d := testkit.StartDaemon(t)
	res := d.MustRunCLI(gitfixture.TempDir(t), "version")
	if got := strings.TrimSpace(res.Stdout); got != "lane dev" {
		t.Fatalf("lane version printed %q, want %q", got, "lane dev")
	}
}

func TestLaneVersionPrintsTheVersionAReleaseBuildStampsIn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds the lane binary")
	}
	bin := filepath.Join(t.TempDir(), "lane")
	build := exec.Command("go", "build", "-o", bin,
		"-ldflags", "-X github.com/lanewayhq/laneway/internal/cli.version=v1.2.3", "../cmd/lane")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building lane: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("lane version: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "lane v1.2.3" {
		t.Fatalf("lane version printed %q, want %q", got, "lane v1.2.3")
	}
}
