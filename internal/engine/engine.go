// Package engine is Laneway's Engines as data (spec #48, ADR-0007): an
// Engine manifest says everything Laneway needs to run a Service of it and
// make its databases, with no code of the Engine's own. Package service runs
// the container and its readiness probes; package database runs the
// database commands inside it.
//
// Every Engine comes from a Plugin (package plugin), which lists Engine
// manifests that Parse reads. Even Redis, Mailpit, Postgres and MySQL come
// from first-party Plugins in the Catalog. Set is the Engines Laneway can
// run.
package engine

import (
	"bytes"
	"cmp"
	"fmt"
	"hash/fnv"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Password is the shared local password of every Service, so every env file
// and GUI client uses the same one. A manifest uses it as {password}.
const Password = "laneway"

// Manifest is an Engine: a kind of Service, such as Redis. A Service is an
// Engine at a version.
//
// Strings in Env, Command, the readiness probes' commands and the database
// commands may hold placeholders, which Laneway replaces: {user} (User),
// {password} (Password), and {version} (the Service's version) in what
// configures the container; {db}, {from} and {to} in the database commands
// (see Databases). {{ and }} stand for { and }.
type Manifest struct {
	// Name is how config and the CLI name it, such as "redis". The Env
	// Patch's Service variables start with it ({redis_port}).
	Name string `toml:"name" json:"name"`
	// DefaultVersion is the version that gets Port; other versions get a
	// derived port (see Ports).
	DefaultVersion string `toml:"default_version" json:"default_version"`
	// Image is the image repository, fully qualified; the version is its
	// tag. It is "" when Versions maps each version to an image instead.
	Image string `toml:"image" json:"image,omitempty"`
	// Versions, if set instead of Image, are the only versions a Service of
	// the Engine may be at, each with the image it runs (see ImageRef).
	Versions map[string]Version `toml:"versions" json:"versions,omitempty"`
	// Port is the Engine's standard TCP port, which is also the port it
	// listens on inside its container.
	Port int `toml:"port" json:"port"`
	// WebPort is the container port of a web UI the Engine serves besides
	// Port, such as Mailpit's inbox; 0 if none. It is published too.
	WebPort int `toml:"web_port" json:"web_port,omitempty"`
	// DataDir is where the Engine keeps its data in the container; a named
	// volume is mounted there.
	DataDir string `toml:"data_dir" json:"data_dir"`
	// User is the user clients log in as; "" if the Engine has no users
	// (Redis is password-only).
	User string `toml:"user" json:"user,omitempty"`
	// Env is the container's environment.
	Env map[string]string `toml:"env" json:"env,omitempty"`
	// Command is the container's command; empty for the image's own.
	Command []string `toml:"command" json:"command,omitempty"`
	// Ready are the checks that the Engine is ready for clients, each
	// tried until it passes, in order, once the container is started.
	Ready []Probe `toml:"ready" json:"ready"`
	// Database is how Laneway makes the Engine's databases; nil if a
	// Project can't have its database in it.
	Database *Databases `toml:"database" json:"database,omitempty"`
	// Domains are the Service Domains Laneway serves the Engine's ports at
	// (see ServiceDomain); the first one's URL is {<engine>_endpoint}.
	Domains []ServiceDomain `toml:"domains" json:"domains,omitempty"`
}

// The kinds of readiness Probe.
const (
	// Banner reads the first line the Engine sends on a new connection,
	// which must start with Expect, such as SMTP's "220".
	Banner = "banner"
	// RedisPing sends PING: any answer from Redis itself, even an error
	// such as -NOAUTH, means it is up, except -LOADING (still loading its
	// data).
	RedisPing = "redis-ping"
	// HTTP sends GET Path, which must answer with Status.
	HTTP = "http"
	// Command runs Command in the container, which must exit 0.
	Command = "command"
)

// kinds are the Probe kinds there are.
var kinds = []string{Banner, RedisPing, HTTP, Command}

// Probe is one check that an Engine is ready for clients. A connection alone
// proves nothing: the container engine's port forwarder accepts connections
// before anything listens inside the container. So each kind needs the
// Engine itself to answer.
type Probe struct {
	// Kind is one of Banner, RedisPing, HTTP or Command.
	Kind string `toml:"kind" json:"kind"`
	// Port is the container port Banner, RedisPing and HTTP connect to:
	// Port or WebPort, Port if not set.
	Port int `toml:"port" json:"port,omitempty"`
	// Expect is what a Banner's first line starts with.
	Expect string `toml:"expect" json:"expect,omitempty"`
	// Path and Status are an HTTP probe's request path ("/" if not set)
	// and the status it must answer with (200 if not set).
	Path   string `toml:"path" json:"path,omitempty"`
	Status int    `toml:"status" json:"status,omitempty"`
	// Command is what a Command probe runs in the container.
	Command []string `toml:"command" json:"command,omitempty"`
}

// Databases are the commands that make, copy, drop and list an Engine's
// databases, run inside the Service's container (no shell: the first word
// is the program; use sh -c for more). A database name is lowercase letters,
// digits and _ (see package naming), so {db}, {from} and {to} can go into
// SQL or a shell script as they are. A command fails by exiting with a
// status other than 0, saying why on stderr.
type Databases struct {
	// MaxName is the longest a database name may be.
	MaxName int `toml:"max_name" json:"max_name"`
	// Exists prints something if the database {db} exists and nothing if
	// it doesn't.
	Exists []string `toml:"exists" json:"exists"`
	// Create creates {db}, empty. It runs only once Exists has said {db}
	// doesn't exist.
	Create []string `toml:"create" json:"create"`
	// Clone creates {to} as a copy of {from}. It runs only once Exists has
	// said {from} exists and {to} doesn't. It must leave {to} complete or
	// not there, or as something Exists says doesn't exist.
	Clone []string `toml:"clone" json:"clone"`
	// Drop drops {db}, closing any connections to it first. Dropping a
	// database that doesn't exist must succeed.
	Drop []string `toml:"drop" json:"drop"`
	// List prints every database in the Service, one per line.
	List []string `toml:"list" json:"list"`
}

// Placeholders each part of a manifest may use.
var (
	containerPlaceholders = []string{"user", "password", "version"}
	dbPlaceholders        = []string{"user", "password", "db"}
	clonePlaceholders     = []string{"user", "password", "from", "to"}
	listPlaceholders      = []string{"user", "password"}
)

// placeholder matches what Expand treats specially: an escaped brace, or
// {name}. Any other brace is literal.
var placeholder = regexp.MustCompile(`\{\{|\}\}|\{([a-z][a-z0-9_]*)\}`)

// Expand replaces the placeholders in args: {user} and {password} with the
// Engine's, the others with values. One without a value is left as it is
// (Parse has already refused unknown ones).
func (m *Manifest) Expand(args []string, values map[string]string) []string {
	if args == nil {
		return nil
	}
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = placeholder.ReplaceAllStringFunc(arg, func(match string) string {
			switch match {
			case "{{":
				return "{"
			case "}}":
				return "}"
			}
			name := match[1 : len(match)-1]
			switch v, ok := values[name]; {
			case ok:
				return v
			case name == "user":
				return m.User
			case name == "password":
				return Password
			}
			return match
		})
	}
	return out
}

