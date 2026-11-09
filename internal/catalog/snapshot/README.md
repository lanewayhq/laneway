# The Catalog snapshot

`catalog.json` and `catalog.json.sig` here are a copy of the signed Catalog that is built into Laneway (see `../snapshot.go`). A fresh install that can't reach the Catalog uses it to name the Catalog Plugins a Project needs when Linking fails.

Refresh it from the published Catalog before a release, and commit both files:

```sh
go generate ./internal/catalog
```

It downloads the Catalog from `catalog.DefaultURL` and writes it here only if it is signed with the key built into Laneway (`placeholderKey` in `../fetch.go`).

There is no snapshot until the first signed Catalog is published (lanewayhq/laneway#71). Until then, this README is all that's here, and a fresh install that can't reach the Catalog Links with a warning instead.
