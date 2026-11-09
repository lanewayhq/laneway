package e2e

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lanewayhq/laneway/internal/testkit"
)

// redis74 is Redis 7.4 by digest (its multi-platform index), so the slow
// tier runs a version mapped to a digest too.
const redis74 = "docker.io/library/redis@sha256:2e3eb11462c4e3759e6972678dd051d150ff54b1e60714b3c33b701260172a90"

// TestAMappedVersionMovesToItsNewImageWithItsDataOnARealContainerEngine
// adds the redis Plugin by hand with "7" mapped to redis:7.2, then updates
// it to map "7" to Redis 7.4 by digest: the Service's container is created
// again from the new image, with the data in its volume, at its port.
func TestAMappedVersionMovesToItsNewImageWithItsDataOnARealContainerEngine(t *testing.T) {
	for name, engine := range realEngines(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			for _, image := range []string{"docker.io/library/redis:7.2", redis74} {
				if err := engine.Pull(ctx, image); err != nil {
					t.Skipf("pulling %s on %s: %v", image, name, err)
				}
			}
			d, prefix := realEngineDaemon(t, engine)
			containerName := prefix + "-redis-7"
			removeContainer(t, engine, containerName)
			plugin := testkit.FirstPartyPlugin(t, "redis")
			manifest := filepath.Join(plugin, "redis.toml")
			mapTo := func(image string) {
				writeFile(t, manifest, strings.Replace(readFile(t, manifest), `image = "docker.io/library/redis"`, "", 1)+
					fmt.Sprintf("\n[versions.\"7\"]\nimage = %q\n", image))
			}
			original := readFile(t, manifest)
			mapTo("docker.io/library/redis:7.2")
			d.MustRunCLI(d.Home, "plugin", "add", "--yes", plugin)
			d.MustRunCLI(serviceProject(t, "versions-real-"+name, `redis = "7"`, "").Dir, "link")

			c := dialRedis(t, d, 6379)
			c.do("AUTH", "laneway")
			if got := c.do("SET", "greeting", "hello"); got != "+OK" {
				t.Fatalf("SET = %q", got)
			}
			if got := c.do("EVAL", "return redis.REDIS_VERSION", "0"); !strings.HasPrefix(got, "7.2.") {
				t.Fatalf("Redis version before the update = %q, want Redis 7.2", got)
			}
			c.conn.Close()

			writeFile(t, manifest, original)
			mapTo(redis74)
			withManifest(t, plugin, `{"name": "redis", "version": "99.0.0", "api_version": 1, "engines": ["redis.toml"]}`)
			assertContains(t, d.MustRunCLI(d.Home, "plugin", "update", "redis", "--yes").Stdout, "→ 99.0.0")

			c = dialRedis(t, d, 6379)
			c.do("AUTH", "laneway")
			if got := c.do("GET", "greeting"); got != "hello" {
				t.Fatalf("GET after the update = %q, want the hello written before it", got)
			}
			if got := c.do("EVAL", "return redis.REDIS_VERSION", "0"); !strings.HasPrefix(got, "7.4.") {
				t.Fatalf("Redis version after the update = %q, want Redis 7.4", got)
			}
		})
	}
}
