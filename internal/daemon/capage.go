package daemon

import (
	"net/http"

	"github.com/lanewayhq/laneway/internal/capage"
	"github.com/lanewayhq/laneway/internal/rootdomain"
)

// The CA page (package capage) is at /ca on the Dashboard Domain, over
// HTTPS through the Dashboard and over plain HTTP through the redirect,
// which serves it instead of redirecting.

// newCAPage is the CA page's handler.
func (d *Daemon) newCAPage() http.Handler {
	return capage.Handler(capage.Config{
		RootCert: d.cfg.Paths.CARootCert(),
		QR: func() string {
			if rootdomain.IsDefault(d.rootDomain()) {
				return ""
			}
			return d.caPageURL()
		},
	})
}

// caPageURL is the CA page's plain-HTTP URL, the one a device that doesn't
// trust Laneway's CA yet can open.
func (d *Daemon) caPageURL() string {
	return "http://" + d.dashboardDomain() + capage.Path
}

// plainHTTP picks what the redirect serves over plain HTTP: the CA page on
// the Dashboard Domain, and nothing else.
func (d *Daemon) plainHTTP(host, path string) http.Handler {
	if host == d.dashboardDomain() && capage.Has(path) {
		return d.caPage
	}
	return nil
}
