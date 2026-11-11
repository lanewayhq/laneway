package engine_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/engine"
)

const searchManifest = `
name = "search"
default_version = "1.9"
image = "docker.io/example/search"
port = 7700
web_port = 7701
data_dir = "/srv/data"
user = "admin"
command = ["search", "--master-key={password}", "--tag=v{version}"]

[env]
SEARCH_USER = "{user}"
SEARCH_ENV = "development"

[[ready]]
kind = "banner"
expect = "220"

[[ready]]
kind = "http"
port = 7701
path = "/health"
status = 204

[[ready]]
kind = "command"
command = ["search", "ping", "--user={user}"]

[database]
max_name = 40
exists = ["search", "has", "{db}"]
create = ["search", "make", "{db}"]
clone = ["search", "copy", "{from}", "{to}"]
drop = ["search", "drop", "{db}"]
list = ["search", "list", "--password={password}"]
`

func TestAnEngineManifestDescribesItsContainerReadinessAndDatabaseCommands(t *testing.T) {
	m, err := engine.Parse("search.toml", []byte(searchManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "search" || m.DefaultVersion != "1.9" || m.Image != "docker.io/example/search" ||
		m.Port != 7700 || m.WebPort != 7701 || m.DataDir != "/srv/data" || m.User != "admin" {
		t.Fatalf("parsed %+v", m)
	}
	if got, want := m.ContainerEnv("2.0"), []string{"SEARCH_ENV=development", "SEARCH_USER=admin"}; !slices.Equal(got, want) {
		t.Fatalf("container env %q, want %q", got, want)
	}
	if got, want := m.ContainerCmd("2.0"), []string{"search", "--master-key=laneway", "--tag=v2.0"}; !slices.Equal(got, want) {
		t.Fatalf("container command %q, want %q", got, want)
	}

	want := []engine.Probe{
		{Kind: engine.Banner, Port: 7700, Expect: "220"},
		{Kind: engine.HTTP, Port: 7701, Path: "/health", Status: 204},
		{Kind: engine.Command, Command: []string{"search", "ping", "--user={user}"}},
	}
	if !slices.EqualFunc(m.Ready, want, func(a, b engine.Probe) bool {
		return a.Kind == b.Kind && a.Port == b.Port && a.Expect == b.Expect && a.Path == b.Path &&
			a.Status == b.Status && slices.Equal(a.Command, b.Command)
	}) {
		t.Fatalf("readiness probes %+v, want %+v", m.Ready, want)
	}
	if got := m.Expand(m.Ready[2].Command, nil); !slices.Equal(got, []string{"search", "ping", "--user=admin"}) {
		t.Fatalf("expanded probe command %q", got)
	}

	db := m.Database
	if db == nil || db.MaxName != 40 {
		t.Fatalf("database commands %+v", db)
	}
	if got := m.Expand(db.Clone, map[string]string{"from": "shop", "to": "shop__feature"}); !slices.Equal(got, []string{"search", "copy", "shop", "shop__feature"}) {
		t.Fatalf("expanded clone command %q", got)
	}
	if got := m.Expand(db.List, nil); !slices.Equal(got, []string{"search", "list", "--password=laneway"}) {
		t.Fatalf("expanded list command %q", got)
	}
}

func TestAnHTTPProbeDefaultsToStatus200AtSlashOnTheEnginesPort(t *testing.T) {
	m, err := engine.Parse("web.toml", []byte(`
name = "web"
default_version = "1"
image = "docker.io/example/web"
port = 8080
data_dir = "/data"

[[ready]]
kind = "http"
`))
	if err != nil {
		t.Fatal(err)
	}
	if p := m.Ready[0]; p.Port != 8080 || p.Path != "/" || p.Status != 200 {
		t.Fatalf("probe %+v, want GET / on 8080 expecting 200", p)
	}
	if m.Database != nil {
		t.Fatalf("database commands %+v, want none", m.Database)
	}
}

const mappedManifest = `
name = "store"
default_version = "1"
port = 9000
data_dir = "/data"

[versions.1]
image = "docker.io/example/store:1.0.4"

[versions."2"]
image = "registry.example.test:5000/team/store@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

[[ready]]
kind = "http"
`

func TestAnEngineMayMapEachServiceVersionToAnImageByTagOrDigest(t *testing.T) {
	m, err := engine.Parse("store.toml", []byte(mappedManifest))
	if err != nil {
		t.Fatal(err)
	}
	for version, want := range map[string]string{
		"1": "docker.io/example/store:1.0.4",
		"2": "registry.example.test:5000/team/store@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	} {
		if got, err := m.ImageRef(version); err != nil || got != want {
			t.Errorf("version %s runs %q (%v), want %q", version, got, err, want)
		}
	}
	_, err = m.ImageRef("3")
	if err == nil || !strings.Contains(err.Error(), "store") || !strings.Contains(err.Error(), `"1", "2"`) {
		t.Fatalf("an unlisted version gave %v, want an error naming the Engine and its versions", err)
	}
	// The default version still gets the standard port first: ports are
	// keyed by the version, not the image.
	if got := m.Ports("1"); len(got) < 2 || got[0] != 9000 || got[1] != 59001 {
		t.Fatalf("ports of the default version start %v, want 9000 then 59001", got[:min(len(got), 3)])
	}
}

// enginePorts parses an Engine whose standard port is port and default
// version 1, and returns the ports of version.
func enginePorts(t *testing.T, port int, version string) []int {
	t.Helper()
	return enginePortsAt(t, port, "1", version)
}

// enginePortsAt parses an Engine whose standard port is port and default
// version defaultVersion, and returns the ports of version.
func enginePortsAt(t *testing.T, port int, defaultVersion, version string) []int {
	t.Helper()
	m, err := engine.Parse("cache.toml", []byte(fmt.Sprintf(`
name = "cache"
default_version = %q
image = "docker.io/example/cache"
port = %d
data_dir = "/data"

[[ready]]
kind = "http"
`, defaultVersion, port)))
	if err != nil {
		t.Fatal(err)
	}
	return m.Ports(version)
}

// checkPorts checks that version of an Engine whose standard port is port
// gets first, then the rest of first's block of 100.
func checkPorts(t *testing.T, port int, version string, first int) {
	t.Helper()
	checkBlock(t, fmt.Sprintf("port %d, version %s", port, version), enginePorts(t, port, version), first)
}

// checkBlock checks that ports, described by what, are first then the rest
// of first's block of 100.
func checkBlock(t *testing.T, what string, ports []int, first int) {
	t.Helper()
	if len(ports) != 100 || ports[0] != first {
		t.Errorf("%s: ports start %v (%d of them), want %d then the rest of its block of 100", what, ports[:min(len(ports), 3)], len(ports), first)
		return
	}
	block := first / 100 * 100
	seen := map[int]bool{}
	for _, p := range ports {
		if p < block || p > block+99 || seen[p] {
			t.Errorf("%s: port %d is outside %d–%d or listed twice", what, p, block, block+99)
		}
		seen[p] = true
	}
}

func TestAnotherVersionOfAnEngineWithALowPortGetsAPortFromTheFirstThreeDigitsOfIt(t *testing.T) {
	tests := []struct {
		port    int
		version string
		first   int
	}{
		{6379, "6", 63706},
		{5432, "14", 54314},
		{3306, "8.4", 33084},
		{6549, "2", 65402},
	}
	for _, tt := range tests {
		checkPorts(t, tt.port, tt.version, tt.first)
	}
}

func TestAnotherVersionOfAnEngineWithAHighPortGetsAPortFrom50000AndTheFirstTwoDigitsOfIt(t *testing.T) {
	tests := []struct {
		port    int
		version string
		first   int
	}{
		{9000, "2", 59002},   // rustfs
		{7480, "0.9", 57409}, // the store test Engine
		{6554, "3", 56503},
		{6550, "3", 56503},
		{27017, "7", 52707}, // a 5-digit port
	}
	for _, tt := range tests {
		checkPorts(t, tt.port, tt.version, tt.first)
	}
}

// TestTheDefaultVersionOfAnEngineGetsItsStandardPortThenItsVersionBlock: the
// standard port is the default version's best port, but if another Engine's
// Service holds it (MySQL's and MariaDB's 3306) the default version falls
// back to the block another version of that name would get.
func TestTheDefaultVersionOfAnEngineGetsItsStandardPortThenItsVersionBlock(t *testing.T) {
	tests := []struct {
		port           int
		defaultVersion string
		block          int // the first port after the standard one
	}{
		{3306, "8.4", 33084},
		{6379, "7", 63707},
		{5432, "17", 54317},
		{9000, "1", 59001},  // rustfs
		{7379, "1", 57301},  // the kv test Engine
		{27017, "8", 52708}, // a 5-digit port
	}
	for _, tt := range tests {
		got := enginePortsAt(t, tt.port, tt.defaultVersion, tt.defaultVersion)
		if len(got) == 0 || got[0] != tt.port {
			t.Errorf("port %d, default version %s: ports start %v, want the standard port %d first", tt.port, tt.defaultVersion, got[:min(len(got), 3)], tt.port)
			continue
		}
		checkBlock(t, fmt.Sprintf("port %d, default version %s after its standard port", tt.port, tt.defaultVersion), got[1:], tt.block)
	}
}

func TestAnEngineWithoutAMappingRunsItsImageTaggedWithTheVersion(t *testing.T) {
	m, err := engine.Parse("search.toml", []byte(searchManifest))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := m.ImageRef("2.0"); err != nil || got != "docker.io/example/search:2.0" {
		t.Fatalf("version 2.0 runs %q (%v), want docker.io/example/search:2.0", got, err)
	}
}

func TestAManifestLanewayCannotUseIsRefusedSayingWhy(t *testing.T) {
	base := `
name = "cache"
default_version = "1"
image = "docker.io/example/cache"
port = 6000
data_dir = "/data"
`
	ready := "\n[[ready]]\nkind = \"redis-ping\"\n"
	noImage := strings.Replace(base, `image = "docker.io/example/cache"`, "", 1)
	tests := []struct {
		name, manifest, want string
	}{
		{"no name", strings.Replace(base, `name = "cache"`, "", 1) + ready, "name"},
		{"a name that isn't a variable", strings.Replace(base, `"cache"`, `"my-cache"`, 1) + ready, "name"},
		{"no image", strings.Replace(base, `image = "docker.io/example/cache"`, "", 1) + ready, "image"},
		{"no port", strings.Replace(base, "port = 6000", "", 1) + ready, "port"},
		{"a relative data_dir", strings.Replace(base, `"/data"`, `"data"`, 1) + ready, "data_dir"},
		{"no readiness probe", base, "ready"},
		{"an unknown probe kind", base + "\n[[ready]]\nkind = \"smoke-signal\"\n", "smoke-signal"},
		{"a banner probe expecting nothing", base + "\n[[ready]]\nkind = \"banner\"\n", "expect"},
		{"a command probe without a command", base + "\n[[ready]]\nkind = \"command\"\n", "command"},
		{"a probe on an unpublished port", base + "\n[[ready]]\nkind = \"http\"\nport = 9999\n", "9999"},
		{"an unknown key", base + "colour = \"red\"\n" + ready, "colour"},
		{"an unknown placeholder", base + "command = [\"cache\", \"{dbname}\"]\n" + ready, "{dbname}"},
		{"a database placeholder in the container's command", base + "command = [\"cache\", \"{db}\"]\n" + ready, "{db}"},
		{"a database without every command", base + ready + "\n[database]\nmax_name = 30\nexists = [\"x\", \"{db}\"]\n", "database.create"},
		{"a clone that names {db}", base + ready + "\n[database]\nmax_name = 30\n" +
			"exists = [\"x\"]\ncreate = [\"x\"]\nclone = [\"x\", \"{db}\"]\ndrop = [\"x\"]\nlist = [\"x\"]\n", "{db}"},
		{"a database without max_name", base + ready + "\n[database]\n" +
			"exists = [\"x\"]\ncreate = [\"x\"]\nclone = [\"x\"]\ndrop = [\"x\"]\nlist = [\"x\"]\n", "max_name"},
		{"a mapping without the default version", noImage + "\n[versions.2]\nimage = \"docker.io/example/cache:2.0.1\"\n" + ready, `default_version "1"`},
		{"an unknown key in the mapping", noImage + "\n[versions.1]\nimage = \"docker.io/example/cache:1.0.1\"\ncolour = \"red\"\n" + ready, "versions.1.colour"},
		{"a mapping to an image with neither tag nor digest", noImage + "\n[versions.1]\nimage = \"docker.io/example/cache\"\n" + ready, "versions.1.image"},
		{"a mapping to a malformed digest", noImage + "\n[versions.1]\nimage = \"docker.io/example/cache@sha256:abc\"\n" + ready, "versions.1.image"},
		{"a mapping without an image", noImage + "\n[versions.1]\n" + ready, "versions.1.image"},
		{"a mapped version that isn't a version", noImage + "\n[versions.1]\nimage = \"docker.io/example/cache:1.0.1\"\n" +
			"[versions.\"one point oh\"]\nimage = \"docker.io/example/cache:1.0.1\"\n" + ready, "one point oh"},
		{"both image and a mapping", base + "\n[versions.1]\nimage = \"docker.io/example/cache:1.0.1\"\n" + ready, "versions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := engine.Parse("cache.toml", []byte(tt.manifest))
			if err == nil {
				t.Fatalf("parsed, want an error about %s", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) || !strings.HasPrefix(err.Error(), "cache.toml") {
				t.Fatalf("error %q, want it to name cache.toml and %q", err, tt.want)
			}
		})
	}
}
