package engine

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Version is what a Service of an Engine at one version runs, when the
// Engine maps its versions to images (Manifest.Versions), such as RustFS,
// whose image tags only full versions (1.0.4), at version "1".
type Version struct {
	// Image is a fully qualified image reference, with a tag
	// ("docker.io/rustfs/rustfs:1.0.4") or a digest
	// ("docker.io/rustfs/rustfs@sha256:…").
	Image string `toml:"image" json:"image"`
}

// ImageRef is the image a Service of m at version runs: the version's
// image, if m maps its versions to images, or else m.Image tagged with the
// version. It is an error naming m for a version m doesn't map.
//
// A Service, its volume and its port are keyed by its version, never by its
// image, so a Plugin update that maps a version to a new image keeps the
// Service's data and port.
func (m *Manifest) ImageRef(version string) (string, error) {
	if len(m.Versions) == 0 {
		return m.Image + ":" + version, nil
	}
	if v, ok := m.Versions[version]; ok {
		return v.Image, nil
	}
	return "", fmt.Errorf("the %s Engine has no version %q; its versions are %s", m.Name, version, m.versionList())
}

// versionList lists m's mapped versions, for messages: "1", "2".
func (m *Manifest) versionList() string {
	return `"` + strings.Join(slices.Sorted(maps.Keys(m.Versions)), `", "`) + `"`
}

// version is what a Service version may be, as config allows it.
var version = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// imageRef is a fully qualified image reference with a tag or a digest: a
// registry host (with a port, perhaps), a repository path, and :tag or
// @sha256:digest.
var imageRef = regexp.MustCompile(`^` +
	`[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)*(?::[0-9]+)?` + // registry
	`(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)+` + // repository
	`(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}|@sha256:[0-9a-f]{64})$`) // tag or digest

// checkVersions reports the first thing wrong with m's mapping of versions
// to images, if it has one.
func (m *Manifest) checkVersions() error {
	if len(m.Versions) == 0 {
		return nil
	}
	for _, v := range slices.Sorted(maps.Keys(m.Versions)) {
		if !version.MatchString(v) {
			return fmt.Errorf("versions.%s: a version must be letters, digits, ., _ and -, such as \"7\" or \"7.2\", not %q", v, v)
		}
		ref := m.Versions[v].Image
		registry, _, _ := strings.Cut(ref, "/")
		if !imageRef.MatchString(ref) || !strings.ContainsAny(registry, ".:") && registry != "localhost" {
			return fmt.Errorf("versions.%s.image must be a fully qualified image reference with a tag or a digest, "+
				"such as \"docker.io/rustfs/rustfs:1.0.4\" or \"docker.io/rustfs/rustfs@sha256:…\", not %q", v, ref)
		}
	}
	if _, ok := m.Versions[m.DefaultVersion]; !ok {
		return fmt.Errorf("versions has no default_version %q: map it to the image the %s Engine runs by default; its versions are %s",
			m.DefaultVersion, m.Name, m.versionList())
	}
	return nil
}
