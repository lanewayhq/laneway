package daemon

import (
	"context"

	"github.com/lanewayhq/laneway/internal/plugin"
	"github.com/lanewayhq/laneway/internal/service"
)

// updateServices serves the Services of the Engines of p, just installed
// over an earlier version of it, with p's Engine manifests from now on. A
// Service whose version p maps to a new image (ADR-0008) moves to it, with
// its data and port: see service.Manager.Update.
func (d *Daemon) updateServices(ctx context.Context, p plugin.Plugin) {
	for _, st := range d.services.List() {
		for _, e := range p.Engines {
			if e.Name != st.Engine.Name {
				continue
			}
			if err := d.services.Update(ctx, service.Service{Engine: e, Version: st.Version, Port: st.Port}); err != nil {
				d.log.Error("updating a Service's Engine", "plugin", p.Name, "service", st.Name(), "err", err)
			}
		}
	}
}
