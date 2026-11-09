package e2e

import (
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/daemon"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// The rustfs Plugin is the first-party one, at the version go.mod pins:
// test daemons fetch it from their test Catalog, and the fake container
// engine runs its image as a fake object store with a fake console.

// rustfsDaemon is a daemon whose Dashboard is at <dash>.localhost, with
// opts applied after. Use a dash of the test's own.
func rustfsDaemon(t *testing.T, dash string, opts ...testkit.Option) *testkit.Daemon {
	t.Helper()
	opts = append([]testkit.Option{func(c *daemon.Config) { c.DashboardDomain = dash + ".localhost" }}, opts...)
	return testkit.StartDaemon(t, opts...)
}

// s3Request sends a request signed with the shared credentials (AWS
// Signature Version 4, as an S3 client signs it) to url, through the
// daemon's HTTPS proxy, with header (name, value pairs), and returns the
// answer.
func s3Request(t *testing.T, d *testkit.Daemon, method, url, body string, header ...string) testkit.Response {
	t.Helper()
	req := signedS3Request(t, method, url, body, "laneway", "laneway", time.Now(), header...)
	res, err := d.HTTPS().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return readResponse(t, res)
}

// bucketNames are the <Name>s of a ListAllMyBucketsResult, sorted.
func bucketNames(body string) []string {
	var names []string
	for _, m := range regexp.MustCompile(`<Name>([^<]*)</Name>`).FindAllStringSubmatch(body, -1) {
		names = append(names, m[1])
	}
	slices.Sort(names)
	return names
}

func TestAProjectDeclaringRustFSGetsABucketPerWorktreeServedAtTheS3ServiceDomain(t *testing.T) {
	t.Parallel()
	d := rustfsDaemon(t, "rfs-fast")
	repo := serviceProject(t, "rfs-shop", `rustfs = "1"`,
		"[env]\nAWS_ENDPOINT = \"{rustfs_endpoint}\"\nAWS_BUCKET = \"{rustfs_bucket}\"\n\n")

	res := d.MustRunCLI(repo.Dir, "link", "--yes")
	assertContains(t, res.Stdout, "Fetched Plugin rustfs ")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "AWS_ENDPOINT=https://s3.rfs-fast.localhost\n")
	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "AWS_BUCKET=rfs-shop--feature\n")
	list := s3Request(t, d, "GET", "https://s3.rfs-fast.localhost/", "")
	if got := bucketNames(list.Body); list.StatusCode != http.StatusOK || !slices.Equal(got, []string{"rfs-shop", "rfs-shop--feature"}) {
		t.Fatalf("listing Buckets at the s3 Service Domain answered %d with %q, want each Worktree's Bucket:\n%s", list.StatusCode, got, list.Body)
	}
	console := d.Get("https://rustfs.rfs-fast.localhost/rustfs/console/")
	if console.StatusCode != http.StatusOK {
		t.Fatalf("the console at the rustfs Service Domain answered %d:\n%s", console.StatusCode, console.Body)
	}
	assertContains(t, console.Body, "RustFS")
}
