package recipe_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/recipe"
)

func TestATestDatabaseIsMadeOnlyWhenDatabaseTestIsOn(t *testing.T) {
	tests := []struct {
		fixture string
		want    bool
	}{
		{"database-test-on", true},
		{"database-test-off", false},
		// Without a Recipe that decides it (the Laravel Recipe does), auto is off.
		{"database-test-auto", false},
		{"database-postgres", false},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			if got := mustResolveFixture(t, tt.fixture).TestDatabase; got != tt.want {
				t.Errorf("TestDatabase = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestInvalidTestDatabaseConfigNamesTheFileAndTheKey(t *testing.T) {
	tests := []struct {
		fixture string
		want    []string // substrings of the error, besides the file
	}{
		{"invalid-database-test", []string{"database.test", "on, off or auto", `"yes"`}},
		{"invalid-database-test-sqlite", []string{"database.test", "only for databases in a Service", "sqlite"}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			_, err := resolveFixture(t, tt.fixture)
			if err == nil {
				t.Fatal("resolved without an error")
			}
			msg := err.Error()
			for _, want := range append([]string{filepath.Join("testdata", tt.fixture, config.FileName)}, tt.want...) {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q doesn't mention %q", msg, want)
				}
			}
		})
	}
}

func TestTheTestDatabaseSettingWrittenAsALayerResolvesToItself(t *testing.T) {
	for _, fixture := range []string{"database-test-on", "database-test-off", "database-test-auto"} {
		t.Run(fixture, func(t *testing.T) {
			res := mustResolveFixture(t, fixture)

			var buf strings.Builder
			if err := config.Encode(&buf, res.Layer()); err != nil {
				t.Fatal(err)
			}
			written, err := config.ParseLayer("written", []byte(buf.String()))
			if err != nil {
				t.Fatalf("reading back:\n%s\n%v", buf.String(), err)
			}
			again, err := recipe.Resolve(recipe.Input{Layers: []config.Layer{written}})
			if err != nil {
				t.Fatalf("resolving what was written:\n%s\n%v", buf.String(), err)
			}
			if again.Database != res.Database || again.TestDatabase != res.TestDatabase {
				t.Errorf("written as:\n%s\nresolves to %+v %t, want %+v %t", buf.String(),
					again.Database, again.TestDatabase, res.Database, res.TestDatabase)
			}
		})
	}
}
