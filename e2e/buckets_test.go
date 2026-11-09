package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lanewayhq/laneway/internal/container"
	"github.com/lanewayhq/laneway/internal/container/containertest"
	"github.com/lanewayhq/laneway/internal/gitfixture"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// Buckets through Seam 1: the objects test Plugin
// (internal/testkit/testplugins/objects) adds an object storage Engine with
// Bucket commands, and the fake container engine runs its image with the
// fake object storage server, which keeps Buckets in the container's volume.
// Tests see the Buckets the way any S3 client would: over the Service's
// port.

// objectsImage is the objects Engine's image.
const objectsImage = "example.test/objects"

// objectsPort is the objects Engine's port.
const objectsPort = 7490

// objectsDaemon is a daemon with the objects Plugin added, whose fake
// container engine can run the objects Engine's image.
func objectsDaemon(t *testing.T, opts ...testkit.Option) *testkit.Daemon {
	t.Helper()
	d := testkit.StartDaemon(t, opts...)
	d.Engine.Serve(objectsImage, containertest.ObjectStore)
	d.Engine.AnswerCommands(objectsImage, containertest.ObjectStoreCommands)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", testkit.Plugin(t, "objects"))
	return d
}

// s3 sends a signed request to the objects Service, as an S3 client would,
// and returns the answer's status and body.
func s3(t *testing.T, d *testkit.Daemon, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, fmt.Sprintf("http://%s:%d%s", d.ServiceHost(), objectsPort, path), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=laneway/20261109/us-east-1/s3/aws4_request")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b)
}

// buckets are the Buckets in the objects Service, sorted.
func buckets(t *testing.T, d *testkit.Daemon) []string {
	t.Helper()
	status, body := s3(t, d, "GET", "/", "")
	if status != http.StatusOK {
		t.Fatalf("listing Buckets answered %d: %s", status, body)
	}
	var names []string
	for _, m := range regexp.MustCompile(`<Name>([^<]*)</Name>`).FindAllStringSubmatch(body, -1) {
		names = append(names, m[1])
	}
	slices.Sort(names)
	return names
}

func assertBuckets(t *testing.T, d *testkit.Daemon, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := buckets(t, d); !slices.Equal(got, want) {
		t.Fatalf("the objects Service has the Buckets %q, want %q", got, want)
	}
}

func TestEachWorktreeGetsAPrivateBucketOfItsOwnWhilePreparing(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	repo := serviceProject(t, "bk-shop", `objects = "1"`, "")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	assertBuckets(t, d, "bk-shop")

	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertBuckets(t, d, "bk-shop", "bk-shop--feature")
	if status, _ := s3(t, d, "PUT", "/bk-shop--feature/photos/cat.jpg", "meow"); status != http.StatusOK {
		t.Fatalf("putting an object in the Worktree's Bucket answered %d", status)
	}
	if status, body := s3(t, d, "GET", "/bk-shop--feature/photos/cat.jpg", ""); status != http.StatusOK || body != "meow" {
		t.Fatalf("getting the object back answered %d %q", status, body)
	}
	if status, _ := s3(t, d, "GET", "/bk-shop/photos/cat.jpg", ""); status != http.StatusNotFound {
		t.Fatalf("the main Worktree's Bucket answered %d for the other Worktree's object, want 404", status)
	}
	// Private: no one gets in without signing.
	res, err := http.Get(fmt.Sprintf("http://%s:%d/bk-shop--feature/photos/cat.jpg", d.ServiceHost(), objectsPort))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("an anonymous GET of a private Bucket's object answered %d, want 403", res.StatusCode)
	}
}

func TestAProjectWithADatabaseInAnotherEngineGetsBothItsDatabaseAndItsBucket(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	repo := serviceProject(t, "bk-db", `objects = "1"`, "[database]\nengine = \"postgres\"\n\n")
	d.MustRunCLI(repo.Dir, "link")

	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertBuckets(t, d, "bk-db", "bk-db--feature")
	assertAt(t, infoJSON(t, d, wt.Dir), "bk_db__feature", "database", "name")
}

