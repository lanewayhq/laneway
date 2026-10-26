// Package container is the container engine: the one way Portby runs
// containers (for Services). It is one of the two things the spec's Seam 1
// lets tests swap; package containertest has the fake.
//
// The real engine (Docker) speaks the Docker Engine API over a Unix socket,
// which both Docker and rootless Podman serve (ADR-0005). It is plain
// net/http pinned to API v1.44, the newest version Podman's compatible API
// accepts, rather than the moby client: see spike-5-lazy-tcp.md.
package container

import (
	"context"
	"errors"
)

// Engine runs containers.
type Engine interface {
	// Pull makes sure image is available locally, pulling it if it isn't.
	Pull(ctx context.Context, image string) error
	// Start makes the container described by spec run: it creates it if
	// it doesn't exist (pulling the image first if needed) and starts it if
	// it isn't running. It returns the host address ("127.0.0.1:<port>")
	// that spec.Port is published on, which may change on every start.
	//
	// The container may not be ready when Start returns: the published
	// port can accept connections before anything listens inside.
	Start(ctx context.Context, spec Spec) (addr string, err error)
	// Stop stops the named container if it is running. A container that
	// doesn't exist is not an error.
	Stop(ctx context.Context, name string) error
	// Running reports whether the named container exists and is running,
	// and if so the host address its port is published on.
	Running(ctx context.Context, name string, port int) (addr string, running bool, err error)
}

// Spec describes a container.
type Spec struct {
	// Name is the container's name, unique on the engine.
	Name string
	// Image is a fully qualified image reference, such as
	// "docker.io/library/redis:7": Podman's short-name resolution can
	// prompt or fail.
	Image  string
	Cmd    []string
	Env    []string
	Labels map[string]string
	// Port is the container's TCP port. It is published on the host's
	// loopback only, on a port the engine picks.
	Port int
	// ExtraPorts are more container ports, published like Port; Running
	// gives their host addresses.
	ExtraPorts []int
	// Volume is a named volume mounted at VolumeTarget, so data survives
	// the container being stopped (or re-created).
	Volume       string
	VolumeTarget string
}

// ErrUnavailable means no container engine could be reached.
var ErrUnavailable = errors.New("no container engine is available")
