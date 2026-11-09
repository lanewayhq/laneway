package catalog

import "testing"

func TestTheSnapshotBuiltIntoLanewayIsSignedWithItsKey(t *testing.T) {
	data, sig := builtInSnapshot()
	if data == nil {
		t.Skip("no Catalog snapshot yet: run `go generate ./internal/catalog` once the first signed Catalog is published")
	}
	if _, err := Verify(data, sig, DefaultKey); err != nil {
		t.Fatalf("snapshot/%s: %v; refresh it with `go generate ./internal/catalog`", SnapshotFile, err)
	}
}
