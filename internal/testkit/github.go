package testkit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lanewayhq/laneway/internal/catalog"
	"github.com/lanewayhq/laneway/internal/daemon"
	"github.com/lanewayhq/laneway/internal/github"
)

// WithGitHub makes the daemon download Plugins added from GitHub
// (`lane plugin add github.com/…`) from url instead of https://github.com,
// as it swaps the Catalog (see WithCatalog). A test daemon without it uses
// an empty GitHub of its own (see GitHub), so tests never reach the real
// one.
func WithGitHub(url string) Option {
	return func(cfg *daemon.Config) { cfg.GitHub = url }
}

// GitHub is a stand-in for GitHub's release downloads, served over HTTP, to
// which a test publishes test Plugins (see Plugin) as repositories'
// releases. Give a daemon its Option.
type GitHub struct {
	server *httptest.Server

	mu       sync.Mutex
	releases map[string][]string // tags by repository ("owner/name"), oldest first
	assets   map[string][]byte   // by "owner/name/tag"
}

// NewGitHub serves a GitHub with no releases until the test ends.
func NewGitHub(t testing.TB) *GitHub {
	t.Helper()
	g := newGitHub()
	t.Cleanup(g.server.Close)
	return g
}

func newGitHub() *GitHub {
	g := &GitHub{releases: map[string][]string{}, assets: map[string][]byte{}}
	g.server = httptest.NewServer(http.HandlerFunc(g.serve))
	return g
}

// emptyGitHub is what test daemons download GitHub Plugins from unless given
// a GitHub: one with no releases.
var emptyGitHub = sync.OnceValue(newGitHub)

// Option makes a daemon download GitHub Plugins from this GitHub.
func (g *GitHub) Option() Option { return WithGitHub(g.server.URL) }

// Release publishes the Plugin in dir (such as one from Plugin) as the
// release tag of repo ("owner/name"), which becomes its latest, and returns
// the release archive's SHA-256.
func (g *GitHub) Release(t testing.TB, repo, tag, dir string) string {
	t.Helper()
	archive, err := catalog.Pack(dir)
	if err != nil {
		t.Fatalf("testkit: releasing %s: %v", dir, err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.releases[repo] = append(g.releases[repo], tag)
	g.assets[repo+"/"+tag] = archive
	return catalog.SHA256(archive)
}

// serve answers as GitHub does for a release's asset: at
// /<owner>/<name>/releases/download/<tag>/<asset>, and at
// /<owner>/<name>/releases/latest/download/<asset> by redirecting to the
// latest release's.
func (g *GitHub) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) < 5 || parts[2] != "releases" || parts[len(parts)-1] != github.Asset {
		http.NotFound(w, r)
		return
	}
	repo := parts[0] + "/" + parts[1]
	switch {
	case len(parts) == 6 && parts[3] == "latest" && parts[4] == "download":
		tags := g.releases[repo]
		if len(tags) == 0 {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/%s/releases/download/%s/%s", repo, tags[len(tags)-1], github.Asset), http.StatusFound)
	case len(parts) == 6 && parts[3] == "download":
		archive, ok := g.assets[repo+"/"+parts[4]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(archive)
	default:
		http.NotFound(w, r)
	}
}
