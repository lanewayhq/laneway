package service

import (
	"context"
	"fmt"
)

// Update serves the Service svc names, which is served already, with svc's
// Engine manifest from now on, as when its Engine's Plugin is updated. Its
// port, volume and container name are keyed by its version, so they stay.
//
// When the image the Service runs changes, such as when an Engine maps its
// version to a new image (ADR-0008), a running container is stopped, and
// its next start creates it again on the new image with its volume, and so
// its data (container.Engine.Start). The new image is pulled in the
// background, so that start doesn't wait for a download.
func (m *Manager) Update(ctx context.Context, svc Service) error {
	s, ok := m.lookup(svc.Name())
	if !ok {
		return fmt.Errorf("no Service %s", svc.Name())
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	svc.Port = s.Port // it listens there already
	spec := m.spec(svc)
	s.mu.Lock()
	changed := spec.Image != s.spec.Image
	running := s.state == Running
	s.Service, s.spec = svc, spec
	s.mu.Unlock()
	if !changed {
		return nil
	}
	m.mu.Lock()
	if !m.closed {
		m.wg.Go(func() { s.pull(spec.Image) })
	}
	m.mu.Unlock()
	if !running {
		return nil
	}
	m.log.Info("stopping Service to start it on its new image", "service", s.Name(), "image", spec.Image)
	return s.stopLocked(ctx, Stopped)
}