// ContainerEnv is the environment of the container of a Service of m at
// version, as KEY=value, sorted.
func (m *Manifest) ContainerEnv(version string) []string {
	var env []string
	for _, k := range slices.Sorted(maps.Keys(m.Env)) {
		env = append(env, k+"="+m.Expand([]string{m.Env[k]}, map[string]string{"version": version})[0])
	}
	return env
}

// ContainerCmd is the command of the container of a Service of m at
// version; nil for the image's own.
func (m *Manifest) ContainerCmd(version string) []string {
	return m.Expand(m.Command, map[string]string{"version": version})
}

// Variables are the Env Patch template variables each Service gives a
// Worktree using it: {<engine>_host}, {<engine>_port}, {<engine>_username}
// (only for Engines with users), {<engine>_password} and
// {<engine>_endpoint} (only for Engines with Service Domains; see
// Endpoint).
func (m *Manifest) Variables() []string {
	vars := []string{m.Name + "_host", m.Name + "_port"}
	if m.User != "" {
		vars = append(vars, m.Name+"_username")
	}
	vars = append(vars, m.Name+"_password")
	if len(m.Domains) > 0 {
		vars = append(vars, m.endpointVariable())
	}
	return vars
}

// Values are the Variables' values for a Service of m listening on host and
// port.
func (m *Manifest) Values(host string, port int) map[string]string {
	vars := map[string]string{m.Name + "_host": host, m.Name + "_port": strconv.Itoa(port)}
	if m.User != "" {
		vars[m.Name+"_username"] = m.User
	}
	vars[m.Name+"_password"] = Password
	return vars
}

