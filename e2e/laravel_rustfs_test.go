package e2e

import (
	"net/http"
	"path/filepath"
	"testing"
)

// laravelS3EnvExample is the part of a fresh Laravel app's .env.example
// that configures its filesystem disks.
const laravelS3EnvExample = `
FILESYSTEM_DISK=local

AWS_ACCESS_KEY_ID=
AWS_SECRET_ACCESS_KEY=
AWS_DEFAULT_REGION=us-east-1
AWS_BUCKET=
AWS_USE_PATH_STYLE_ENDPOINT=false
`

func TestALaravelAppDeclaringRustFSHasItsS3DiskPointedAtEachWorktreesBucket(t *testing.T) {
	t.Parallel()
	d := rustfsDaemon(t, "lrfs")
	laravelStubs(t, d)
	repo := laravelRepo(t, "lara-rfs").
		WriteFile(".env.example", laravelEnvExample+laravelS3EnvExample).
		WriteFile("composer.json", `{"require": {"php": "^8.2", "league/flysystem-aws-s3-v3": "^3.0"}}`+"\n").
		WriteFile("laneway.toml", `[services]
rustfs = "1"

[buckets.public]
public = true

[env]
AWS_PUBLIC_BUCKET = "{rustfs_bucket_public}"
`).
		Commit("store uploads in S3")

	out := d.MustRunCLI(repo.Dir, "link", "--yes").Stdout
	assertContains(t, out, "Fetched Plugin rustfs ")
	d.MustRunCLI(repo.Dir, "worktree", "ready")
	wt := repo.AddWorktree("feature")
	d.MustRunCLI(wt.Dir, "worktree", "ready")

	for _, w := range []struct{ dir, bucket, public string }{
		{repo.Dir, "lara-rfs", "lara-rfs-public"},
		{wt.Dir, "lara-rfs--feature", "lara-rfs-public--feature"},
	} {
		env := filepath.Join(w.dir, ".env")
		for key, want := range map[string]string{
			"AWS_ACCESS_KEY_ID":           "laneway",
			"AWS_SECRET_ACCESS_KEY":       "laneway",
			"AWS_DEFAULT_REGION":          "us-east-1",
			"AWS_BUCKET":                  w.bucket,
			"AWS_ENDPOINT":                "https://s3.lrfs.localhost",
			"AWS_URL":                     "https://s3.lrfs.localhost/" + w.bucket,
			"AWS_USE_PATH_STYLE_ENDPOINT": "true",
			"AWS_PUBLIC_BUCKET":           w.public, // the Project's own wiring
			"FILESYSTEM_DISK":             "local",  // the Project's choice
		} {
			if got := envValue(t, env, key); got != want {
				t.Errorf("%s=%q in %s, want %q", key, got, env, want)
			}
		}
	}

	// AWS_URL is where the s3 disk's objects are, in the Worktree's own
	// Bucket.
	url := envValue(t, filepath.Join(wt.Dir, ".env"), "AWS_URL") + "/avatars/1.txt"
	if put := s3Request(t, d, "PUT", url, "an avatar\n"); put.StatusCode != http.StatusOK {
		t.Fatalf("PUT %s answered %d:\n%s", url, put.StatusCode, put.Body)
	}
	if get := s3Request(t, d, "GET", url, ""); get.StatusCode != http.StatusOK || get.Body != "an avatar\n" {
		t.Fatalf("GET %s answered %d %q, want the object just put", url, get.StatusCode, get.Body)
	}
	main := envValue(t, filepath.Join(repo.Dir, ".env"), "AWS_URL") + "/avatars/1.txt"
	if get := s3Request(t, d, "GET", main, ""); get.StatusCode != http.StatusNotFound {
		t.Errorf("GET %s answered %d, want 404: the object is in the other Worktree's Bucket", main, get.StatusCode)
	}
}

// flysystemWarning is how the Laravel Recipe's warning about the package
// Laravel's s3 disk needs begins.
const flysystemWarning = "composer.json doesn't require league/flysystem-aws-s3-v3"

func TestLinkingALaravelAppDeclaringRustFSWithoutTheS3PackageWarnsAboutIt(t *testing.T) {
	t.Parallel()
	d := rustfsDaemon(t, "lrfs-warn")
	laravelStubs(t, d)
	repo := laravelRepo(t, "lara-rfs-warn").
		WriteFile("laneway.toml", "[services]\nrustfs = \"1\"\n").
		Commit("store uploads in S3")

	preview := d.RunCLIWithInput(repo.Dir, "n\n", "link")
	res := d.MustRunCLI(repo.Dir, "link", "--yes")
	doctor := d.RunCLI(repo.Dir, "doctor")

	assertContains(t, preview.Stderr, "lane link: warning: ")
	assertContains(t, preview.Stderr, flysystemWarning)
	assertContains(t, res.Stderr, "lane link: warning: ")
	assertContains(t, res.Stderr, flysystemWarning)
	assertContains(t, doctor.Stdout, flysystemWarning)
}
