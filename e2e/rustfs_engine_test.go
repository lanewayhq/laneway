package e2e

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/clock"
	"github.com/lanewayhq/laneway/internal/container"
	"github.com/lanewayhq/laneway/internal/daemon"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// The slow tier for the rustfs Plugin: the first-party Plugin, at the
// version go.mod pins, against the RustFS image it maps "1" to, on Docker
// and Podman. Its clone and size commands fetch rc from GitHub, so these
// tests need the network.

// rustfsImage is the image the first-party rustfs Plugin runs at version
// "1", read from its manifest.
func rustfsImage(t *testing.T) string {
	t.Helper()
	engines, err := testkit.FirstPartyEngines()
	if err != nil {
		t.Fatal(err)
	}
	m, ok := engines.Lookup("rustfs")
	if !ok {
		t.Fatal("no first-party Plugin adds the rustfs Engine")
	}
	image, err := m.ImageRef("1")
	if err != nil {
		t.Fatal(err)
	}
	return image
}

// realRustFSDaemon is realEngineDaemon with its Dashboard at dash and the
// RustFS image pulled, so Preparing doesn't wait for a download. It returns
// the name of RustFS's container, removed when the test ends.
func realRustFSDaemon(t *testing.T, engine *container.Docker, dash string, opts ...testkit.Option) (*testkit.Daemon, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := engine.Pull(ctx, rustfsImage(t)); err != nil {
		t.Fatalf("pulling %s: %v", rustfsImage(t), err)
	}
	opts = append([]testkit.Option{func(c *daemon.Config) { c.DashboardDomain = dash }}, opts...)
	d, prefix := realEngineDaemon(t, engine, opts...)
	containerName := prefix + "-rustfs-1"
	removeContainer(t, engine, containerName)
	return d, containerName
}

// assertRunning fails the test unless the container name is running as
// want says.
func assertRunning(t *testing.T, engine *container.Docker, name string, want bool) {
	t.Helper()
	if _, running, err := engine.Running(context.Background(), name, 9000); err != nil || running != want {
		t.Fatalf("container %s running = %t, %v; want %t", name, running, err, want)
	}
}

// anonymousRequest sends an unsigned request to url through the daemon's
// HTTPS proxy, with header (name, value pairs), as a browser would.
func anonymousRequest(t *testing.T, d *testkit.Daemon, method, url string, header ...string) testkit.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := d.HTTPS().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return readResponse(t, res)
}

// realPreflight is a browser's CORS preflight for a PUT to url from origin;
// it returns the answer's status and Access-Control-Allow-Origin.
func realPreflight(t *testing.T, d *testkit.Daemon, url, origin string) (int, string) {
	t.Helper()
	res := anonymousRequest(t, d, "OPTIONS", url, "Origin", origin, "Access-Control-Request-Method", "PUT")
	return res.StatusCode, res.Header.Get("Access-Control-Allow-Origin")
}

