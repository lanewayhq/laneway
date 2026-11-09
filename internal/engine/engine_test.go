package engine_test

import (
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

func TestAManifestLanewayCannotUseIsRefusedSayingWhy(t *testing.T) {
	base := `
name = "cache"
default_version = "1"
image = "docker.io/example/cache"
port = 6000
data_dir = "/data"
`
	ready := "\n[[ready]]\nkind = \"redis-ping\"\n"
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