func TestAFailedBucketCommandFailsPreparingWithItsOutputAndRetryingMakesTheBucket(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	var broken atomic.Bool
	broken.Store(true)
	d.Engine.AnswerCommands(objectsImage, func(ctx context.Context, spec container.Spec, data *containertest.Volume, cmd []string) container.Output {
		if broken.Load() && strings.Contains(strings.Join(cmd, " "), "-X PUT") {
			return container.Output{ExitCode: 22, Stderr: []byte("<Error><Code>XMinioStorageFull</Code></Error>\n")}
		}
		return containertest.ObjectStoreCommands(ctx, spec, data, cmd)
	})
	repo := serviceProject(t, "bk-fail", `objects = "1"`, "")
	d.MustRunCLI(repo.Dir, "link")

	res := d.RunCLI(repo.Dir, "worktree", "ready")

	if res.ExitCode != 1 {
		t.Fatalf("worktree ready exited %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, `"bucket bk-fail"`)
	assertContains(t, res.Stderr, "objects's create command exited with status 22")
	assertContains(t, res.Stderr, "XMinioStorageFull")
	assertContains(t, res.Stderr, "--retry")
	assertSetupState(t, d, repo.Dir, "failed")
	assertBuckets(t, d)

	broken.Store(false)
	d.MustRunCLI(repo.Dir, "worktree", "ready", "--retry")

	assertSetupState(t, d, repo.Dir, "ready")
	assertBuckets(t, d, "bk-fail")
}

func TestPreparingAWorktreeAgainLeavesItsBucketAsItIs(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	marker := filepath.Join(gitfixture.TempDir(t), "fixed")
	repo := serviceProject(t, "bk-again", `objects = "1"`, "[worktree]\non_create = "+tomlList("test -e "+marker)+"\n\n")
	d.MustRunCLI(repo.Dir, "link")
	if res := d.RunCLI(repo.Dir, "worktree", "ready"); res.ExitCode != 1 {
		t.Fatalf("worktree ready exited %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	assertBuckets(t, d, "bk-again")
	if status, _ := s3(t, d, "PUT", "/bk-again/kept.txt", "still here"); status != http.StatusOK {
		t.Fatalf("putting an object answered %d", status)
	}

	writeTestFile(t, marker, "")
	d.MustRunCLI(repo.Dir, "worktree", "ready", "--retry")

	if status, body := s3(t, d, "GET", "/bk-again/kept.txt", ""); status != http.StatusOK || body != "still here" {
		t.Fatalf("after Preparing again the object answered %d %q, want it kept", status, body)
	}
}

func TestTheBucketVariableHoldsEachWorktreesOwnBucket(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	repo := serviceProject(t, "bk-env", `objects = "1"`, "[env]\nAWS_BUCKET = \"{objects_bucket}\"\n\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertContains(t, readFile(t, filepath.Join(repo.Dir, ".env")), "AWS_BUCKET=bk-env\n")
	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "AWS_BUCKET=bk-env--feature\n")
}

func TestInfoListsTheWorktreesBuckets(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	repo := serviceProject(t, "bk-info", `objects = "1"`, "")
	d.MustRunCLI(repo.Dir, "link")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	info := infoJSON(t, d, wt.Dir)
	assertLen(t, info, 1, "buckets")
	assertAt(t, info, "bk-info--feature", "buckets", 0, "name")
	assertAt(t, info, "objects:1", "buckets", 0, "service")
	assertAt(t, info, false, "buckets", 0, "public")

	out := d.MustRunCLI(wt.Dir, "info").Stdout
	assertLineWith(t, out, "Buckets", "bk-info--feature", "on", "objects:1")
}

func TestDeclaringObjectStorageInAReadyProjectAndLinkingAgainMakesItsBuckets(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	repo := gitfixture.New(t, "bk-later").WriteFile("README.md", "a shop\n").Commit("a shop")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	// The objects Plugin is installed already, so Linking fetches nothing.
	declared := "[services]\nobjects = \"1\"\n\n[buckets.public]\npublic = true\n"
	repo.WriteFile("laneway.toml", declared)
	wt.WriteFile("laneway.toml", declared)
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertBuckets(t, d, "bk-later", "bk-later--feature", "bk-later-public", "bk-later-public--feature")
	assertOrigins(t, d, "bk-later-public--feature", "https://feature.bk-later.localhost")
	if status, _ := anonymousGet(t, d, "/bk-later-public--feature/missing.txt"); status != http.StatusNotFound {
		t.Fatalf("an anonymous GET in the public Bucket answered %d, want 404: it may be read", status)
	}
}

func TestDeclaringAnotherBucketInAReadyProjectAndLinkingAgainMakesIt(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	repo := serviceProject(t, "bk-more", `objects = "1"`, "")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	assertBuckets(t, d, "bk-more")

	repo.WriteFile("laneway.toml", "[services]\nobjects = \"1\"\n\n[buckets.exports]\n")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")

	assertBuckets(t, d, "bk-more", "bk-more-exports")
	assertOrigins(t, d, "bk-more-exports", "https://bk-more.localhost")
}
