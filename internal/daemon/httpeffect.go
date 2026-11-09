package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lanewayhq/laneway/internal/plugin"
)

// httpEffectTimeout is how long an http effect's request may take, response
// included.
const httpEffectTimeout = 10 * time.Second

// httpEffect sends an http effect's request for t's Plugin (ADR-0007): the
// Plugin's module has no sockets, so Laneway makes the request, only to a
// host its manifest lists (checked with the effect, and again for every
// redirect). A response other than 2xx fails the effect.
func (d *Daemon) httpEffect(ctx context.Context, t *effectTarget, r *plugin.HTTPRequest) error {
	what := fmt.Sprintf("%s %s", r.Method, r.URL)
	ctx, cancel := context.WithTimeout(ctx, httpEffectTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, strings.NewReader(r.Body))
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Laneway (Plugin "+t.plugin.Name+")")
	}
	client := &http.Client{CheckRedirect: func(next *http.Request, _ []*http.Request) error {
		if !t.plugin.AllowsURL(next.URL) {
			return fmt.Errorf("redirected to %s, a host the Plugin's manifest doesn't list under capabilities.http", next.URL.Host)
		}
		return nil
	}}
	res, err := client.Do(req)
	if err != nil {
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			err = urlErr.Err
		}
		return fmt.Errorf("%s: %w", what, err)
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("%s answered %s", what, res.Status)
	}
	return nil
}
