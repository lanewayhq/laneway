package e2e

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lanewayhq/laneway/internal/container"
	"github.com/lanewayhq/laneway/internal/container/containertest"
	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// A new Worktree's Bucket starts as a copy of the main Worktree's, under a
// size limit (lanewayhq/laneway#95), through the fake object storage
// server: see buckets_test.go.

// putObject puts an object in the objects Service, failing the test unless
// it is stored.
func putObject(t *testing.T, d *testkit.Daemon, path, body string) {
	t.Helper()
	if status, answer := s3(t, d, "PUT", path, body); status != http.StatusOK {
		t.Fatalf("putting %s answered %d: %s", path, status, answer)
	}
}

// assertObject fails the test unless the object at path holds body.
func assertObject(t *testing.T, d *testkit.Daemon, path, body string) {
	t.Helper()
	if status, got := s3(t, d, "GET", path, ""); status != http.StatusOK || got != body {
		t.Fatalf("GET %s answered %d %q, want 200 %q", path, status, got, body)
	}
}

// objectKeys are the keys of the objects in the Bucket name, sorted.
func objectKeys(t *testing.T, d *testkit.Daemon, name string) []string {
	t.Helper()
	status, body := s3(t, d, "GET", "/"+name, "")
	if status != http.StatusOK {
		t.Fatalf("listing the Bucket %s answered %d: %s", name, status, body)
	}
	var keys []string
	for _, m := range regexp.MustCompile(`<Key>([^<]*)</Key>`).FindAllStringSubmatch(body, -1) {
		keys = append(keys, m[1])
	}
	return keys
}

func assertEmptyBucket(t *testing.T, d *testkit.Daemon, name string) {
	t.Helper()
	if keys := objectKeys(t, d, name); len(keys) > 0 {
		t.Fatalf("the Bucket %s holds %q, want it empty", name, keys)
	}
}

func TestANewWorktreesBucketStartsAsACopyOfTheMainWorktreesBucket(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	repo := serviceProject(t, "bkc-copy", `objects = "1"`, "")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	putObject(t, d, "/bkc-copy/avatars/1.png", "first avatar")
	putObject(t, d, "/bkc-copy/invoices/2026/7.pdf", "an invoice")

	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertObject(t, d, "/bkc-copy--feature/avatars/1.png", "first avatar")
	assertObject(t, d, "/bkc-copy--feature/invoices/2026/7.pdf", "an invoice")
	// A copy: what the new Worktree does to it stays in its own Bucket.
	putObject(t, d, "/bkc-copy--feature/avatars/1.png", "changed in the feature")
	assertObject(t, d, "/bkc-copy/avatars/1.png", "first avatar")
}

func TestTheMainWorktreesBucketStartsEmpty(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	other := serviceProject(t, "bkc-other", `objects = "1"`, "")
	d.MustRunCLI(other.Dir, "link")
	d.MustRunCLI(other.Dir, "worktree", "ready")
	putObject(t, d, "/bkc-other/logo.svg", "another Project's file")
	repo := serviceProject(t, "bkc-main", `objects = "1"`, "")

	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	assertBuckets(t, d, "bkc-other", "bkc-main")
	assertEmptyBucket(t, d, "bkc-main")
}

func TestAMainWorktreeBucketOverTheLimitIsNotCopiedAndPreparingWarnsNamingTheSetting(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	repo := serviceProject(t, "bkc-big", `objects = "1"`, "bucket_copy_limit = \"10B\"\n\n")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	putObject(t, d, "/bkc-big/video.mp4", "eleven byte")

	wt := repo.AddWorktree("feature")
	res := d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertBuckets(t, d, "bkc-big", "bkc-big--feature")
	assertEmptyBucket(t, d, "bkc-big--feature")
	assertContains(t, res.Stdout, "is Ready")
	assertContains(t, res.Stderr, "warning: the main Worktree's Bucket bkc-big holds 11 B, more than the 10 B limit on copying a Bucket, "+
		"so bkc-big--feature was created empty instead of copied from it")
	assertContains(t, res.Stderr, "raise bucket_copy_limit (now 10 B) in laneway.toml")
	assertSetupState(t, d, wt.Dir, "ready")
}

