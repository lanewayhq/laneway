// Package catalog is the Catalog (spec #48, ADR-0007): the signed list of
// first-party Plugins that Laneway may fetch on its own when a Project needs
// one. It lists each Plugin's releases, with each release's SHA-256 and what
// it adds: its Recipes' and Integrations' detection rules, so Laneway can
// match a Project against Plugins it hasn't fetched, and its Engines' names,
// so it can find the Engines a Project's config names.
//
// The Catalog is a JSON document served beside a detached Ed25519 signature
// (SignatureSuffix). Laneway refuses a Catalog whose signature isn't made with
// the key built into it (DefaultKey), and a release whose SHA-256 isn't the
// one the Catalog lists, so taking over where the Catalog or a release is
// downloaded from isn't enough to get code onto a developer's machine.
package catalog

import (
	"bytes"
	"cmp"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/lanewayhq/laneway/internal/detect"
)

// Format is the version of the Catalog's format this Laneway reads.
const Format = 1

// SignatureSuffix is added to the Catalog's URL to get its signature's: the
// Ed25519 signature of the Catalog's exact bytes, base64-encoded.
const SignatureSuffix = ".sig"

// Catalog is the Catalog, as Laneway reads it once its signature is checked.
type Catalog struct {
	// Format is the format's version; only Format is read.
	Format int `json:"format"`
	// Plugins are the Plugins it lists, each once.
	Plugins []Plugin `json:"plugins"`
}

// Plugin is a Plugin the Catalog lists, with its releases.
type Plugin struct {
	Name     string    `json:"name"`
	Releases []Release `json:"releases"`
}

// Release is one release of a Plugin: where its archive is, its SHA-256,
// the Plugin API version it targets, and what it adds.
type Release struct {
	// Version is the Plugin's version in this release, such as "1.2.0";
	// the release's manifest must say the same.
	Version string `json:"version"`
	// APIVersion is the Plugin API version the release targets; only one
	// that this Laneway speaks may be fetched.
	APIVersion int `json:"api_version"`
	// URL is where its archive is: a gzipped tar of the Plugin's
	// directory, its manifest at the top. A relative URL is relative to the
	// Catalog's.
	URL string `json:"url"`
	// SHA256 is the archive's hash, hex-encoded.
	SHA256 string `json:"sha256"`
	// Recipes and Integrations are the Recipes and Integrations it adds,
	// with the detection rules its manifest declares for them.
	Recipes      []Detector `json:"recipes,omitempty"`
	Integrations []Detector `json:"integrations,omitempty"`
	// Engines are the names of the Engines it adds.
	Engines []string `json:"engines,omitempty"`
}

// Detector is a Recipe or Integration a release adds: its name and its
// detection rule.
type Detector struct {
	Name   string      `json:"name"`
	Detect detect.Rule `json:"detect"`
}

// ErrRefused is wrapped by every error refusing a Catalog or a release
// because it isn't what Laneway's key or the Catalog vouches for, as opposed
// to one that couldn't be fetched.
var ErrRefused = errors.New("refused")

// refused is an error that wraps ErrRefused.
type refused struct{ msg string }

func (e refused) Error() string        { return e.msg }
func (e refused) Is(target error) bool { return target == ErrRefused }

func refusedf(format string, args ...any) error {
	return refused{fmt.Sprintf(format, args...)}
}

// Verify checks that sig, as served beside the Catalog (SignatureSuffix), is
// key's signature of data, and only then reads data as a Catalog.
func Verify(data, sig []byte, key ed25519.PublicKey) (*Catalog, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return nil, refusedf("its signature isn't a base64-encoded Ed25519 signature")
	}
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, data, raw) {
		return nil, refusedf("it isn't signed with Laneway's Catalog key")
	}
	var c Catalog
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("it isn't a valid Catalog: %w", err)
	}
	if c.Format != Format {
		return nil, fmt.Errorf("it is in Catalog format %d, but this Laneway reads only format %d; update Laneway", c.Format, Format)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("it isn't a valid Catalog: %w", err)
	}
	return &c, nil
}

// Sign returns the signature to serve beside the Catalog data, signed with
// key: what Verify checks.
func Sign(data []byte, key ed25519.PrivateKey) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, data)) + "\n")
}

// name is what Plugin, Recipe, Integration and Engine names look like.
var name = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (c *Catalog) validate() error {
	seen := map[string]bool{}
	for i, p := range c.Plugins {
		if !name.MatchString(p.Name) {
			return fmt.Errorf("plugins[%d]: %q isn't a Plugin name", i, p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("plugins[%d]: Plugin %s is listed twice", i, p.Name)
		}
		seen[p.Name] = true
		for j, r := range p.Releases {
			at := fmt.Sprintf("plugins[%d] (%s) releases[%d]", i, p.Name, j)
			if _, ok := parseVersion(r.Version); !ok {
				return fmt.Errorf("%s: version %q isn't a version such as \"1.2.0\"", at, r.Version)
			}
			if r.URL == "" {
				return fmt.Errorf("%s: url isn't set", at)
			}
			if !sha256Hex.MatchString(r.SHA256) {
				return fmt.Errorf("%s: sha256 %q isn't a hex-encoded SHA-256", at, r.SHA256)
			}
			for _, d := range slices.Concat(r.Recipes, r.Integrations) {
				if !name.MatchString(d.Name) {
					return fmt.Errorf("%s: %q isn't a Recipe or Integration name", at, d.Name)
				}
				if err := d.Detect.Validate(); err != nil {
					return fmt.Errorf("%s (%s): %w", at, d.Name, err)
				}
			}
			for _, e := range r.Engines {
				if !name.MatchString(e) {
					return fmt.Errorf("%s: %q isn't an Engine name", at, e)
				}
			}
		}
	}
	return nil
}

// version is a release version: dot-separated numbers, maybe with a
// "-suffix" pre-release, which sorts before the release itself.
type version struct {
	parts []int
	pre   string
}

func parseVersion(s string) (version, bool) {
	s = strings.TrimPrefix(s, "v")
	core, pre, _ := strings.Cut(s, "-")
	var v version
	for _, part := range strings.Split(core, ".") {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return version{}, false
		}
		v.parts = append(v.parts, n)
	}
	v.pre = pre
	return v, true
}

// compareVersions compares two versions that parse (see parseVersion).
func compareVersions(a, b string) int {
	va, _ := parseVersion(a)
	vb, _ := parseVersion(b)
	for i := range max(len(va.parts), len(vb.parts)) {
		var x, y int
		if i < len(va.parts) {
			x = va.parts[i]
		}
		if i < len(vb.parts) {
			y = vb.parts[i]
		}
		if c := cmp.Compare(x, y); c != 0 {
			return c
		}
	}
	switch {
	case va.pre == vb.pre:
		return 0
	case va.pre == "":
		return 1
	case vb.pre == "":
		return -1
	}
	return cmp.Compare(va.pre, vb.pre)
}
