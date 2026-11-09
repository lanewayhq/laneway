package engine_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/engine"
)

const objectsManifest = `
name = "objects"
default_version = "1"
image = "docker.io/example/objects"
port = 9000
data_dir = "/data"
user = "admin"

[[ready]]
kind = "http"
path = "/health/ready"

[bucket]
exists = ["sh", "-c", "exists", "sh", "{bucket}"]
create = ["sh", "-c", "create", "sh", "{bucket}", "{user}"]
clone = ["sh", "-c", "clone", "sh", "{from}", "{to}"]
drop = ["sh", "-c", "drop", "sh", "{bucket}"]
list = ["sh", "-c", "list --password={password}"]
size = ["sh", "-c", "size", "sh", "{bucket}"]
configure = ["sh", "-c", "configure", "sh", "{bucket}", "{public}", "{origins}"]
`

func TestAnEngineManifestMayDeclareBucketCommands(t *testing.T) {
	m, err := engine.Parse("objects.toml", []byte(objectsManifest))
	if err != nil {
		t.Fatal(err)
	}
	b := m.Bucket
	if b == nil {
		t.Fatal("no Bucket commands, want the [bucket] table's")
	}
	if got := m.Expand(b.Create, map[string]string{"bucket": "shop--feature"}); !slices.Equal(got, []string{"sh", "-c", "create", "sh", "shop--feature", "admin"}) {
		t.Fatalf("expanded create command %q", got)
	}
	if got := m.Expand(b.Clone, map[string]string{"from": "shop", "to": "shop--feature"}); !slices.Equal(got, []string{"sh", "-c", "clone", "sh", "shop", "shop--feature"}) {
		t.Fatalf("expanded clone command %q", got)
	}
	if got := m.Expand(b.Size, map[string]string{"bucket": "shop"}); !slices.Equal(got, []string{"sh", "-c", "size", "sh", "shop"}) {
		t.Fatalf("expanded size command %q", got)
	}
	if got := m.Expand(b.Configure, map[string]string{"bucket": "shop-public", "public": "true", "origins": "https://shop.localhost https://feature.shop.localhost"}); !slices.Equal(got, []string{"sh", "-c", "configure", "sh", "shop-public", "true", "https://shop.localhost https://feature.shop.localhost"}) {
		t.Fatalf("expanded configure command %q", got)
	}
	if got := m.Expand(b.List, nil); !slices.Equal(got, []string{"sh", "-c", "list --password=laneway"}) {
		t.Fatalf("expanded list command %q", got)
	}
	if !slices.Contains(m.Variables(), "objects_bucket") {
		t.Fatalf("variables %q, want {objects_bucket} among them", m.Variables())
	}

	plain, err := engine.Parse("search.toml", []byte(searchManifest))
	if err != nil {
		t.Fatal(err)
	}
	if plain.Bucket != nil || slices.Contains(plain.Variables(), "search_bucket") {
		t.Fatalf("an Engine without [bucket] has Bucket commands %+v and variables %q", plain.Bucket, plain.Variables())
	}
}

func TestBucketCommandsLanewayCannotUseAreRefusedSayingWhy(t *testing.T) {
	without := func(key string) string {
		var lines []string
		for line := range strings.Lines(objectsManifest) {
			if !strings.HasPrefix(line, key+" = ") {
				lines = append(lines, line)
			}
		}
		return strings.Join(lines, "")
	}
	tests := []struct {
		name, manifest, want string
	}{
		{"no exists", without("exists"), "bucket.exists is missing"},
		{"no create", without("create"), "bucket.create is missing"},
		{"no clone", without("clone"), "bucket.clone is missing: the Bucket commands are exists, create, clone, configure, drop, list and size"},
		{"no configure", without("configure"), "bucket.configure is missing"},
		{"a copy's source in configure", strings.Replace(objectsManifest, `"{bucket}", "{public}"`, `"{from}", "{public}"`, 1), "bucket.configure uses {from}"},
		{"origins in create", strings.Replace(objectsManifest, `"{bucket}", "{user}"`, `"{bucket}", "{origins}"`, 1), "bucket.create uses {origins}"},
		{"no size", without("size"), "bucket.size is missing"},
		{"a Bucket in clone", strings.Replace(objectsManifest, `"{from}", "{to}"`, `"{bucket}", "{to}"`, 1), "bucket.clone uses {bucket}"},
		{"a copy's source in size", strings.Replace(objectsManifest, `"size", "sh", "{bucket}"`, `"size", "sh", "{from}"`, 1), "bucket.size uses {from}"},
		{"no drop", without("drop"), "bucket.drop is missing"},
		{"no list", without("list"), "bucket.list is missing"},
		{"an unknown key", objectsManifest + "colour = [\"red\"]\n", "bucket.colour"},
		{"a database placeholder", strings.Replace(objectsManifest, `"drop", "sh", "{bucket}"`, `"drop", "sh", "{db}"`, 1), "{db}"},
		{"a Bucket in list", strings.Replace(objectsManifest, `"list --password={password}"`, `"list {bucket}"`, 1), "bucket.list uses {bucket}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := engine.Parse("objects.toml", []byte(tt.manifest))
			if err == nil {
				t.Fatalf("parsed, want an error about %s", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) || !strings.HasPrefix(err.Error(), "objects.toml") {
				t.Fatalf("error %q, want it to name objects.toml and %q", err, tt.want)
			}
		})
	}
}
