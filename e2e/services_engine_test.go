package e2e

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/smtp"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/container"
	"github.com/mrsafalpiya/portby.dev/internal/daemon"
	"github.com/mrsafalpiya/portby.dev/internal/testkit"
)

// realEngines are the container engines the slow tier runs against: each
// Docker Engine API socket that answers. Without PORTBY_SLOW=1 the slow tier
// is skipped; an engine that doesn't answer is skipped on its own.
func realEngines(t *testing.T) map[string]*container.Docker {
	t.Helper()
	if os.Getenv("PORTBY_SLOW") == "" {
		t.Skip("slow tier: set PORTBY_SLOW=1 to run against real container engines")
	}
	sockets := map[string]string{"docker": "/var/run/docker.sock"}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		sockets["podman"] = filepath.Join(dir, "podman", "podman.sock")
	}
	engines := map[string]*container.Docker{}
	for name, socket := range sockets {
		engines[name] = container.NewDocker(socket)
	}
	return engines
}

// realEngineDaemon starts a daemon whose Services run on engine, under a
// container prefix of their own that is removed when the test ends, with
// opts applied after.
func realEngineDaemon(t *testing.T, engine *container.Docker, opts ...testkit.Option) (*testkit.Daemon, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Ping(ctx); err != nil {
		t.Skipf("no container engine at %s: %v", engine.Socket, err)
	}
	prefix := fmt.Sprintf("portby-test-%08x", rand.Uint32())
	opts = append([]testkit.Option{testkit.WithContainerEngine(engine),
		func(c *daemon.Config) { c.ContainerPrefix = prefix }}, opts...)
	d := testkit.StartDaemon(t, opts...)
	return d, prefix
}

// removeContainer removes a Service's container and volume when the test
// ends (after the daemon has stopped it).
func removeContainer(t *testing.T, engine *container.Docker, name string) {
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := engine.Remove(ctx, name, name); err != nil {
			t.Errorf("removing container %s: %v", name, err)
		}
	})
}

func TestRedisStartsLazilyAndStopsOnARealContainerEngine(t *testing.T) {
	for name, engine := range realEngines(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d, prefix := realEngineDaemon(t, engine)
			containerName := prefix + "-redis-7"
			removeContainer(t, engine, containerName)
			d.MustRunCLI(serviceProject(t, "svc-real-"+name, `redis = "7"`, "").Dir, "link")

			c := dialRedis(t, d, 6379)
			if got := c.do("AUTH", "portby"); got != "+OK" {
				t.Fatalf("AUTH = %q, want +OK", got)
			}
			if got := c.do("SET", "greeting", "hello"); got != "+OK" {
				t.Fatalf("SET = %q", got)
			}
			c.conn.Close()
			ctx := context.Background()
			if _, running, err := engine.Running(ctx, containerName, 6379); err != nil || !running {
				t.Fatalf("container %s running = %t, %v; want running", containerName, running, err)
			}

			d.MustRunCLI(d.Home, "services", "down", "redis")
			if _, running, err := engine.Running(ctx, containerName, 6379); err != nil || running {
				t.Fatalf("after `services down`, container running = %t, %v", running, err)
			}

			// It starts again on the next connection, with its data (the
			// volume).
			c = dialRedis(t, d, 6379)
			c.do("AUTH", "portby")
			if got := c.do("GET", "greeting"); got != "hello" {
				t.Fatalf("GET after a restart = %q, want hello", got)
			}
		})
	}
}

func TestMailpitCatchesMailAndServesItsInboxOnARealContainerEngine(t *testing.T) {
	for name, engine := range realEngines(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dash := "dash-mail-real-" + name + ".localhost"
			d, prefix := realEngineDaemon(t, engine, func(c *daemon.Config) { c.DashboardDomain = dash })
			containerName := prefix + "-mailpit-v1"
			removeContainer(t, engine, containerName)
			d.MustRunCLI(serviceProject(t, "mail-real-"+name, `mailpit = "v1"`, "").Dir, "link")
			inbox := "https://mail." + dash

			// Any login is accepted, so apps may use the shared credentials.
			sendMailAs(t, dialMail(t, d, 1025), "Welcome", smtp.PlainAuth("", "portby", "portby", "localhost"))
			if subjects := inboxSubjects(t, d, inbox); !slices.Equal(subjects, []string{"Welcome"}) {
				t.Fatalf("the inbox lists %q, want the message just sent", subjects)
			}

			d.MustRunCLI(d.Home, "services", "down", "mailpit")
			if _, running, err := engine.Running(context.Background(), containerName, 1025); err != nil || running {
				t.Fatalf("after `services down`, container running = %t, %v", running, err)
			}
			// Visiting the inbox starts it again, with the mail kept (the
			// volume).
			if subjects := inboxSubjects(t, d, inbox); !slices.Equal(subjects, []string{"Welcome"}) {
				t.Fatalf("after a restart the inbox lists %q, want the message sent before", subjects)
			}
		})
	}
}
