package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/container/containertest"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// Engine version mapping through Seam 1 (ADR-0008): an Engine manifest may
// map a Service version to an image, by tag or by digest, as the rustfs
// Plugin maps "1" to rustfs:1.0.4. The Service, its volume and its port are
// keyed by the version, so a Plugin update mapping the version to a new
// image keeps them.

// kvDigest is an image reference of the kv Engine's image by digest.
const kvDigest = kvImage + "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// mappedKV is the kv test Plugin at version, its Engine mapping the Service
// version "1" to image instead of tagging its image with the version.
func mappedKV(t *testing.T, version, image string) string {
	t.Helper()
	return mappedKVAt(t, testkit.Plugin(t, "kv"), version, image)
}

// mappedKVAt makes dir, a copy of the kv test Plugin, a mappedKV.
func mappedKVAt(t *testing.T, dir, version, image string) string {
	t.Helper()
	withManifest(t, dir, fmt.Sprintf(`{"name": "kv", "version": %q, "api_version": 1, "engines": ["kv.toml"]}`, version))
	manifest := filepath.Join(dir, "kv.toml")
	kv, _, _ := strings.Cut(readFile(t, manifest), "\n[versions")
	writeFile(t, manifest, strings.Replace(kv, `image = "example.test/kv"`, "", 1)+
		fmt.Sprintf("\n[versions.\"1\"]\nimage = %q\n", image))
	return dir
}

// mappedKVDaemon is a daemon with plugin, a mappedKV, added, whose fake
// container engine can run the kv Engine's image at any tag or digest.
func mappedKVDaemon(t *testing.T, plugin string) *testkit.Daemon {
	t.Helper()
	d := testkit.StartDaemon(t)
	d.Engine.Serve(kvImage, containertest.Redis)
	d.MustRunCLI(d.Home, "plugin", "add", "--yes", plugin)
	return d
}

func TestAServiceAtAMappedVersionRunsTheImageTheVersionMapsTo(t *testing.T) {
	t.Parallel()
	d := mappedKVDaemon(t, mappedKV(t, "0.2.0", kvDigest))
	d.MustRunCLI(serviceProject(t, "kv-mapped", `kv = "1"`, "").Dir, "link")

	c := dialRedis(t, d, 7379)
	if got := c.do("AUTH", "laneway"); got != "+OK" {
		t.Fatalf("AUTH = %q, want +OK", got)
	}

	if got := d.Engine.Image("laneway-kv-1"); got != kvDigest {
		t.Fatalf("the kv:1 container runs %q, want %q", got, kvDigest)
	}
}

func TestDeclaringAVersionAnEnginesMappingDoesntListIsAConfigErrorNamingTheEngine(t *testing.T) {
	t.Parallel()
	d := mappedKVDaemon(t, mappedKV(t, "0.2.0", kvImage+":1.0.4"))

	res := d.RunCLI(serviceProject(t, "kv-unmapped", `kv = "2"`, "").Dir, "link")

	if res.ExitCode == 0 {
		t.Fatalf("Linking a Project declaring kv 2 exited 0:\n%s", res.Output())
	}
	assertContains(t, res.Stderr, `services.kv: the kv Engine has no version "2"; its versions are "1"`)
}

func TestUpdatingAHandAddedPluginMovesAServiceToTheImageItsVersionNowMapsTo(t *testing.T) {
	t.Parallel()
	plugin := mappedKV(t, "0.2.0", kvImage+":1.0.4")
	d := mappedKVDaemon(t, plugin)
	d.MustRunCLI(serviceProject(t, "kv-hand-updated", `kv = "1"`, "").Dir, "link")
	c := dialRedis(t, d, 7379)
	c.do("AUTH", "laneway")
	c.do("SET", "greeting", "hello")
	c.conn.Close()
	mappedKVAt(t, plugin, "0.3.0", kvImage+":1.0.5")

	assertContains(t, d.MustRunCLI(d.Home, "plugin", "update", "kv", "--yes").Stdout, "Updated Plugin kv 0.2.0 → 0.3.0")

	c = dialRedis(t, d, 7379)
	c.do("AUTH", "laneway")
	if got := c.do("GET", "greeting"); got != "hello" {
		t.Fatalf("GET after the update = %q, want the hello written before it", got)
	}
	if got := d.Engine.Image("laneway-kv-1"); got != kvImage+":1.0.5" {
		t.Fatalf("after the update the kv:1 container runs %q, want %s:1.0.5", got, kvImage)
	}
}

// TestAPluginUpdateMappingAVersionToANewImageRecreatesItsServiceWithItsData
// updates the Plugin while its Service is running, and while it is stopped:
// either way, the next connection reaches a container of the new image, at
// the same port, with the data written before.
func TestAPluginUpdateMappingAVersionToANewImageRecreatesItsServiceWithItsData(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(fmt.Sprintf("running=%t", running), func(t *testing.T) {
			t.Parallel()
			cat := testkit.NewCatalog(t)
			cat.Publish(mappedKV(t, "1.0.0", kvImage+":1.0.4"))
			d := catalogDaemon(t, cat)
			d.MustRunCLI(serviceProject(t, "kv-upgraded", `kv = "1"`, "").Dir, "link", "--yes")
			c := dialRedis(t, d, 7379)
			c.do("AUTH", "laneway")
			if got := c.do("SET", "greeting", "hello"); got != "+OK" {
				t.Fatalf("SET = %q, want +OK", got)
			}
			c.conn.Close()
			if got := d.Engine.Image("laneway-kv-1"); got != kvImage+":1.0.4" {
				t.Fatalf("before the update the kv:1 container runs %q, want %s:1.0.4", got, kvImage)
			}
			if !running {
				d.MustRunCLI(d.Home, "services", "down", "kv")
			}
			cat.Publish(mappedKV(t, "1.1.0", kvImage+":1.0.5"))

			assertContains(t, d.MustRunCLI(d.Home, "plugin", "update", "--yes").Stdout, "Updated Plugin kv 1.0.0 → 1.1.0")

			c = dialRedis(t, d, 7379)
			c.do("AUTH", "laneway")
			if got := c.do("GET", "greeting"); got != "hello" {
				t.Fatalf("GET after the update = %q, want the hello written before it", got)
			}
			if got := d.Engine.Image("laneway-kv-1"); got != kvImage+":1.0.5" {
				t.Fatalf("after the update the kv:1 container runs %q, want %s:1.0.5", got, kvImage)
			}
		})
	}
}
