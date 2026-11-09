package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/engine"
)

// Service Domains are Domains under the Dashboard's, <label>.<Dashboard
// Domain>, at which an Engine's manifest asks Laneway to serve one of its
// container ports (engine.ServiceDomain), such as Mailpit's inbox at
// https://mail.laneway.localhost: router.Upstream forwards a request to the
// port of the Engine's Service, starting the Service first, and every
// request (a WebSocket for its whole life) keeps the Service from going
// Idle. A Project can't be served at one: it is under the Dashboard's
// Domain, whose name is reserved.

// serviceDomainLabel is the label of the Service Domain domain would be:
// the one label before the Dashboard's Domain.
func (d *Daemon) serviceDomainLabel(domain string) (string, bool) {
	label, ok := strings.CutSuffix(domain, "."+d.cfg.DashboardDomain)
	if !ok || label == "" || strings.Contains(label, ".") {
		return "", false
	}
	return label, true
}

// domainService is the Engine declaring the Service Domain label, the port
// it forwards to, and the name of the Service it serves: the Engine's
// default version's if it is served, else the first served one ("" if
// none is).
func (d *Daemon) domainService(label string) (e *engine.Manifest, port int, service string) {
	e, sd, ok := d.plugins.ServiceDomain(label)
	if !ok {
		return nil, 0, ""
	}
	for _, st := range d.services.List() {
		if st.Engine.Name != e.Name {
			continue
		}
		if service == "" || st.Version == e.DefaultVersion {
			service = st.Name()
		}
		if st.Version == e.DefaultVersion {
			break
		}
	}
	return e, sd.Port, service
}

// serviceDomainUpstream starts the Service served at the Service Domain
// domain, whose label is label, if needed, and returns the address of the
// port it forwards to.
func (d *Daemon) serviceDomainUpstream(ctx context.Context, domain, label string) (string, error) {
	e, port, service := d.domainService(label)
	switch {
	case e == nil:
		return "", fmt.Errorf("nothing is served at %s yet: no installed Engine declares it as a Service Domain. "+
			"Declare the Engine that does in the [services] table of a Project's %s and link the Project; "+
			"Linking it fetches its Plugin from the Catalog", domain, config.FileName)
	case service == "":
		// The Engine as a Project declares it, not a name made up from it.
		return "", fmt.Errorf("no Project uses %s yet: declare it in the [services] table of %s (%s = %q) and link the Project",
			e.Name, config.FileName, e.Name, e.DefaultVersion)
	}
	return d.services.Forward(ctx, service, port)
}

// useServiceDomain records a request to the Service Domain label as
// activity of the Service it serves. Call the returned func when it ends.
func (d *Daemon) useServiceDomain(label string) func() {
	_, _, service := d.domainService(label)
	if service == "" {
		return func() {}
	}
	return d.services.Use(service)
}
