package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"syscall"
	"time"
)

// DefaultURL is where Laneway fetches the Catalog from: the latest release of
// lanewayhq/plugins, which publishes it after the maintainer approves its
// signing (see that repository's docs/releasing.md).
const DefaultURL = "https://github.com/lanewayhq/plugins/releases/latest/download/catalog.json"

// catalogKey is the base64 Ed25519 public key built into Laneway: the public
// half of lanewayhq/plugins' Catalog signing key (its keys/catalog.pub).
// Laneway refuses any Catalog not signed with it. Changing it needs a Laneway
// release, and Laneways built before then refuse Catalogs signed with the new
// key (see lanewayhq/plugins' keys/README.md). After changing it, refresh
// the Catalog snapshot (snapshot.go), which must be signed with it.
const catalogKey = "3jCN1VXjJCoBRm+mO2vDBfi4gPou8HaDqKWtPEweLtE="

// DefaultKey is the key the Catalog must be signed with: the one built into
// Laneway.
var DefaultKey = func() ed25519.PublicKey {
	key, err := base64.StdEncoding.DecodeString(catalogKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		panic("catalog: the built-in key isn't a base64 Ed25519 public key")
	}
	return key
}()

// Limits on what is downloaded, so a misbehaving server can't fill the disk
// or memory.
const (
	maxCatalog = 8 << 20
	maxRelease = 128 << 20
)

// Timeouts for downloads, every attempt included: Linking waits for them.
const (
	catalogTimeout = 20 * time.Second
	releaseTimeout = 3 * time.Minute
)

// retryPolicy is how many times, and how patiently, a download is tried
// when it fails temporarily, such as with a 5xx status (see temporary).
type retryPolicy struct {
	// attempts is how many times a download is tried at most.
	attempts int
	// backoff is the wait before the second attempt, doubled before each
	// one after.
	backoff time.Duration
	// catalogAttempt and releaseAttempt are how long one attempt at
	// downloading the Catalog (or its signature) and a release may take,
	// so a hung attempt leaves time for another.
	catalogAttempt, releaseAttempt time.Duration
}

// defaultRetry tries a download three times, over about three seconds when
// it fails fast, as GitHub's intermittent 504s do. A hung attempt takes
// longer, but catalogTimeout and releaseTimeout still bound the whole.
var defaultRetry = retryPolicy{attempts: 3, backoff: time.Second, catalogAttempt: 5 * time.Second, releaseAttempt: 2 * time.Minute}

// Location is where the Catalog is fetched from, the key it must be signed
// with, and the snapshot of it to fall back on. The zero Location is
// Laneway's own (DefaultURL, DefaultKey, and the snapshot built into
// Laneway); tests serve their own Catalog signed with a test key.
type Location struct {
	URL string
	Key ed25519.PublicKey
	// Snapshot is a copy of the Catalog, and SnapshotSig its signature,
	// read when neither the Catalog nor a copy kept from an earlier fetch
	// can be (see snapshot.go). Without a URL, it is the one built into
	// Laneway, if there is one yet.
	Snapshot, SnapshotSig []byte
}

// Source fetches the Catalog and its Plugins' releases. It keeps the last
// Catalog it verified in a directory, so that Laneway still knows what a
// Project needs when it can't reach the Catalog.
type Source struct {
	loc    Location
	dir    string
	client *http.Client
	retry  retryPolicy
}

// cacheFile is the copy of the last Catalog verified, and its signature
// beside it (SignatureSuffix), in a Source's directory: named like the
// snapshot, which gensnapshot.go makes from such a copy.
const cacheFile = SnapshotFile

// NewSource returns a Source for loc that keeps its copy of the Catalog in
// dir.
func NewSource(loc Location, dir string) *Source {
	if loc.URL == "" {
		loc.URL = DefaultURL
		if loc.Snapshot == nil {
			loc.Snapshot, loc.SnapshotSig = builtInSnapshot()
		}
	}
	if loc.Key == nil {
		loc.Key = DefaultKey
	}
	return &Source{loc: loc, dir: dir, client: &http.Client{}, retry: defaultRetry}
}

// URL is where the Catalog is fetched from.
func (s *Source) URL() string { return s.loc.URL }

// Load fetches the Catalog and checks its signature, keeping a copy. When it
// can't (Laneway is offline, or the Catalog is refused), it returns the copy
// kept last time, or else the Location's snapshot, if there is one, along
// with the error saying why it couldn't. cat is nil only when there's none
// of them.
func (s *Source) Load(ctx context.Context) (cat *Catalog, err error) {
	cat, data, sig, err := s.fetch(ctx)
	if err == nil {
		if werr := s.keep(data, sig); werr != nil {
			return cat, fmt.Errorf("keeping a copy of the Catalog: %w", werr)
		}
		return cat, nil
	}
	err = fmt.Errorf("the Catalog at %s: %w", s.loc.URL, err)
	if kept, kerr := s.kept(); kerr == nil {
		return kept, err
	}
	if s.loc.Snapshot != nil {
		if snap, serr := Verify(s.loc.Snapshot, s.loc.SnapshotSig, s.loc.Key); serr == nil {
			return snap, err
		}
	}
	return nil, err
}

