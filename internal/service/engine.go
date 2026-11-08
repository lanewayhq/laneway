// Package service is the Service Manager (ADR-0001, ADR-0005): Services are
// shared containers, one per engine and version (redis:7), started on the
// first connection to the port Laneway listens on for them and stopped once
// they have had no connections and no running Worktree depending on them
// for the Service idle timeout.
//
// Engines lists the Service engines Laneway knows. Adding one (Postgres,
// MySQL, Mailpit) is adding an *Engine there: its image, port, command and
// a readiness Probe. See the notes in architecture.md.
package service

import (
	"bufio"
	"context"
	"fmt"
	"hash/fnv"
	"net"
	"strconv"
	"strings"
)

// User and Password are the shared local credentials of every Service, so
// every env file and GUI client uses the same ones.
const (
	User     = "laneway"
	Password = "laneway"
)

// Engine is a kind of Service, such as Redis. A Service is an Engine at a
// version.
type Engine struct {
	// Name is how config and the CLI name it, such as "redis".
	Name string
	// DefaultVersion is the version that gets Port; other versions get a
	// derived port (see Ports).
	DefaultVersion string
	// Port is the engine's standard TCP port, which is also the port it
	// listens on inside its container.
	Port int
	// WebPort is the container port of a web UI the engine serves besides
	// Port, such as Mailpit's inbox; 0 if none. It is published too, and
	// the Service is only ready once it answers HTTP there (Manager.Web).
	WebPort int
	// Image is the image repository, fully qualified; the version is its
	// tag.
	Image string
	// DataDir is where the engine keeps its data in the container; a named
	// volume is mounted there.
	DataDir string
	// User is the user clients log in as; "" if the engine has no users
	// (Redis is password-only).
	User string
	// Env and Cmd configure the container (nil Cmd = the image's default).
	Env func(version string) []string
	Cmd func(version string) []string
	// Probe reports whether the engine is ready for clients, speaking its
	// protocol over conn, a fresh connection to the container's published
	// port. The engine's port forwarder accepts connections before
	// anything listens inside the container, so a connection alone proves
	// nothing. It returns an error while the engine isn't ready.
	Probe func(ctx context.Context, conn net.Conn) error
}

// Engines are the Service engines Laneway can run.
var Engines = []*Engine{Redis, Mailpit, Postgres, MySQL}

// Lookup returns the Engine called name.
func Lookup(name string) (*Engine, bool) {
	for _, e := range Engines {
		if e.Name == name {
			return e, true
		}
	}
	return nil, false
}

// Names lists the Engines' names, for messages.
func Names() []string {
	var names []string
	for _, e := range Engines {
		names = append(names, e.Name)
	}
	return names
}

// Variables are the Env Patch template variables each Service gives a
// Worktree using it: {<engine>_host}, {<engine>_port}, {<engine>_username}
// (only for engines with users) and {<engine>_password}.
func (e *Engine) Variables() []string {
	vars := []string{e.Name + "_host", e.Name + "_port"}
	if e.User != "" {
		vars = append(vars, e.Name+"_username")
	}
	return append(vars, e.Name+"_password")
}

// Values are the Variables' values for a Service of e listening on host and
// port.
func (e *Engine) Values(host string, port int) map[string]string {
	vars := map[string]string{e.Name + "_host": host, e.Name + "_port": strconv.Itoa(port)}
	if e.User != "" {
		vars[e.Name+"_username"] = e.User
	}
	vars[e.Name+"_password"] = Password
	return vars
}

// AllVariables are every Engine's Variables.
func AllVariables() []string {
	var vars []string
	for _, e := range Engines {
		vars = append(vars, e.Variables()...)
	}
	return vars
}

// Ports are the ports a Service of e at version may listen on, best first.
// The default version gets the standard Port. Any other version gets a port
// derived from the first three digits of the standard port and the
// version's digits, such as redis:6 → 63706 and postgres:14 → 54314; a
// version with more than two digits uses a hash of it instead. The others
// are the rest of that block of 100, in order, for when the first is taken.
// The port a Service gets is recorded in the Registry, so it never changes.
func (e *Engine) Ports(version string) []int {
	if version == e.DefaultVersion {
		return []int{e.Port}
	}
	base := e.Port / 10 * 100
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, version)
	n, err := strconv.Atoi(digits)
	if err != nil || n > 99 {
		h := fnv.New32a()
		h.Write([]byte(version))
		n = int(h.Sum32() % 100)
	}
	ports := make([]int, 0, 100)
	for i := range 100 {
		ports = append(ports, base+(n+i)%100)
	}
	return ports
}

// Redis is the Redis engine.
var Redis = &Engine{
	Name:           "redis",
	DefaultVersion: "7",
	Port:           6379,
	Image:          "docker.io/library/redis",
	DataDir:        "/data",
	Cmd: func(string) []string {
		return []string{"redis-server", "--requirepass", Password, "--appendonly", "yes"}
	},
	Probe: probeRedis,
}

// probeRedis sends PING. An answer from Redis itself, even -NOAUTH, means
// it is up; -LOADING means it is still loading its data.
func probeRedis(ctx context.Context, conn net.Conn) error {
	if _, err := conn.Write([]byte("PING\r\n")); err != nil {
		return err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	if strings.HasPrefix(line, "-LOADING") || !(strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) {
		return fmt.Errorf("redis: %s", strings.TrimSpace(line))
	}
	return nil
}

// Mailpit is the Mailpit engine: an SMTP server that catches all mail, and
// an inbox to read it (WebPort, served at the mail Domain). It accepts any
// login or none, so apps can use the shared credentials or no credentials.
// Mail is kept in the volume, so it survives Idle.
var Mailpit = &Engine{
	Name:           "mailpit",
	DefaultVersion: "v1",
	Port:           1025,
	WebPort:        8025,
	Image:          "docker.io/axllent/mailpit",
	DataDir:        "/data",
	Env: func(string) []string {
		return []string{
			"MP_DATABASE=/data/mailpit.db",
			"MP_SMTP_AUTH_ACCEPT_ANY=true",
			"MP_SMTP_AUTH_ALLOW_INSECURE=true",
		}
	},
	Probe: probeSMTP,
}

// probeSMTP reads the server's greeting: 220 once it is ready.
func probeSMTP(ctx context.Context, conn net.Conn) error {
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	if !strings.HasPrefix(line, "220") {
		return fmt.Errorf("smtp: %s", strings.TrimSpace(line))
	}
	return nil
}
