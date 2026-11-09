package recipe_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/engine"
	"github.com/lanewayhq/laneway/internal/recipe"
)

// resolveWithout resolves testdata/<fixture> as if the Engine name weren't
// installed and fetchable were the Engines Laneway can fetch from the Catalog.
func resolveWithout(t *testing.T, fixture, name string, fetchable ...string) error {
	t.Helper()
	dir := filepath.Join("testdata", fixture)
	layers, err := config.LoadLayers(dir)
	if err != nil {
		t.Fatal(err)
	}
	engines := slices.DeleteFunc(slices.Clone(firstPartyEngines()), func(e *engine.Manifest) bool { return e.Name == name })
	_, err = recipe.Resolve(recipe.Input{Engines: engines, FetchableEngines: fetchable, Dir: dir, Layers: layers})
	return err
}

func TestConfigNamingAnEngineInTheCatalogButNotInstalledSaysToRunLaneDoctor(t *testing.T) {
	tests := []struct {
		fixture, engine, want string
	}{
		{"database-postgres", "postgres",
			"database.engine: the postgres Engine isn't installed; run `lane doctor` to fetch it from the Catalog"},
		{"services-layers", "redis",
			"services.redis: the redis Engine isn't installed; run `lane doctor` to fetch it from the Catalog"},
		{"catalog-engine-env", "redis",
			"env.REDIS_PORT: the redis Engine isn't installed; run `lane doctor` to fetch it from the Catalog"},
		{"catalog-engine-linked-env", "mailpit",
			"linked_env.MAIL_HOST: the mailpit Engine isn't installed; run `lane doctor` to fetch it from the Catalog"},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			err := resolveWithout(t, tt.fixture, tt.engine, "memcached", tt.engine)
			if err == nil {
				t.Fatal("resolved without an error")
			}
			want := filepath.Join("testdata", tt.fixture, config.FileName) + ": " + tt.want
			if err.Error() != want {
				t.Errorf("error = %q, want %q", err, want)
			}
		})
	}
}

// Only an Engine with users has {<engine>_username}, which Laneway can't
// tell before fetching it, so the error doesn't claim it has one.
func TestAUsernameVariableOfAnEngineInTheCatalogButNotInstalledSaysLanewayCantTellIfItHasOne(t *testing.T) {
	err := resolveWithout(t, "catalog-engine-username", "redis", "redis")
	want := filepath.Join("testdata", "catalog-engine-username", config.FileName) + ": env.REDIS_USER: " +
		"the redis Engine isn't installed, so Laneway can't tell whether it has {redis_username}; " +
		"run `lane doctor` to fetch it from the Catalog"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

// Only an Engine with Bucket commands has {<engine>_bucket}, likewise.
func TestABucketVariableOfAnEngineInTheCatalogButNotInstalledSaysLanewayCantTellIfItHasOne(t *testing.T) {
	err := resolveWithout(t, "catalog-engine-bucket", "redis", "redis")
	want := filepath.Join("testdata", "catalog-engine-bucket", config.FileName) + ": env.AWS_BUCKET: " +
		"the redis Engine isn't installed, so Laneway can't tell whether it has {redis_bucket}; " +
		"run `lane doctor` to fetch it from the Catalog"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

// And so does {<engine>_bucket_<name>} for a Bucket config declares.
func TestADeclaredBucketsVariableOfAnEngineInTheCatalogButNotInstalledSaysLanewayCantTellIfItHasOne(t *testing.T) {
	err := resolveWithout(t, "catalog-engine-declared-bucket", "redis", "redis")
	want := filepath.Join("testdata", "catalog-engine-declared-bucket", config.FileName) + ": env.PUBLIC_BUCKET: " +
		"the redis Engine isn't installed, so Laneway can't tell whether it has {redis_bucket_public}; " +
		"run `lane doctor` to fetch it from the Catalog"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

func TestConfigNamingAnEngineNeitherInstalledNorInTheCatalogStillCallsItUnknown(t *testing.T) {
	tests := []struct {
		fixture, engine, want string
	}{
		{"database-postgres", "postgres", `database.engine must be sqlite or mysql, not "postgres"`},
		{"services-layers", "redis", `services.redis: Laneway has no Service "redis"`},
		{"catalog-engine-env", "redis", "env.REDIS_PORT: unknown template variable {redis_port}"},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			err := resolveWithout(t, tt.fixture, tt.engine, "memcached")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to say %q", err, tt.want)
			}
		})
	}
}
