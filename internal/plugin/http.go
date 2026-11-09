package plugin

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// HTTPRequest is an http effect's request (see sdk.HTTPRequest), which
// Laneway sends for the Plugin, since its module has no sockets. Laneway
// sends it only to a host the manifest lists under capabilities.http.
type HTTPRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// HTTPMethods are the methods an http effect may use.
var HTTPMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}

// check says what's wrong with the request, whoever sends it.
func (r *HTTPRequest) check() error {
	if r == nil {
		return errors.New("it has no request")
	}
	if !slices.Contains(HTTPMethods, r.Method) {
		return fmt.Errorf("its method is %q, but it must be %s", r.Method, strings.Join(HTTPMethods, ", "))
	}
	u, err := url.Parse(r.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("its URL %q isn't an http or https URL", r.URL)
	}
	return nil
}

// AllowsURL reports whether p's http effects may send a request to u: its
// manifest lists u's host. A listed host without a port allows any port.
func (p Plugin) AllowsURL(u *url.URL) bool {
	host, port := strings.ToLower(u.Hostname()), u.Port()
	return slices.ContainsFunc(p.Capabilities.HTTP, func(listed string) bool {
		l, err := parseHost(listed)
		return err == nil && strings.ToLower(l.Hostname()) == host && (l.Port() == "" || l.Port() == port)
	})
}

// checkHost refuses an http effect to a host p's manifest doesn't list.
func (p Plugin) checkHost(r *HTTPRequest) error {
	u, _ := url.Parse(r.URL) // checked by HTTPRequest.check
	if p.AllowsURL(u) {
		return nil
	}
	if len(p.Capabilities.HTTP) == 0 {
		return fmt.Errorf("it goes to %s, but its manifest lists no hosts under capabilities.http", u.Host)
	}
	return fmt.Errorf("it goes to %s, but its manifest lists only %s under capabilities.http", u.Host, strings.Join(p.Capabilities.HTTP, ", "))
}

// parseHost reads a host capabilities.http lists: a host name or IP
// address, with an optional port.
func parseHost(listed string) (*url.URL, error) {
	u, err := url.Parse("//" + listed)
	if err != nil || u.Host != listed || u.User != nil || u.Hostname() == "" || strings.ContainsAny(listed, "/*") {
		return nil, fmt.Errorf("%q isn't a host; list hosts such as \"ntfy.sh\" or \"localhost:8080\", without a scheme or path", listed)
	}
	return u, nil
}

// validateHTTPHosts checks the hosts capabilities.http lists.
func validateHTTPHosts(hosts []string) error {
	for i, h := range hosts {
		if _, err := parseHost(h); err != nil {
			return fmt.Errorf("capabilities.http[%d]: %w", i, err)
		}
	}
	return nil
}
