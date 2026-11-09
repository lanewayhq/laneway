package catalog_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lanewayhq/laneway/internal/catalog"
	"github.com/lanewayhq/laneway/internal/testkit"
)

// A fresh install that can't reach the Catalog names the Plugins a Project
// needs from the snapshot, so the snapshot must list every first-party
// Plugin of the lanewayhq/plugins version go.mod pins: bumping the pin to
// one with a new Plugin, such as rustfs, also means `go generate
// ./internal/catalog` once that Plugin is in a signed Catalog.
func TestTheSnapshotListsEveryFirstPartyPluginOfThePinnedPlugins(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("snapshot", catalog.SnapshotFile))
	if os.IsNotExist(err) {
		t.Skip("no Catalog snapshot yet")
	}
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(filepath.Join("snapshot", catalog.SnapshotFile+catalog.SignatureSuffix))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Verify(data, sig, catalog.DefaultKey)
	if err != nil {
		t.Fatalf("snapshot/%s: %v", catalog.SnapshotFile, err)
	}
	listed := map[string]bool{}
	for _, p := range cat.Plugins {
		listed[p.Name] = true
	}
	for _, name := range testkit.FirstPartyPlugins(t) {
		if !listed[name] {
			t.Errorf("the snapshot doesn't list %s, a first-party Plugin of the pinned lanewayhq/plugins: release it, then run `go generate ./internal/catalog`", name)
		}
	}
}
