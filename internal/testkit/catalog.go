package testkit

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/lanewayhq/laneway/internal/catalog"
	"github.com/lanewayhq/laneway/internal/daemon"
)

// WithCatalog makes the daemon fetch the Catalog from url and accept it only
// if it is signed with key: the spec's third swappable thing in Seam 1. A
// test daemon without it uses an empty Catalog of its own (see Catalog).
func WithCatalog(url string, key ed25519.PublicKey) Option {
	return func(cfg *daemon.Config) { cfg.Catalog = catalog.Location{URL: url, Key: key} }
}

// Catalog is a Catalog a test serves over HTTP, signed with a test key, to
// which it publishes test Plugins (see Plugin) as releases. Give a daemon
// its Option.
type Catalog struct {
	t      testing.TB
	key    ed25519.PrivateKey
	server *httptest.Server

	mu       sync.Mutex
	plugins  []catalog.Plugin
	archives map[string][]byte // by path, as served
	offline  bool
	forged   bool
	tampered map[string]bool // release archive paths
}

// NewCatalog serves an empty Catalog, signed with a new test key, until the
// test ends.
func NewCatalog(t testing.TB) *Catalog {
	t.Helper()
	c := newCatalog(t)
	t.Cleanup(c.server.Close)
	return c
}

func newCatalog(t testing.TB) *Catalog {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err)
	}
	c := &Catalog{t: t, key: key, archives: map[string][]byte{}, tampered: map[string]bool{}}
	c.server = httptest.NewServer(http.HandlerFunc(c.serve))
	return c
}

// emptyCatalog is what test daemons use unless given a Catalog: an empty one,
// so they never reach the real Catalog.
var emptyCatalog = sync.OnceValue(func() *Catalog { return newCatalog(nil) })

// URL is where the Catalog is served.
func (c *Catalog) URL() string { return c.server.URL + "/catalog.json" }

// Key is the public key the Catalog is signed with.
func (c *Catalog) Key() ed25519.PublicKey { return c.key.Public().(ed25519.PublicKey) }

// Option makes a daemon fetch this Catalog and trust its test key.
func (c *Catalog) Option() Option { return WithCatalog(c.URL(), c.Key()) }

// Publish adds the Plugin in dir (such as one from Plugin) to the Catalog as
// a release: its version, Plugin API version, Recipes, Integrations and
// Engines are what its manifest declares.
func (c *Catalog) Publish(dir string) {
	c.t.Helper()
	var m struct {
		Name         string             `json:"name"`
		Version      string             `json:"version"`
		APIVersion   int                `json:"api_version"`
		Recipes      []catalog.Detector `json:"recipes"`
		Integrations []catalog.Detector `json:"integrations"`
		Engines      []string           `json:"engines"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		c.t.Fatalf("testkit: publishing %s: %v", dir, err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		c.t.Fatalf("testkit: publishing %s: %v", dir, err)
	}
	archive, err := catalog.Pack(dir)
	if err != nil {
		c.t.Fatalf("testkit: publishing %s: %v", dir, err)
	}
	r := catalog.Release{Version: m.Version, APIVersion: m.APIVersion, SHA256: catalog.SHA256(archive),
		URL: fmt.Sprintf("releases/%s-%s.tar.gz", m.Name, m.Version), Recipes: m.Recipes, Integrations: m.Integrations}
	for _, f := range m.Engines {
		var e struct {
			Name string `toml:"name"`
		}
		if _, err := toml.DecodeFile(filepath.Join(dir, f), &e); err != nil {
			c.t.Fatalf("testkit: publishing %s: %v", dir, err)
		}
		r.Engines = append(r.Engines, e.Name)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.archives["/"+r.URL] = archive
	i := slices.IndexFunc(c.plugins, func(p catalog.Plugin) bool { return p.Name == m.Name })
	if i < 0 {
		c.plugins = append(c.plugins, catalog.Plugin{Name: m.Name})
		i = len(c.plugins) - 1
	}
	c.plugins[i].Releases = append(c.plugins[i].Releases, r)
}

// Offline makes the Catalog and its releases unreachable, as if the daemon
// were offline: every connection is closed unanswered.
func (c *Catalog) Offline() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offline = true
}

// Forge signs the Catalog with a key other than its own from now on, as a
// Catalog served by someone who took over where it's downloaded from would
// be.
func (c *Catalog) Forge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forged = true
}

// Tamper makes the release of Plugin name at version serve an archive other
// than the one whose SHA-256 the Catalog lists.
func (c *Catalog) Tamper(name, version string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tampered[fmt.Sprintf("/releases/%s-%s.tar.gz", name, version)] = true
}

func (c *Catalog) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.offline {
		if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
			conn.Close()
		}
		return
	}
	data, err := json.MarshalIndent(catalog.Catalog{Format: catalog.Format, Plugins: c.plugins}, "", "  ")
	if err != nil {
		panic(err)
	}
	key := c.key
	if c.forged {
		_, key, _ = ed25519.GenerateKey(nil)
	}
	switch {
	case r.URL.Path == "/catalog.json":
		w.Write(data)
	case r.URL.Path == "/catalog.json"+catalog.SignatureSuffix:
		w.Write(catalog.Sign(data, key))
	case strings.HasPrefix(r.URL.Path, "/releases/") && c.archives[r.URL.Path] != nil:
		archive := c.archives[r.URL.Path]
		if c.tampered[r.URL.Path] {
			archive = append(slices.Clone(archive), 0)
		}
		w.Write(archive)
	default:
		http.NotFound(w, r)
	}
}
