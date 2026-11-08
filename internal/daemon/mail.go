package daemon

import (
	"context"
	"fmt"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/service"
)

// Mailpit's inbox is served at the mail Domain, mail.<Dashboard Domain>
// (https://mail.laneway.localhost): router.Upstream forwards it to the
// Mailpit Service's web UI, starting the Service first, and every request
// to it (a WebSocket for its whole life) keeps the Service from going Idle.

// mailDomain is where Mailpit's inbox is served. A Project can't have it:
// it is under the Dashboard's Domain, whose name is reserved.
func (d *Daemon) mailDomain() string {
	return "mail." + d.cfg.DashboardDomain
}

// mailService is the name of the Mailpit Service the inbox shows: the
// default version's if it is served, else the first served one.
func (d *Daemon) mailService() (string, bool) {
	var found []service.Status
	for _, st := range d.services.List() {
		if st.Engine == service.Mailpit {
			found = append(found, st)
		}
	}
	for _, st := range found {
		if st.Version == st.Engine.DefaultVersion {
			return st.Name(), true
		}
	}
	if len(found) > 0 {
		return found[0].Name(), true
	}
	return "", false
}

// mailUpstream starts the Mailpit Service if needed and returns the address
// of its inbox.
func (d *Daemon) mailUpstream(ctx context.Context) (string, error) {
	name, ok := d.mailService()
	if !ok {
		return "", fmt.Errorf("no Project uses Mailpit yet: declare it in the [services] table of %s (mailpit = %q) and link the Project",
			config.FileName, service.Mailpit.DefaultVersion)
	}
	return d.services.Web(ctx, name)
}

// useMail records a request to the inbox as activity of the Mailpit
// Service. Call the returned func when it ends.
func (d *Daemon) useMail() func() {
	name, ok := d.mailService()
	if !ok {
		return func() {}
	}
	return d.services.Use(name)
}
