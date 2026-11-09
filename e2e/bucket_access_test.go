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
	"github.com/lanewayhq/laneway/internal/testkit"
)

// Who can reach a Bucket, and from where (lanewayhq/laneway#97), through
// the fake object storage server (see buckets_test.go), which keeps each
// Bucket's policy and CORS configuration as the Engine's configure command
// set them and answers anonymous reads and CORS preflights by them.

// bucketOrigins are the origins the Bucket name's CORS configuration
// allows, in the order it lists them; none if it has none.
func bucketOrigins(t *testing.T, d *testkit.Daemon, name string) []string {
	t.Helper()
	status, body := s3(t, d, "GET", "/"+name+"?cors", "")
	switch status {
	case http.StatusNotFound:
		assertContains(t, body, "NoSuchCORSConfiguration")
		return nil
	case http.StatusOK:
	default:
		t.Fatalf("getting the CORS configuration of %s answered %d: %s", name, status, body)
	}
	var origins []string
	for _, m := range regexp.MustCompile(`<AllowedOrigin>([^<]*)</AllowedOrigin>`).FindAllStringSubmatch(body, -1) {
		origins = append(origins, m[1])
	}
	return origins
}

func assertOrigins(t *testing.T, d *testkit.Daemon, name string, want ...string) {
	t.Helper()
	if got := bucketOrigins(t, d, name); !slices.Equal(got, want) {
		t.Fatalf("the Bucket %s allows CORS from %q, want %q", name, got, want)
	}
}

// waitForOrigins waits for the Bucket name to allow CORS from exactly
// want, which Laneway sets in the background.
func waitForOrigins(t *testing.T, d *testkit.Daemon, name string, want ...string) {
	t.Helper()
	var got []string
	eventually(t, fmt.Sprintf("the Bucket %s to allow CORS from %q", name, want), func() bool {
		got = bucketOrigins(t, d, name)
		return slices.Equal(got, want)
	})
}

