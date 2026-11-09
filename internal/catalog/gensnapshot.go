//go:build ignore

// Gensnapshot refreshes the Catalog snapshot built into Laneway (snapshot/,
// see snapshot.go) from the published Catalog: it downloads the Catalog at
// catalog.DefaultURL and its signature, and writes them into snapshot/ only
// if they are signed with the key built into Laneway (catalog.DefaultKey).
//
//	go generate ./internal/catalog
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lanewayhq/laneway/internal/catalog"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gensnapshot:", err)
		os.Exit(1)
	}
}

func run() error {
	tmp, err := os.MkdirTemp("", "laneway-catalog-snapshot-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	// A Location with a URL has no snapshot to fall back on, and tmp has no
	// copy kept: Load succeeds only with the published Catalog, verified,
	// and keeps exactly what it fetched in tmp.
	src := catalog.NewSource(catalog.Location{URL: catalog.DefaultURL, Key: catalog.DefaultKey}, tmp)
	cat, err := src.Load(context.Background())
	if err != nil {
		return fmt.Errorf("%w; the snapshot is unchanged", err)
	}
	for _, name := range []string{catalog.SnapshotFile, catalog.SnapshotFile + catalog.SignatureSuffix} {
		data, err := os.ReadFile(filepath.Join(tmp, name))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join("snapshot", name), data, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("snapshot/%s: the Catalog at %s, listing %d Plugins\n", catalog.SnapshotFile, catalog.DefaultURL, len(cat.Plugins))
	return nil
}
