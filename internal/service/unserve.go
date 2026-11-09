package service

import (
	"context"
	"fmt"
)

// Unserve stops serving the Service called name, as when its Engine's Plugin
// is removed: it stops listening on its port, closes its connections and
// stops its container. It keeps the container's volume, and so the
// Service's data: serving it again later finds the data where it was.
func (m *Manager) Unserve(ctx context.Context, name string) error {
	m.mu.Lock()
	s, ok := m.services[name]
	delete(m.services, name)
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no Service %s", name)
	}
	s.mu.Lock()
	s.unserved = true
	s.mu.Unlock()
	for _, ln := range s.listeners {
		ln.Close()
	}
	return s.stop(ctx, Stopped)
}

// Volume is the name of the volume that keeps the data of the Service ref.
func (m *Manager) Volume(ref Ref) string {
	return m.opts.Prefix + "-" + ref.Engine + "-" + ref.Version
}