// preflight sends a browser's CORS preflight for a PUT to path from
// origin, as a page uploading straight to the Bucket would, and returns the
// answer's status and Access-Control-Allow-Origin.
func preflight(t *testing.T, d *testkit.Daemon, path, origin string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("OPTIONS", fmt.Sprintf("http://%s:%d%s", d.ServiceHost(), objectsPort, path), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", "PUT")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode, res.Header.Get("Access-Control-Allow-Origin")
}

func TestEachBucketAllowsBrowserRequestsFromItsOwnWorktreesDomainOnly(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	repo := serviceProject(t, "ba-cors", `objects = "1"`, "")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertOrigins(t, d, "ba-cors", "https://ba-cors.localhost")
	assertOrigins(t, d, "ba-cors--feature", "https://feature.ba-cors.localhost")
	if status, allowed := preflight(t, d, "/ba-cors--feature/avatars/1.png", "https://feature.ba-cors.localhost"); status != http.StatusOK || allowed != "https://feature.ba-cors.localhost" {
		t.Fatalf("a preflight from the Worktree's Domain answered %d allowing %q, want 200 allowing it", status, allowed)
	}
	if status, allowed := preflight(t, d, "/ba-cors/avatars/1.png", "https://feature.ba-cors.localhost"); status != http.StatusForbidden || allowed != "" {
		t.Fatalf("a preflight from another Worktree's Domain answered %d allowing %q, want 403", status, allowed)
	}
}

// anonymousGet gets path from the objects Service without signing, as a
// browser following a public URL would, and returns the answer's status and
// body.
func anonymousGet(t *testing.T, d *testkit.Daemon, path string) (int, string) {
	t.Helper()
	res, err := http.Get(fmt.Sprintf("http://%s:%d%s", d.ServiceHost(), objectsPort, path))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(body)
}

// declaredBuckets declares a public Bucket, public, and a private one,
// exports, which says nothing about public.
const declaredBuckets = "[buckets.public]\npublic = true\n\n[buckets.exports]\n\n"

func TestEachWorktreeGetsEachBucketItsProjectDeclaresAndOnlyPublicOnesAreReadableByAnyone(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	repo := serviceProject(t, "ba-decl", `objects = "1"`, declaredBuckets)
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertBuckets(t, d, "ba-decl", "ba-decl-exports", "ba-decl-public",
		"ba-decl--feature", "ba-decl-exports--feature", "ba-decl-public--feature")
	for _, b := range []string{"ba-decl-public", "ba-decl-public--feature", "ba-decl-exports--feature", "ba-decl--feature"} {
		putObject(t, d, "/"+b+"/logo.svg", "<svg/>")
	}
	for _, b := range []string{"ba-decl-public", "ba-decl-public--feature"} {
		if status, body := anonymousGet(t, d, "/"+b+"/logo.svg"); status != http.StatusOK || body != "<svg/>" {
			t.Errorf("an anonymous GET of the public Bucket %s's object answered %d %q, want 200", b, status, body)
		}
		// Reading objects, not listing them.
		if status, _ := anonymousGet(t, d, "/"+b); status != http.StatusForbidden {
			t.Errorf("an anonymous listing of the public Bucket %s answered %d, want 403", b, status)
		}
	}
	for _, b := range []string{"ba-decl-exports--feature", "ba-decl--feature"} {
		if status, _ := anonymousGet(t, d, "/"+b+"/logo.svg"); status != http.StatusForbidden {
			t.Errorf("an anonymous GET of the private Bucket %s's object answered %d, want 403", b, status)
		}
	}
	// A declared Bucket allows CORS from its Worktree's Domains too.
	assertOrigins(t, d, "ba-decl-public--feature", "https://feature.ba-decl.localhost")
	assertOrigins(t, d, "ba-decl-exports", "https://ba-decl.localhost")
}

func TestAddingOrRemovingAnAliasChangesTheOriginsTheWorktreesBucketsAllow(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	repo := serviceProject(t, "ba-alias", `objects = "1"`, "")
	d.MustRunCLI(repo.Dir, "link")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")
	waitForOrigins(t, d, "ba-alias--feature", "https://feature.ba-alias.localhost", "https://checkout.ba-alias.localhost")
	d.MustRunCLI(wt.Dir, "worktree", "alias", "basket")
	waitForOrigins(t, d, "ba-alias--feature", "https://feature.ba-alias.localhost",
		"https://basket.ba-alias.localhost", "https://checkout.ba-alias.localhost")

	// Not the newest Alias, so the Primary Domain stays basket's.
	d.MustRunCLI(wt.Dir, "worktree", "alias", "--remove", "checkout")

	waitForOrigins(t, d, "ba-alias--feature", "https://feature.ba-alias.localhost", "https://basket.ba-alias.localhost")
	if status, _ := preflight(t, d, "/ba-alias--feature/a.png", "https://checkout.ba-alias.localhost"); status != http.StatusForbidden {
		t.Fatalf("a preflight from the removed Alias answered %d, want 403", status)
	}
}

func TestANewWorktreesDeclaredBucketStartsAsACopyOfTheMainWorktreesAndStaysPublic(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	repo := serviceProject(t, "ba-copy", `objects = "1"`, declaredBuckets)
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	putObject(t, d, "/ba-copy-public/logo.svg", "<svg/>")
	putObject(t, d, "/ba-copy-exports/2026.csv", "id,total")
	putObject(t, d, "/ba-copy/invoice.pdf", "private")

	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertObject(t, d, "/ba-copy-public--feature/logo.svg", "<svg/>")
	assertObject(t, d, "/ba-copy-exports--feature/2026.csv", "id,total")
	assertEmptyBucketBut(t, d, "ba-copy-public--feature", "logo.svg")
	if status, body := anonymousGet(t, d, "/ba-copy-public--feature/logo.svg"); status != http.StatusOK || body != "<svg/>" {
		t.Fatalf("an anonymous GET of the copied public Bucket's object answered %d %q, want 200", status, body)
	}
	assertOrigins(t, d, "ba-copy-public--feature", "https://feature.ba-copy.localhost")
}

// assertEmptyBucketBut fails the test unless the Bucket name holds exactly
// the objects keys.
func assertEmptyBucketBut(t *testing.T, d *testkit.Daemon, name string, keys ...string) {
	t.Helper()
	if got := objectKeys(t, d, name); !slices.Equal(got, keys) {
		t.Fatalf("the Bucket %s holds %q, want %q", name, got, keys)
	}
}

func TestEachDeclaredBucketsNameIsAVariableEvenWhenAnotherConfigFileDeclaresIt(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	repo := serviceProject(t, "ba-env", `objects = "1"`,
		"[env]\nAWS_BUCKET = \"{objects_bucket}\"\nPUBLIC_BUCKET = \"{objects_bucket_user_uploads}\"\n\n").
		WriteFile("laneway.local.toml", "[buckets.user_uploads]\npublic = true\n")
	d.MustRunCLI(repo.Dir, "link", "--yes")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	writeTestFile(t, filepath.Join(wt.Dir, "laneway.local.toml"), "[buckets.user_uploads]\npublic = true\n")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	assertContains(t, readFile(t, filepath.Join(repo.Dir, ".env")), "AWS_BUCKET=ba-env\nPUBLIC_BUCKET=ba-env-user-uploads\n")
	assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "PUBLIC_BUCKET=ba-env-user-uploads--feature\n")
	assertBuckets(t, d, "ba-env", "ba-env-user-uploads", "ba-env--feature", "ba-env-user-uploads--feature")
}

func TestInfoSaysWhichOfTheWorktreesBucketsArePublic(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	repo := serviceProject(t, "ba-info", `objects = "1"`, declaredBuckets)
	d.MustRunCLI(repo.Dir, "link")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	info := infoJSON(t, d, wt.Dir)
	assertLen(t, info, 3, "buckets")
	for i, want := range []struct {
		name   string
		public bool
	}{{"ba-info--feature", false}, {"ba-info-exports--feature", false}, {"ba-info-public--feature", true}} {
		assertAt(t, info, want.name, "buckets", i, "name")
		assertAt(t, info, "objects:1", "buckets", i, "service")
		assertAt(t, info, want.public, "buckets", i, "public")
	}
	out := d.MustRunCLI(wt.Dir, "info").Stdout
	assertLineWith(t, out, "ba-info-public--feature", "on", "objects:1", "(public)")
}