// TestRustFSOnARealContainerEngine links a Project that declares RustFS and
// a public Bucket: Preparing starts RustFS and makes the main Worktree's
// Buckets; a new Worktree's are copies (made by rc on the server, metadata
// kept), only the public one is readable without signing, and CORS follows
// the Worktree's Aliases. The S3 API and the console are served at their
// Service Domains, which start RustFS, and a request signed for the s3
// Domain's host is accepted through the proxy. A removed Worktree's Buckets
// are Orphans, dropped after their grace period.
func TestRustFSOnARealContainerEngine(t *testing.T) {
	for name, engine := range realEngines(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewFake(orphanEpoch)
			dash := "dash-rfs-real-" + name + ".localhost"
			d, containerName := realRustFSDaemon(t, engine, dash, testkit.WithClock(clk))
			s3URL, consoleURL := "https://s3."+dash, "https://rustfs."+dash+"/rustfs/console/"
			project := "rfs-real-" + name
			repo := serviceProject(t, project, `rustfs = "1"`, declaredBuckets+
				"[env]\nAWS_ENDPOINT = \"{rustfs_endpoint}\"\nAWS_BUCKET = \"{rustfs_bucket}\"\nPUBLIC_BUCKET = \"{rustfs_bucket_public}\"\n\n")

			d.MustRunCLI(repo.Dir, "link", "--yes")
			if setup := waitForSetup(t, d, repo.Dir); setup.State != api.SetupReady {
				t.Fatalf("the main Worktree's setup is %+v, want Ready", setup)
			}
			assertRunning(t, engine, containerName, true)
			assertContains(t, readFile(t, filepath.Join(repo.Dir, ".env")), "AWS_ENDPOINT="+s3URL+"\n")
			for _, put := range []struct{ path, body string }{
				{"/" + project + "/invoices/1.txt", "private invoice"},
				{"/" + project + "-public/logo.svg", "<svg/>"},
			} {
				res := s3Request(t, d, "PUT", s3URL+put.path, put.body, "Content-Type", "text/plain", "X-Amz-Meta-Owner", "alice")
				if res.StatusCode != http.StatusOK {
					t.Fatalf("a signed PUT %s at the s3 Service Domain answered %d:\n%s", put.path, res.StatusCode, res.Body)
				}
			}

			wt := repo.AddWorktree("feature")
			if setup := waitForSetup(t, d, wt.Dir); setup.State != api.SetupReady {
				t.Fatalf("the Worktree's setup is %+v, want Ready", setup)
			}

			list := s3Request(t, d, "GET", s3URL+"/", "")
			want := []string{project, project + "--feature", project + "-exports", project + "-exports--feature", project + "-public", project + "-public--feature"}
			if got := bucketNames(list.Body); list.StatusCode != http.StatusOK || !slices.Equal(got, want) {
				t.Fatalf("RustFS has the Buckets %q (%d), want %q:\n%s", got, list.StatusCode, want, list.Body)
			}
			// RustFS checks the signatures it accepts through the proxy.
			forged := signedS3Request(t, "GET", s3URL+"/", "", "laneway", "not-the-password", time.Now())
			res, err := d.HTTPS().Do(forged)
			if err != nil {
				t.Fatal(err)
			}
			if res := readResponse(t, res); res.StatusCode != http.StatusForbidden {
				t.Fatalf("a request signed with the wrong password answered %d, want 403:\n%s", res.StatusCode, res.Body)
			}
			copied := s3Request(t, d, "GET", s3URL+"/"+project+"--feature/invoices/1.txt", "")
			if copied.StatusCode != http.StatusOK || copied.Body != "private invoice" ||
				copied.Header.Get("Content-Type") != "text/plain" || copied.Header.Get("X-Amz-Meta-Owner") != "alice" {
				t.Fatalf("the Worktree's copy of the object answered %d %q with Content-Type %q and owner %q, want the main Worktree's object with its metadata",
					copied.StatusCode, copied.Body, copied.Header.Get("Content-Type"), copied.Header.Get("X-Amz-Meta-Owner"))
			}
			assertContains(t, readFile(t, filepath.Join(wt.Dir, ".env")), "AWS_BUCKET="+project+"--feature\n")

			// Only the public Bucket's objects are readable without signing.
			if res := anonymousRequest(t, d, "GET", s3URL+"/"+project+"-public--feature/logo.svg"); res.StatusCode != http.StatusOK || res.Body != "<svg/>" {
				t.Fatalf("an anonymous GET of the public Bucket's object answered %d %q, want 200", res.StatusCode, res.Body)
			}
			if res := anonymousRequest(t, d, "GET", s3URL+"/"+project+"--feature/invoices/1.txt"); res.StatusCode != http.StatusForbidden {
				t.Fatalf("an anonymous GET of the private Bucket's object answered %d, want 403:\n%s", res.StatusCode, res.Body)
			}

			// CORS allows the Worktree's Domain, and follows its Aliases.
			upload := s3URL + "/" + project + "--feature/avatars/1.png"
			domain, alias := "https://feature."+project+".localhost", "https://checkout."+project+".localhost"
			if status, allowed := realPreflight(t, d, upload, domain); status != http.StatusOK || allowed != domain {
				t.Fatalf("a preflight from the Worktree's Domain answered %d allowing %q, want 200 allowing it", status, allowed)
			}
			if status, _ := realPreflight(t, d, upload, alias); status != http.StatusForbidden {
				t.Fatalf("a preflight from an Alias not yet added answered %d, want 403", status)
			}
			d.MustRunCLI(wt.Dir, "worktree", "alias", "checkout")
			eventuallyInBackground(t, "the Bucket to allow CORS from the new Alias", func() bool {
				status, allowed := realPreflight(t, d, upload, alias)
				return status == http.StatusOK && allowed == alias
			})

			// The console, at its Service Domain, starts RustFS.
			d.MustRunCLI(d.Home, "services", "down", "rustfs")
			assertRunning(t, engine, containerName, false)
			console := d.Get(consoleURL)
			if console.StatusCode != http.StatusOK || !strings.Contains(console.Body, "<html") {
				t.Fatalf("the console at %s answered %d, want its page:\n%.500s", consoleURL, console.StatusCode, console.Body)
			}
			assertRunning(t, engine, containerName, true)
			// And so does the S3 API, at its own.
			d.MustRunCLI(d.Home, "services", "down", "rustfs")
			assertRunning(t, engine, containerName, false)
			if res := s3Request(t, d, "GET", s3URL+"/"+project+"/invoices/1.txt", ""); res.StatusCode != http.StatusOK || res.Body != "private invoice" {
				t.Fatalf("a signed GET at the s3 Service Domain after a restart answered %d %q, want the object kept in the volume", res.StatusCode, res.Body)
			}
			assertRunning(t, engine, containerName, true)

			// A removed Worktree's Buckets are Orphans, dropped after their
			// grace period.
			repo.Git("worktree", "remove", "--force", wt.Dir)
			waitForOrphans(t, d, project+"--feature", project+"-exports--feature", project+"-public--feature")
			clk.Advance(daemon.OrphanGrace)
			waitForOrphans(t, d)
			list = s3Request(t, d, "GET", s3URL+"/", "")
			if got, want := bucketNames(list.Body), []string{project, project + "-exports", project + "-public"}; !slices.Equal(got, want) {
				t.Fatalf("after dropping the Orphans RustFS has the Buckets %q, want %q", got, want)
			}
		})
	}
}

