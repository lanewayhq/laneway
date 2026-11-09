package catalog

import (
	"embed"
	"errors"
	"io/fs"
)

// The Catalog snapshot: a copy of the signed Catalog built into Laneway, so
// that a fresh install that can't reach the Catalog, and has no copy kept
// from an earlier fetch, still knows which Catalog Plugins a Project needs
// and can name them when Linking fails (spec #48, story 3). It is only a
// fallback: Laneway always prefers the Catalog it fetches, then the copy it
// kept, and it checks the snapshot's signature like theirs.
//
// The snapshot is snapshot/catalog.json and its signature beside it
// (SignatureSuffix), refreshed from the published Catalog before a release
// with:
//
//	go generate ./internal/catalog
//
// Until the first signed Catalog is published there is none: the directory
// holds only its README, and a fresh install that can't reach the Catalog
// Links with a warning that Plugins the Project needs may be missing.

//go:generate go run gensnapshot.go

// SnapshotFile is the Catalog snapshot in snapshot/, its signature beside
// it.
const SnapshotFile = "catalog.json"

//go:embed snapshot
var snapshotFiles embed.FS

// builtInSnapshot is the Catalog snapshot built into Laneway and its
// signature, or nil ones if there is none yet.
func builtInSnapshot() (data, sig []byte) {
	data, err := fs.ReadFile(snapshotFiles, "snapshot/"+SnapshotFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		panic(err) // embedded
	}
	sig, err = fs.ReadFile(snapshotFiles, "snapshot/"+SnapshotFile+SignatureSuffix)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		panic(err) // embedded
	}
	return data, sig
}
