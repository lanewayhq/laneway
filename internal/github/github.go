// Package github downloads third-party Plugins from GitHub releases, for
// `lane plugin add github.com/<owner>/<repo>` (spec #48, ADR-0007). Such a
// Plugin is outside the Catalog, so nothing vouches for it but the
// developer who adds it: Laneway installs it only when asked, and pins it to
// the hash it had then until `lane plugin update`.
package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/lanewayhq/laneway/internal/catalog"
)

// DefaultURL is where releases are downloaded from.
const DefaultURL = "https://github.com"

// Asset is the name of the release asset holding the Plugin: a gzipped tar
// of the Plugin's directory, its manifest at the top, as in the Catalog.
const Asset = "plugin.tar.gz"

// Limits on a download, so a misbehaving server can't fill the disk or
// memory, or keep `lane plugin add` waiting for ever.
const (
	maxRelease = 128 << 20
	timeout    = 2 * time.Minute
)

// Source is a Plugin's repository on GitHub, and the release to install:
// its latest when Tag is "".
type Source struct {
	Owner, Repo, Tag string
}

// prefix is how a Source is written: github.com/<owner>/<repo>[@<tag>].
const prefix = "github.com/"

var (
	ownerRepo = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?/[A-Za-z0-9._-]+$`)
	tag       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
)

// IsSource reports whether s is meant as a GitHub Source (it starts with
// github.com/, maybe after https://), whether or not it is a valid one.
func IsSource(s string) bool {
	return strings.HasPrefix(strings.TrimPrefix(s, "https://"), prefix)
}

// Parse reads a Source written as github.com/<owner>/<repo>, maybe followed
// by @<tag> for a release other than the latest, and maybe after https://.
func Parse(s string) (Source, error) {
	rest, ok := strings.CutPrefix(strings.TrimPrefix(s, "https://"), prefix)
	if !ok {
		return Source{}, fmt.Errorf("%q isn't a GitHub repository such as github.com/<owner>/<repo>", s)
	}
	rest, t, hasTag := strings.Cut(rest, "@")
	rest = strings.TrimSuffix(strings.TrimSuffix(rest, "/"), ".git")
	if !ownerRepo.MatchString(rest) || (hasTag && !tag.MatchString(t)) {
		return Source{}, fmt.Errorf("%q isn't a GitHub repository such as github.com/<owner>/<repo>, or github.com/<owner>/<repo>@<tag> for one release", s)
	}
	owner, repo, _ := strings.Cut(rest, "/")
	return Source{Owner: owner, Repo: repo, Tag: t}, nil
}

// String writes s as Parse reads it.
func (s Source) String() string {
	out := prefix + s.Owner + "/" + s.Repo
	if s.Tag != "" {
		out += "@" + s.Tag
	}
	return out
}

// Releases downloads releases from GitHub, or from wherever tests serve
// theirs.
type Releases struct {
	base   string
	client *http.Client
}

// New returns Releases downloaded from base (DefaultURL if "").
func New(base string) *Releases {
	if base == "" {
		base = DefaultURL
	}
	return &Releases{base: strings.TrimSuffix(base, "/"), client: &http.Client{}}
}

// Fetch downloads the release src names and unpacks its Plugin into dir,
// which must exist and be empty. It returns the archive's SHA-256.
func (r *Releases) Fetch(ctx context.Context, src Source, dir string) (string, error) {
	u := fmt.Sprintf("%s/%s/%s/releases/latest/download/%s", r.base, src.Owner, src.Repo, Asset)
	release := "latest release"
	if src.Tag != "" {
		u = fmt.Sprintf("%s/%s/%s/releases/download/%s/%s", r.base, src.Owner, src.Repo, src.Tag, Asset)
		release = "release " + src.Tag
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	res, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading the %s of %s: %w", release, src, err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return "", fmt.Errorf("%s has no %s with a %s asset (looked at %s); a Plugin's releases must include its directory packed as %s",
			src, release, Asset, u, Asset)
	case res.StatusCode != http.StatusOK:
		return "", fmt.Errorf("downloading the %s of %s: GET %s: %s", release, src, u, res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxRelease+1))
	if err != nil {
		return "", fmt.Errorf("downloading the %s of %s: %w", release, src, err)
	}
	if len(data) > maxRelease {
		return "", fmt.Errorf("the %s of %s is larger than the %d bytes Laneway accepts", release, src, maxRelease)
	}
	if err := catalog.Unpack(data, dir); err != nil {
		return "", fmt.Errorf("unpacking the %s of %s: %w", release, src, err)
	}
	return catalog.SHA256(data), nil
}