func TestARemovedWorktreesDeclaredBucketsBecomeOrphansEvenWithNamesCutShort(t *testing.T) {
	t.Parallel()
	d, _ := bucketOrphanDaemon(t)
	repo := serviceProject(t, "ba-orphans", `objects = "1"`, "[buckets.user_uploads]\n\n")
	d.MustRunCLI(repo.Dir, "link")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	// Too long for its declared Bucket's name, which is cut short with a hash.
	wt := repo.AddWorktree("a-worktree-whose-name-is-far-too-long-for-a-bucket")
	d.MustRunCLI(wt.Dir, "worktree", "ready")
	info := infoJSON(t, d, wt.Dir)
	private, _ := at(t, info, "buckets", 0, "name").(string)
	declared, _ := at(t, info, "buckets", 1, "name").(string)
	if private == "" || declared == "" || strings.HasSuffix(declared, "-for-a-bucket") {
		t.Fatalf("the Worktree's Buckets are %q and %q, want the declared one cut short with a hash", private, declared)
	}
	assertBuckets(t, d, "ba-orphans", "ba-orphans-user-uploads", private, declared)

	repo.Git("worktree", "remove", wt.Dir)

	waitForOrphans(t, d, private, declared)
	if o := orphan(t, d, declared); o.Kind != "bucket" || o.Service != "objects:1" {
		t.Errorf("Orphan = %+v, want a bucket in objects:1", o)
	}
}

func TestAFailedConfigureFailsPreparingWithItsOutputAndRetryingConfiguresTheBucket(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	var broken atomic.Bool
	broken.Store(true)
	d.Engine.AnswerCommands(objectsImage, func(ctx context.Context, spec container.Spec, data *containertest.Volume, cmd []string) container.Output {
		if broken.Load() && len(cmd) == 7 && strings.Contains(cmd[2], "?policy") {
			return container.Output{ExitCode: 1, Stderr: []byte("<Error><Code>MalformedPolicy</Code></Error>\n")}
		}
		return containertest.ObjectStoreCommands(ctx, spec, data, cmd)
	})
	repo := serviceProject(t, "ba-fail", `objects = "1"`, declaredBuckets)
	d.MustRunCLI(repo.Dir, "link")

	res := d.RunCLI(repo.Dir, "worktree", "ready")

	if res.ExitCode != 1 {
		t.Fatalf("worktree ready exited %d, want 1:\n%s", res.ExitCode, res.Output())
	}
	assertContains(t, res.Stderr, `"bucket ba-fail"`)
	assertContains(t, res.Stderr, "objects's configure command exited with status 1")
	assertContains(t, res.Stderr, "MalformedPolicy")
	assertSetupState(t, d, repo.Dir, "failed")

	broken.Store(false)
	d.MustRunCLI(repo.Dir, "worktree", "ready", "--retry")

	assertSetupState(t, d, repo.Dir, "ready")
	putObject(t, d, "/ba-fail-public/logo.svg", "<svg/>")
	if status, _ := anonymousGet(t, d, "/ba-fail-public/logo.svg"); status != http.StatusOK {
		t.Fatalf("an anonymous GET of the public Bucket's object answered %d after retrying, want 200", status)
	}
	assertOrigins(t, d, "ba-fail", "https://ba-fail.localhost")
}

func TestDeclaredBucketsAreCheckedLikeTheRestOfTheConfig(t *testing.T) {
	t.Parallel()
	d := objectsDaemon(t)
	tests := []struct {
		name, config string
		want         []string
	}{
		{"ba-err-key", "[buckets.public]\npublic = true\ncors = [\"*\"]\n\n", []string{"buckets.public.cors", "its only key is public"}},
		{"ba-err-name", "[buckets.Public-Files]\n\n", []string{"buckets.Public-Files", "lowercase letters, digits and _"}},
		{"ba-err-public", "[buckets.public]\npublic = \"yes\"\n\n", []string{"buckets.public.public"}},
		{"ba-err-table", "buckets = \"public\"\n\n", []string{"buckets must be a table"}},
		{"ba-err-undeclared", "[env]\nPUBLIC_BUCKET = \"{objects_bucket_public}\"\n\n", []string{"env.PUBLIC_BUCKET", "{objects_bucket_public}"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := serviceProject(t, tt.name, `objects = "1"`, tt.config)

			res := d.RunCLI(repo.Dir, "link")

			if res.ExitCode == 0 {
				t.Fatalf("link succeeded:\n%s", res.Output())
			}
			for _, want := range append([]string{"laneway.toml"}, tt.want...) {
				assertContains(t, res.Stderr, want)
			}
		})
	}
}