func TestTheBucketCopyLimitComesFromTheGlobalConfigUnlessTheProjectSetsItsOwn(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	writeGlobalConfig(t, d, "bucket_copy_limit = \"1kB\"\n")
	global := serviceProject(t, "bkc-global", `objects = "1"`, "")
	own := serviceProject(t, "bkc-own", `objects = "1"`, "bucket_copy_limit = \"2kB\"\n\n")
	for name, repo := range map[string]*gitfixture.Repo{"bkc-global": global, "bkc-own": own} {
		d.MustRunCLI(repo.Dir, "link")
		d.MustRunCLI(repo.Dir, "worktree", "ready")
		putObject(t, d, "/"+name+"/photo.jpg", strings.Repeat("x", 1500))
	}

	globalWt := global.AddWorktree("feature")
	res := d.MustRunCLI(globalWt.Dir, "worktree", "ready")
	ownWt := own.AddWorktree("feature")
	d.MustRunCLI(ownWt.Dir, "worktree", "ready")

	assertEmptyBucket(t, d, "bkc-global--feature")
	assertContains(t, res.Stderr, "holds 1.5 kB, more than the 1 kB limit")
	assertContains(t, res.Stderr, "for every Project in "+globalConfig(d))
	assertObject(t, d, "/bkc-own--feature/photo.jpg", strings.Repeat("x", 1500))
}

// breakClone makes the objects Service's clone command fail with stderr
// and exit status code while the returned flag is set; it starts set.
func breakClone(d *testkit.Daemon, code int, stderr string) *atomic.Bool {
	var broken atomic.Bool
	broken.Store(true)
	d.Engine.AnswerCommands(objectsImage, func(ctx context.Context, spec container.Spec, data *containertest.Volume, cmd []string) container.Output {
		if broken.Load() && len(cmd) == 6 && strings.Contains(cmd[2], "cp --recursive") {
			return container.Output{ExitCode: code, Stderr: []byte(stderr)}
		}
		return containertest.ObjectStoreCommands(ctx, spec, data, cmd)
	})
	return &broken
}

func TestAFailedBucketCopyFailsPreparingWithItsOutputAndRetryingCopiesIt(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	repo := serviceProject(t, "bkc-fail", `objects = "1"`, "")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	putObject(t, d, "/bkc-fail/report.csv", "id,total")
	broken := breakClone(d, 4, "Error: Access denied: local/bkc-fail/report.csv\n")

	wt := repo.AddWorktree("feature")
	res := d.RunCLI(wt.Dir, "worktree", "ready")

	if res.ExitCode != 1 {
		t.Fatalf("worktree ready exited %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, `failed at step "bucket bkc-fail--feature"`)
	assertContains(t, res.Stderr, "copying the main Worktree's Bucket bkc-fail to bkc-fail--feature: objects's clone command exited with status 4")
	assertContains(t, res.Stderr, "Access denied: local/bkc-fail/report.csv")
	// Preparing's output shows the copy it was making.
	assertContains(t, res.Stderr, "  copying the main Worktree's Bucket bkc-fail (8 B) to bkc-fail--feature\n")
	assertSetupState(t, d, wt.Dir, "failed")
	assertBuckets(t, d, "bkc-fail")

	broken.Store(false)
	d.MustRunCLI(wt.Dir, "worktree", "ready", "--retry")

	assertSetupState(t, d, wt.Dir, "ready")
	assertObject(t, d, "/bkc-fail--feature/report.csv", "id,total")
}

func TestABucketCopyCutShortIsCopiedAgainRatherThanKept(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	repo := serviceProject(t, "bkc-cut", `objects = "1"`, "")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	putObject(t, d, "/bkc-cut/a.txt", "first")
	putObject(t, d, "/bkc-cut/b.txt", "second")
	broken := breakClone(d, 137, "")
	wt := repo.AddWorktree("feature")
	if res := d.RunCLI(wt.Dir, "worktree", "ready"); res.ExitCode != 1 {
		t.Fatalf("worktree ready exited %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	// What a clone killed with its container leaves: the new Bucket with
	// some of the objects, still tagged as unfinished.
	putObject(t, d, "/bkc-cut--feature", "")
	putObject(t, d, "/bkc-cut--feature?tagging", "<Tagging><TagSet><Tag><Key>laneway-clone</Key><Value>incomplete</Value></Tag></TagSet></Tagging>")
	putObject(t, d, "/bkc-cut--feature/a.txt", "first")

	broken.Store(false)
	d.MustRunCLI(wt.Dir, "worktree", "ready", "--retry")

	assertObject(t, d, "/bkc-cut--feature/a.txt", "first")
	assertObject(t, d, "/bkc-cut--feature/b.txt", "second")
	if status, body := s3(t, d, "GET", "/bkc-cut--feature?tagging", ""); status != http.StatusNotFound {
		t.Fatalf("the copied Bucket's tags answered %d %q, want none (404)", status, body)
	}
}
