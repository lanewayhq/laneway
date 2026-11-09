package daemon

import (
	"context"
	"os"
	"path/filepath"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/github"
)

// pluginSource makes source, where a Plugin is added from, a directory to
// read it from: source itself when it is a directory, or for a GitHub
// repository (github.com/<owner>/<repo>[@<tag>]), its release downloaded
// into a temporary directory, which done removes. recorded is what the
// installed Plugin records as its source.
func (d *Daemon) pluginSource(ctx context.Context, source string) (dir, recorded string, done func(), err error) {
	if !github.IsSource(source) {
		if !filepath.IsAbs(source) {
			return "", "", nil, &api.Error{Code: api.CodeBadRequest, Message: "source must be an absolute path or a GitHub repository"}
		}
		return source, source, func() {}, nil
	}
	src, err := github.Parse(source)
	if err != nil {
		return "", "", nil, &api.Error{Code: api.CodeBadRequest, Message: err.Error()}
	}
	plugins := d.cfg.Paths.Plugins()
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		return "", "", nil, err
	}
	tmp, err := os.MkdirTemp(plugins, ".github-")
	if err != nil {
		return "", "", nil, err
	}
	done = func() { os.RemoveAll(tmp) }
	sum, err := d.github.Fetch(ctx, src, tmp)
	if err != nil {
		done()
		return "", "", nil, &api.Error{Code: api.CodeInvalidPlugin, Message: err.Error()}
	}
	d.log.Info("Plugin downloaded from GitHub", "source", src.String(), "release_sha256", sum)
	return tmp, src.String(), done, nil
}
