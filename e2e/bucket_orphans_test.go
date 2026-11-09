package e2e

import (
	"slices"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/clock"
	"github.com/lanewayhq/laneway/internal/daemon"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// A removed Worktree's Buckets become Orphans like its databases (ADR-0008),
// through the objects test Plugin and the fake object storage server (see
// buckets_test.go).

// bucketOrphanDaemon is an objectsDaemon on a fake clock at orphanEpoch,
// for driving the Orphan grace period.
func bucketOrphanDaemon(t *testing.T) (*testkit.Daemon, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(orphanEpoch)
	return objectsDaemon(t, testkit.WithClock(clk)), clk
}

func TestRemovingAWorktreeLeavesItsBucketInPlaceAsAnOrphan(t *testing.T) {
	t.Parallel()
	d, _ := bucketOrphanDaemon(t)
	repo := serviceProject(t, "bo-remove", `objects = "1"`, "")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	s3(t, d, "PUT", "/bo-remove--feature/photos/cat.jpg", "meow")

	repo.Git("worktree", "remove", wt.Dir)

	waitForOrphans(t, d, "bo-remove--feature")
	o := orphan(t, d, "bo-remove--feature")
	if o.Kind != "bucket" || o.Project != "bo-remove" || o.Worktree != "feature" || o.Service != "objects:1" {
		t.Errorf("Orphan = %+v, want the bucket of Worktree feature of bo-remove in objects:1", o)
	}
	if !o.Since.Equal(orphanEpoch) || !o.Expires.Equal(orphanEpoch.Add(24*time.Hour)) {
		t.Errorf("Orphan since %v, expires %v; want since %v, expires 24 hours later", o.Since, o.Expires, orphanEpoch)
	}
	assertBuckets(t, d, "bo-remove", "bo-remove--feature")
	if status, body := s3(t, d, "GET", "/bo-remove--feature/photos/cat.jpg", ""); status != 200 || body != "meow" {
		t.Fatalf("the Orphan's object answered %d %q, want it kept", status, body)
	}
}

// createBuckets creates empty Buckets in the objects Service, as the
// Bucket commands (or the developer) would.
func createBuckets(t *testing.T, d *testkit.Daemon, names ...string) {
	t.Helper()
	for _, name := range names {
		if status, body := s3(t, d, "PUT", "/"+name, ""); status != 200 {
			t.Fatalf("creating the Bucket %s answered %d: %s", name, status, body)
		}
	}
}

func TestEveryBucketTheNamingRuleGivesARemovedWorktreeIsAnOrphanButNoOtherWorktreesOrProjects(t *testing.T) {
	t.Parallel()
	d, _ := bucketOrphanDaemon(t)
	repo := serviceProject(t, "bo-rule", `objects = "1"`, "")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	kept := repo.AddWorktree("kept")
	d.MustRunCLI(kept.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	// A Project named like bo-rule's Bucket "public", with a Worktree of
	// the same name.
	other := serviceProject(t, "bo-rule-public", `objects = "1"`, "")
	d.MustRunCLI(other.Dir, "link")
	otherWt := other.AddWorktree("feature")
	d.MustRunCLI(otherWt.Dir, "worktree", "ready")
	// Buckets the Project declared (or declares no longer).
	createBuckets(t, d, "bo-rule-uploads", "bo-rule-uploads--kept", "bo-rule-uploads--feature",
		// Not the Project's.
		"elsewhere--feature", "bo-rules--feature")

	repo.Git("worktree", "remove", wt.Dir)

	waitForOrphans(t, d, "bo-rule--feature", "bo-rule-uploads--feature")
	o := orphan(t, d, "bo-rule-uploads--feature")
	if o.Kind != "bucket" || o.Project != "bo-rule" || o.Worktree != "feature" || o.Service != "objects:1" || !o.Since.Equal(orphanEpoch) {
		t.Errorf("Orphan = %+v, want the bucket of Worktree feature of bo-rule in objects:1 since %v", o, orphanEpoch)
	}
	assertBuckets(t, d, "bo-rule", "bo-rule--kept", "bo-rule--feature", "bo-rule-uploads", "bo-rule-uploads--kept",
		"bo-rule-uploads--feature", "elsewhere--feature", "bo-rules--feature", "bo-rule-public", "bo-rule-public--feature")
}

func TestOrphanedBucketsAreDropped24HoursAfterTheyWereMadeWithWhatIsInThem(t *testing.T) {
	t.Parallel()
	d, clk := bucketOrphanDaemon(t)
	repo := serviceProject(t, "bo-expire", `objects = "1"`, "[database]\nengine = \"postgres\"\n\n")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	s3(t, d, "PUT", "/bo-expire--feature/photos/cat.jpg", "meow")
	repo.Git("worktree", "remove", wt.Dir)
	waitForOrphans(t, d, "bo-expire--feature", "bo_expire__feature")

	clk.Advance(daemon.OrphanGrace - time.Minute)
	if got := orphanNames(t, d); len(got) != 2 {
		t.Fatalf("a minute before the grace period ends the Orphans are %v, want both", got)
	}
	clk.Advance(time.Minute)

	waitForOrphans(t, d)
	assertBuckets(t, d, "bo-expire")
	if got := postgresDatabases(t, d, 5432); slices.Contains(got, "bo_expire__feature") || !slices.Contains(got, "bo_expire") {
		t.Fatalf("after the grace period the databases are %v, want the Worktree's dropped and the main one kept", got)
	}
}

func TestAWorktreeRemovedBeforeItHadABucketLeavesNoOrphanBucket(t *testing.T) {
	t.Parallel()
	d, _ := bucketOrphanDaemon(t)
	repo := serviceProject(t, "bo-early", `objects = "1"`, "")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	// Its setup never gets as far as creating its Bucket.
	d.MustRunCLI(repo.Dir, "services", "down")
	release := d.Engine.HoldStarts()
	wt := repo.AddWorktree("feature")
	waitForWorktree(t, d, "bo-early", "feature")

	repo.Git("worktree", "remove", "--force", wt.Dir)
	waitForNoWorktree(t, d, "bo-early", "feature")
	release()

	waitForOrphans(t, d)
}

func TestGcDropsOrphanedBucketsByHandAndSaysTheyAreBuckets(t *testing.T) {
	t.Parallel()
	d, _ := bucketOrphanDaemon(t)
	repo := serviceProject(t, "bo-gc", `objects = "1"`, "")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	kept := repo.AddWorktree("kept")
	d.MustRunCLI(kept.Dir, "worktree", "ready")
	gone := repo.AddWorktree("gone")
	d.MustRunCLI(gone.Dir, "worktree", "ready")
	repo.Git("worktree", "remove", gone.Dir)
	waitForOrphans(t, d, "bo-gc--gone")

	res := d.RunCLIInTerminal(repo.Dir, "", "gc")

	if res.ExitCode == 0 {
		t.Fatalf("gc without an answer exited 0:\n%s", res.Output())
	}
	assertLineWith(t, res.Stdout, "bo-gc--gone", "objects:1", "bo-gc", "gone", "bucket")
	assertContains(t, res.Stdout, "These Buckets will be dropped")
	assertContains(t, res.Stdout, "Drop these 1 Buckets? [y/N]")
	assertBuckets(t, d, "bo-gc", "bo-gc--gone", "bo-gc--kept")

	res = d.MustRunCLI(repo.Dir, "gc")

	assertContains(t, res.Stdout, "Dropped bo-gc--gone (objects:1)")
	waitForOrphans(t, d)
	assertBuckets(t, d, "bo-gc", "bo-gc--kept")
}

func TestUnlinkTurnsAllTheProjectsBucketsIntoOrphansAndLinkingAgainTakesThemBack(t *testing.T) {
	t.Parallel()
	d, clk := bucketOrphanDaemon(t)
	repo := serviceProject(t, "bo-relink", `objects = "1"`, "")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	s3(t, d, "PUT", "/bo-relink/kept.txt", "still here")
	// Buckets the Project declares.
	createBuckets(t, d, "bo-relink-uploads", "bo-relink-uploads--feature")

	res := d.MustRunCLI(repo.Dir, "unlink")

	assertContains(t, res.Stdout, "Its Buckets are Orphans now")
	assertLineWith(t, res.Stdout, "bo-relink--feature", "objects:1", "bucket")
	waitForOrphans(t, d, "bo-relink", "bo-relink--feature", "bo-relink-uploads", "bo-relink-uploads--feature")
	clk.Advance(time.Hour)

	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	waitForOrphans(t, d)
	clk.Advance(daemon.OrphanGrace)
	assertBuckets(t, d, "bo-relink", "bo-relink--feature", "bo-relink-uploads", "bo-relink-uploads--feature")
	if status, body := s3(t, d, "GET", "/bo-relink/kept.txt", ""); status != 200 || body != "still here" {
		t.Fatalf("after linking again the main Worktree's object answered %d %q, want it kept", status, body)
	}
}