// TestRustFSRefusesAnRcWhoseChecksumIsWrongOnARealContainerEngine adds the
// rustfs Plugin by hand with the SHA-256 of rc changed: a new Worktree's
// copy, the first command needing rc, fails Preparing saying which
// download didn't match.
func TestRustFSRefusesAnRcWhoseChecksumIsWrongOnARealContainerEngine(t *testing.T) {
	for name, engine := range realEngines(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d, _ := realRustFSDaemon(t, engine, "dash-rfs-sum-"+name+".localhost")
			plugin := testkit.FirstPartyPlugin(t, "rustfs")
			manifest := filepath.Join(plugin, "rustfs.toml")
			wrong := strings.Repeat("0", 64)
			original := readFile(t, manifest)
			changed := strings.NewReplacer(
				"31e79fe7697c870e60f5b5c9008adbb3ad5af7f9f2ab45a3d68585c5f87238d1", wrong, // amd64
				"a02091205fe4d9fbf0d16a0dac3d46ad7fac7d5266024b5b17e76327a4e88aa8", wrong, // arm64
			).Replace(original)
			if changed == original {
				t.Fatal("the rustfs Plugin pins rc's SHA-256 differently now: update this test's sums")
			}
			writeFile(t, manifest, changed)
			d.MustRunCLI(d.Home, "plugin", "add", "--yes", plugin)
			repo := serviceProject(t, "rfs-sum-"+name, `rustfs = "1"`, "")
			d.MustRunCLI(repo.Dir, "link", "--yes")
			if setup := waitForSetup(t, d, repo.Dir); setup.State != api.SetupReady {
				t.Fatalf("the main Worktree's setup is %+v, want Ready: it needs no rc", setup)
			}

			setup := waitForSetup(t, d, repo.AddWorktree("feature").Dir)

			if setup.State != api.SetupFailed {
				t.Fatalf("the Worktree's setup is %+v, want failed", setup)
			}
			why := setup.Error + "\n" + strings.Join(setup.Output, "\n")
			assertContains(t, why, "rustfs-cli-linux-")
			assertContains(t, why, "has SHA-256 ")
			assertContains(t, why, ", not "+wrong)
		})
	}
}