func (s *Source) fetch(ctx context.Context) (*Catalog, []byte, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, catalogTimeout)
	defer cancel()
	data, err := s.get(ctx, s.loc.URL, maxCatalog, s.retry.catalogAttempt)
	if err != nil {
		return nil, nil, nil, err
	}
	sig, err := s.get(ctx, s.loc.URL+SignatureSuffix, 4<<10, s.retry.catalogAttempt)
	if err != nil {
		return nil, nil, nil, err
	}
	cat, err := Verify(data, sig, s.loc.Key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("Laneway refused it: %w", err)
	}
	return cat, data, sig, nil
}

// keep saves a verified Catalog and its signature as the copy to fall back
// on.
func (s *Source) keep(data, sig []byte) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		data []byte
	}{{cacheFile + SignatureSuffix, sig}, {cacheFile, data}} {
		tmp := filepath.Join(s.dir, "."+f.name+".tmp")
		if err := os.WriteFile(tmp, f.data, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(s.dir, f.name)); err != nil {
			return err
		}
	}
	return nil
}

// kept reads the copy kept last time, checking its signature again.
func (s *Source) kept() (*Catalog, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, cacheFile))
	if err != nil {
		return nil, err
	}
	sig, err := os.ReadFile(filepath.Join(s.dir, cacheFile+SignatureSuffix))
	if err != nil {
		return nil, err
	}
	return Verify(data, sig, s.loc.Key)
}

// Fetch downloads the release r, checks its SHA-256 is the one the Catalog
// lists, and unpacks it into dir, which must exist and be empty.
func (s *Source) Fetch(ctx context.Context, r Release, dir string) error {
	u, err := s.resolve(r.URL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	data, err := s.get(ctx, u, maxRelease, s.retry.releaseAttempt)
	if err != nil {
		return err
	}
	if sum := SHA256(data); sum != r.SHA256 {
		return refusedf("Laneway refused the release downloaded from %s: its SHA-256 is %s, but the Catalog lists %s", u, sum, r.SHA256)
	}
	if err := unpack(data, dir); err != nil {
		return fmt.Errorf("unpacking the release downloaded from %s: %w", u, err)
	}
	return nil
}

// resolve makes a release URL absolute, relative to the Catalog's.
func (s *Source) resolve(ref string) (string, error) {
	base, err := url.Parse(s.loc.URL)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("the release URL %q: %w", ref, err)
	}
	return base.ResolveReference(r).String(), nil
}

// get downloads u, refusing more than limit bytes. A temporary failure is
// tried again, as the Source's retry policy says, each attempt taking at
// most timeout; any other failure, or ctx ending, stops it at once.
func (s *Source) get(ctx context.Context, u string, limit int64, timeout time.Duration) ([]byte, error) {
	wait := s.retry.backoff
	for attempt := 1; ; attempt++ {
		data, err := s.getOnce(ctx, u, limit, timeout)
		if err == nil || ctx.Err() != nil || !temporary(err) {
			return data, err
		}
		if attempt >= s.retry.attempts {
			return nil, fmt.Errorf("%w (tried %d times)", err, attempt)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, err
		case <-t.C:
		}
		wait *= 2
	}
}

// getOnce is one attempt at get.
func (s *Source) getOnce(ctx context.Context, u string, limit int64, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, errAttemptTimedOut)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, markIfTemporary(ctx, u, timeout, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		err := fmt.Errorf("GET %s: %s", u, res.Status)
		if res.StatusCode >= 500 {
			err = temporaryError{err}
		}
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, markIfTemporary(ctx, u, timeout, fmt.Errorf("GET %s: %w", u, err))
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("GET %s: it is larger than the %d bytes Laneway accepts", u, limit)
	}
	return data, nil
}

// errAttemptTimedOut ends an attempt at a download that took too long.
var errAttemptTimedOut = errors.New("the attempt timed out")

// markIfTemporary decides whether err, from an attempt at downloading u
// that may take timeout, in ctx, is temporary, and marks it so if it is:
// when the attempt timed out or its connection was dropped (closed before
// the whole response came, or reset).
func markIfTemporary(ctx context.Context, u string, timeout time.Duration, err error) error {
	if context.Cause(ctx) == errAttemptTimedOut {
		return temporaryError{fmt.Errorf("GET %s: it took longer than %s", u, timeout)}
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return temporaryError{err}
	}
	for _, dropped := range []error{io.EOF, io.ErrUnexpectedEOF, syscall.ECONNRESET, syscall.ECONNABORTED, syscall.EPIPE} {
		if errors.Is(err, dropped) {
			return temporaryError{err}
		}
	}
	return err
}

// temporaryError marks a failed download as worth trying again.
type temporaryError struct{ error }

func (e temporaryError) Unwrap() error { return e.error }

// temporary is whether a failed download may succeed if tried again.
func temporary(err error) bool {
	_, ok := errors.AsType[temporaryError](err)
	return ok
}

// Unpack writes the Plugin in a release archive (as Pack makes) into dir,
// which must exist and be empty: for Plugins released elsewhere in the same
// format, such as on GitHub.
func Unpack(data []byte, dir string) error { return unpack(data, dir) }

// unpack writes the regular files and directories of the gzipped tar data
// into dir. Anything else, or a path outside dir, is refused.
func unpack(data []byte, dir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(h.Name)
		if name == "." {
			continue
		}
		if !fs.ValidPath(name) {
			return fmt.Errorf("%q isn't a path inside the Plugin's directory", h.Name)
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s isn't a regular file or directory", h.Name)
		}
	}
}

// Pack is unpack's opposite: it makes a release archive (a gzipped tar) of
// the files in dir, for publishing a Plugin in the Catalog.
func Pack(dir string) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if !e.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SHA256 is data's hash as a Release lists it.
func SHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
