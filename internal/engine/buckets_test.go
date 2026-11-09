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
drop = ["sh", "-c", "drop", "sh", "{bucket}"]
list = ["sh", "-c", "list --password={password}"]
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
