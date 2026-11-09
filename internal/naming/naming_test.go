package naming_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/naming"
)

// validBucket is what S3, and RustFS, accept as a Bucket name: lowercase
// letters, digits and -, 3 to 63 characters, starting and ending with a
// letter or digit (and not starting with xn--, checked separately).
var validBucket = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

func assertValidBucket(t *testing.T, name string) {
	t.Helper()
	if !validBucket.MatchString(name) || strings.HasPrefix(name, "xn--") {
		t.Fatalf("%q isn't a valid Bucket name", name)
	}
}

func TestABucketIsNamedAfterItsProjectAndWorktree(t *testing.T) {
	tests := []struct {
		project, worktree, suffix, want string
	}{
		{"Shop", "", "", "shop"},
		{"shop", "feature-x", "", "shop--feature-x"},
		{"My Shop", "feature-x", "", "my-shop--feature-x"},
		// A declared Bucket's name goes after the Project's, before the
		// Worktree's.
		{"shop", "", "public", "shop-public"},
		{"shop", "feature-x", "public", "shop-public--feature-x"},
		{"shop", "feature-x", "user_uploads", "shop-user-uploads--feature-x"},
	}
	for _, tt := range tests {
		got := naming.BucketName(tt.project, tt.worktree, tt.suffix)
		if got != tt.want {
			t.Errorf("BucketName(%q, %q, %q) = %q, want %q", tt.project, tt.worktree, tt.suffix, got, tt.want)
		}
		assertValidBucket(t, got)
	}
}

func TestABucketNameTooLongIsCutShortWithAHash(t *testing.T) {
	long := "the-very-long-project-name-that-goes-on-and-on-and-on-and-on-and-on"
	main := naming.BucketName(long, "", "")
	if len(main) > 63 || !regexp.MustCompile(`^the-very-long-project-name-.*-[0-9a-f]{8}$`).MatchString(main) {
		t.Fatalf("BucketName of a %d-character Project = %q, want it cut to 63 with a hash", len(long), main)
	}
	assertValidBucket(t, main)

	a := naming.BucketName("shop", "add-the-very-long-feature-that-needs-a-long-branch-name-one", "")
	b := naming.BucketName("shop", "add-the-very-long-feature-that-needs-a-long-branch-name-two", "")
	if a == b {
		t.Fatalf("two long Worktree names alike up to the cut share the Bucket %q", a)
	}
	for _, name := range []string{a, b} {
		assertValidBucket(t, name)
		if !strings.HasPrefix(name, "shop--add-the-very-long-feature") {
			t.Errorf("%q doesn't start with the Project's and Worktree's names", name)
		}
	}
}

func TestABucketNameIsMadeFromNonASCIINames(t *testing.T) {
	if got := naming.BucketName("Café Ünïcode", "", ""); got != "caf-n-code" {
		t.Errorf("BucketName of a partly non-ASCII Project = %q, want caf-n-code", got)
	}
	a, b := naming.BucketName("日本語", "", ""), naming.BucketName("中文", "", "")
	if a == b {
		t.Fatalf("two wholly non-ASCII Projects share the Bucket %q", a)
	}
	assertValidBucket(t, a)
	assertValidBucket(t, b)
	assertValidBucket(t, naming.BucketName("日本語", "機能", ""))
}

func TestABucketNameIsLongEnoughAndNeverStartsWithXN(t *testing.T) {
	for _, project := range []string{"a", "ab", "-a-", "xn", "Xn"} {
		for _, worktree := range []string{"", "feature"} {
			assertValidBucket(t, naming.BucketName(project, worktree, ""))
		}
	}
	if a, b := naming.BucketName("a", "", ""), naming.BucketName("b", "", ""); a == b || !strings.HasPrefix(a, "a-") {
		t.Fatalf("Projects a and b have the Buckets %q and %q, want each its own, starting with its name", a, b)
	}
}

// Each Worktree's private Bucket is its own, even when its name and its
// Project's run together; and within a Project, so is each declared one.
func TestCollidingProjectAndWorktreeNamesGetBucketsOfTheirOwn(t *testing.T) {
	assertDistinct := func(names []struct{ project, worktree, suffix string }) {
		t.Helper()
		seen := map[string]int{}
		for i, n := range names {
			got := naming.BucketName(n.project, n.worktree, n.suffix)
			if j, ok := seen[got]; ok {
				t.Errorf("%+v and %+v share the Bucket %q", names[j], n, got)
			}
			seen[got] = i
		}
	}
	assertDistinct([]struct{ project, worktree, suffix string }{
		{"a", "", ""},
		{"a-b", "", ""},
		{"a", "b", ""},
		{"a-b", "c", ""},
		{"a", "b-c", ""},
		{"a", "b--c", ""},
		{"a--b", "c-d", ""},
		{"a-b-c", "d", ""},
	})
	assertDistinct([]struct{ project, worktree, suffix string }{
		{"a", "", ""},
		{"a", "b", ""},
		{"a", "b-c", ""},
		{"a", "c", ""},
		{"a", "", "b"},
		{"a", "", "b_c"},
		{"a", "c", "b"},
		{"a", "b", "c"},
		{"a", "c", "b_c"},
		{"a", "b-c", "c"},
		{"a", "public", ""},
		{"a", "", "public"},
	})
}

// Projects whose names would share their main Worktree's Bucket clash, as
// those sharing a Domain or database do.
func TestProjectsWhoseBucketsWouldBeTheSameClash(t *testing.T) {
	if naming.BucketName("my-app", "", "") != naming.BucketName("my--app", "", "") {
		t.Fatal("my-app and my--app don't share a Bucket, so this test tests nothing")
	}
	if !naming.Clash("my-app", "my--app") {
		t.Error("my-app and my--app don't clash, but would share their main Worktree's Bucket")
	}
	if naming.Clash("my-app", "my-shop") {
		t.Error("my-app and my-shop clash")
	}
}