// Ports are the ports a Service of m at version may listen on, best first.
// The default version gets the standard Port. Any other version gets a port
// derived from the first three digits of the standard port and the
// version's digits, such as redis:6 → 63706 and postgres:14 → 54314; a
// version with more than two digits uses a hash of it instead. The others
// are the rest of that block of 100, in order, for when the first is taken.
// The port a Service gets is recorded in the Registry, so it never changes.
func (m *Manifest) Ports(version string) []int {
	if version == m.DefaultVersion {
		return []int{m.Port}
	}
	base := m.Port / 10 * 100
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

// name is what an Engine may be called: its Service variables must be
// template variables ({redis_port}).
var name = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Parse reads the Engine manifest data, in TOML, from source (named in
// errors), and checks that Laneway can run it.
func Parse(source string, data []byte) (*Manifest, error) {
	var m Manifest
	md, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&m)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("%s: unknown key %s", source, undecoded[0])
	}
	if err := m.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return &m, nil
}

// check reports the first thing in m Laneway can't use, filling in the
// probes' defaults.
func (m *Manifest) check() error {
	switch {
	case !name.MatchString(m.Name):
		return fmt.Errorf("name must be lowercase letters, digits and _, starting with a letter, not %q", m.Name)
	case m.DefaultVersion == "":
		return fmt.Errorf("default_version is missing")
	case m.Image == "" && len(m.Versions) == 0:
		return fmt.Errorf("image is missing: give the image repository, fully qualified, such as \"docker.io/library/redis\", " +
			"or map each version to an image in [versions]")
	case m.Image != "" && len(m.Versions) > 0:
		return fmt.Errorf("image and [versions] are both set: give either the image repository, whose tags are the versions, " +
			"or [versions], mapping each version to an image")
	case m.Port < 1 || m.Port > 65535:
		return fmt.Errorf("port must be the Engine's TCP port, 1 to 65535, not %d", m.Port)
	case m.WebPort < 0 || m.WebPort > 65535:
		return fmt.Errorf("web_port must be 1 to 65535, not %d", m.WebPort)
	case !path.IsAbs(m.DataDir):
		return fmt.Errorf("data_dir must be an absolute path in the container, not %q", m.DataDir)
	case len(m.Ready) == 0:
		return fmt.Errorf("ready is missing: add a [[ready]] probe saying when the Engine is ready for clients")
	}
	if err := m.checkVersions(); err != nil {
		return err
	}
	for _, k := range slices.Sorted(maps.Keys(m.Env)) {
		if err := checkPlaceholders("env."+k, []string{m.Env[k]}, containerPlaceholders); err != nil {
			return err
		}
	}
	if err := checkPlaceholders("command", m.Command, containerPlaceholders); err != nil {
		return err
	}
	for i := range m.Ready {
		if err := m.checkProbe(i, &m.Ready[i]); err != nil {
			return err
		}
	}
	if err := m.checkDomains(); err != nil {
		return err
	}
	if db := m.Database; db != nil {
		if db.MaxName < 1 {
			return fmt.Errorf("database.max_name must be the longest a database name may be")
		}
		for _, c := range []struct {
			key          string
			cmd          []string
			placeholders []string
		}{
			{"exists", db.Exists, dbPlaceholders},
			{"create", db.Create, dbPlaceholders},
			{"clone", db.Clone, clonePlaceholders},
			{"drop", db.Drop, dbPlaceholders},
			{"list", db.List, listPlaceholders},
		} {
			if len(c.cmd) == 0 {
				return fmt.Errorf("database.%s is missing: the database commands are exists, create, clone, drop and list", c.key)
			}
			if err := checkPlaceholders("database."+c.key, c.cmd, c.placeholders); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manifest) checkProbe(i int, p *Probe) error {
	key := fmt.Sprintf("ready[%d]", i)
	if !slices.Contains(kinds, p.Kind) {
		return fmt.Errorf("%s.kind must be one of %s, not %q", key, strings.Join(kinds, ", "), p.Kind)
	}
	if p.Kind == Command {
		if len(p.Command) == 0 {
			return fmt.Errorf("%s.command is missing: a command probe runs it in the container", key)
		}
		return checkPlaceholders(key+".command", p.Command, containerPlaceholders)
	}
	p.Port = cmp.Or(p.Port, m.Port)
	if p.Port != m.Port && p.Port != m.WebPort {
		return fmt.Errorf("%s.port is %d, which isn't published: a probe can only connect to port or web_port", key, p.Port)
	}
	switch p.Kind {
	case Banner:
		if p.Expect == "" {
			return fmt.Errorf("%s.expect is missing: a banner probe needs what the Engine's first line starts with", key)
		}
	case HTTP:
		p.Path = cmp.Or(p.Path, "/")
		p.Status = cmp.Or(p.Status, 200)
		if !strings.HasPrefix(p.Path, "/") {
			return fmt.Errorf("%s.path must start with /, not %q", key, p.Path)
		}
	}
	return nil
}

// checkPlaceholders reports a placeholder in args, the value of key, that
// isn't one of allowed.
func checkPlaceholders(key string, args []string, allowed []string) error {
	for _, arg := range args {
		for _, m := range placeholder.FindAllStringSubmatch(arg, -1) {
			if m[1] != "" && !slices.Contains(allowed, m[1]) {
				return fmt.Errorf("%s uses {%s}, which Laneway doesn't fill in there; it can use {%s} (write {{ for a literal {)",
					key, m[1], strings.Join(allowed, "}, {"))
			}
		}
	}
	return nil
}

// Set is the Engines Laneway can run, in the order Names lists them: the
// installed Plugins' (see plugin.Host.Engines). Each has its own name.
type Set []*Manifest

// Assumed stands for an Engine known by its name alone, such as a Catalog
// Engine not fetched yet: config naming it, and anything config may ask of
// an Engine (users, databases, an endpoint), resolves as if it were
// installed. It can't be run.
func Assumed(name string) *Manifest {
	return &Manifest{Name: name, DefaultVersion: "latest", User: "assumed", Database: &Databases{},
		Domains: []ServiceDomain{{Label: "assumed"}}}
}

// Lookup returns the Engine called name.
func (s Set) Lookup(name string) (*Manifest, bool) {
	for _, m := range s {
		if m.Name == name {
			return m, true
		}
	}
	return nil, false
}

// Names lists the Engines' names, for messages.
func (s Set) Names() []string {
	var names []string
	for _, m := range s {
		names = append(names, m.Name)
	}
	return names
}

// Variables are every Engine's Variables.
func (s Set) Variables() []string {
	var vars []string
	for _, m := range s {
		vars = append(vars, m.Variables()...)
	}
	return vars
}
