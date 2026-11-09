package container

import (
	"context"
	"errors"
	"sync"
)

// Discovered is the Docker engine whose socket find returns, looked up on
// first use rather than when the daemon starts: rootless Podman's socket is
// activated on demand, and an engine may be installed or started later.
// After a call fails because the engine can't be reached, the next call
// looks again.
type Discovered struct {
	// Find returns the socket of the engine to use, such as
	// platform.Platform's ContainerEngine (rootless Podman first).
	Find func(ctx context.Context) (socket string, err error)

	mu     sync.Mutex
	engine *Docker
}

func (e *Discovered) get(ctx context.Context) (*Docker, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.engine == nil {
		socket, err := e.Find(ctx)
		if err != nil {
			return nil, errors.Join(ErrUnavailable, err)
		}
		e.engine = NewDocker(socket)
	}
	return e.engine, nil
}

// forget drops the engine after a call to it failed to reach it.
func (e *Discovered) forget(d *Docker, err error) {
	if errors.Is(err, ErrUnavailable) {
		e.mu.Lock()
		if e.engine == d {
			e.engine = nil
		}
		e.mu.Unlock()
	}
}

func (e *Discovered) Pull(ctx context.Context, image string) error {
	d, err := e.get(ctx)
	if err != nil {
		return err
	}
	err = d.Pull(ctx, image)
	e.forget(d, err)
	return err
}

func (e *Discovered) Start(ctx context.Context, spec Spec) (string, error) {
	d, err := e.get(ctx)
	if err != nil {
		return "", err
	}
	addr, err := d.Start(ctx, spec)
	e.forget(d, err)
	return addr, err
}

func (e *Discovered) Stop(ctx context.Context, name string) error {
	d, err := e.get(ctx)
	if err != nil {
		return err
	}
	err = d.Stop(ctx, name)
	e.forget(d, err)
	return err
}

func (e *Discovered) Running(ctx context.Context, name string, port int) (string, bool, error) {
	d, err := e.get(ctx)
	if err != nil {
		return "", false, err
	}
	addr, running, err := d.Running(ctx, name, port)
	e.forget(d, err)
	return addr, running, err
}

func (e *Discovered) Exec(ctx context.Context, name string, cmd []string) (Output, error) {
	d, err := e.get(ctx)
	if err != nil {
		return Output{}, err
	}
	out, err := d.Exec(ctx, name, cmd)
	e.forget(d, err)
	return out, err
}
