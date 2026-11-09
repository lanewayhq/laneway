package daemon

import (
	"context"
	"fmt"
	"strings"

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

// mailLink is the Dashboard link a Plugin declares (as Mailpit's does) on
// the Services of its Engine whose web UI is the inbox at the mail Domain.
const mailLink = "https://mail.{dashboard_domain}/"

// mailService is the name of the Service the inbox shows: one of an Engine
// whose Plugin links its Services to the mail Domain (see mailLink), the
// default version's if it is served, else the first served one.
func (d *Daemon) mailService() (string, bool) {
	var found []service.Status
	for _, e := range d.plugins.EnginesLinkedAt(mailLink) {
		for _, st := range d.services.List() {
			if st.Engine.Name == e.Name {
				found = append(found, st)
			}
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
		engines := d.plugins.EnginesLinkedAt(mailLink)
		if len(engines) == 0 {
			return "", fmt.Errorf("no Project uses an inbox yet, such as Mailpit's: declare its Engine in the [services] table of a Project's %s "+
				"and link the Project; Linking it fetches its Plugin from the Catalog", config.FileName)
		}
		e := engines[0]
		return "", fmt.Errorf("no Project uses %s yet: declare it in the [services] table of %s (%s = %q) and link the Project",
			strings.ToUpper(e.Name[:1])+e.Name[1:], config.FileName, e.Name, e.DefaultVersion)
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
