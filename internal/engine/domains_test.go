package engine_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/engine"
)

const storeManifest = `
name = "store"
default_version = "1"
image = "docker.io/example/store"
port = 9000
web_port = 9001
data_dir = "/data"

[[ready]]
kind = "http"

[[domains]]
label = "s3"
port = 9000

[[domains]]
label = "store-console"
port = 9001
`

func TestAnEngineManifestDeclaresServiceDomainsForItsPublishedPorts(t *testing.T) {
	m, err := engine.Parse("store.toml", []byte(storeManifest))
	if err != nil {
		t.Fatal(err)
	}
	want := []engine.ServiceDomain{{Label: "s3", Port: 9000}, {Label: "store-console", Port: 9001}}
	if !slices.Equal(m.Domains, want) {
		t.Fatalf("Service Domains %+v, want %+v", m.Domains, want)
	}
}

func TestTheFirstServiceDomainsURLIsTheEnginesEndpointVariable(t *testing.T) {
	m, err := engine.Parse("store.toml", []byte(storeManifest))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.Variables(), "store_endpoint") {
		t.Fatalf("variables %q, want store_endpoint among them", m.Variables())
	}
	if got := m.Endpoint("laneway.localhost"); got != "https://s3.laneway.localhost" {
		t.Fatalf("endpoint %q, want https://s3.laneway.localhost", got)
	}

	plain, err := engine.Parse("cache.toml", []byte(cacheManifest+cacheReady))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plain.Variables(), "cache_endpoint") {
		t.Fatalf("an Engine without Service Domains has the variables %q", plain.Variables())
	}
}

const cacheManifest = `
name = "cache"
default_version = "1"
image = "docker.io/example/cache"
port = 6000
web_port = 6001
data_dir = "/data"
`

const cacheReady = "\n[[ready]]\nkind = \"redis-ping\"\n"

func TestAServiceDomainLanewayCannotServeIsRefusedSayingWhy(t *testing.T) {
	domain := func(label string, port int) string {
		return fmt.Sprintf("\n[[domains]]\nlabel = %q\nport = %d\n", label, port)
	}
	tests := []struct {
		name, domains, want string
	}{
		{"no label", "\n[[domains]]\nport = 6001\n", "domains[0].label"},
		{"a label with a dot", domain("mail.inbox", 6001), "mail.inbox"},
		{"a label with an upper-case letter", domain("Mail", 6001), "Mail"},
		{"a label starting with a hyphen", domain("-mail", 6001), "-mail"},
		{"a label longer than 63 characters", domain(strings.Repeat("a", 64), 6001), "63"},
		{"no port", "\n[[domains]]\nlabel = \"mail\"\n", "domains[0].port"},
		{"an unpublished port", domain("mail", 9999), "9999"},
		{"a label declared twice", domain("mail", 6000) + domain("mail", 6001), "domains[1].label"},
		{"an unknown key", "\n[[domains]]\nlabel = \"mail\"\nport = 6001\npath = \"/inbox\"\n", "path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := engine.Parse("cache.toml", []byte(cacheManifest+cacheReady+tt.domains))
			if err == nil {
				t.Fatalf("parsed, want an error about %s", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) || !strings.HasPrefix(err.Error(), "cache.toml") {
				t.Fatalf("error %q, want it to name cache.toml and %q", err, tt.want)
			}
		})
	}
}
