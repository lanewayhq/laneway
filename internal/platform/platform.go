// Package platform is the thin layer between Portby and the operating system:
// installing the daemon as a service, the permission to bind ports 80 and 443,
// trusting Portby's local CA, opening a browser, and the machine facts
// `portby doctor` reports. Only Linux and WSL2 are implemented (see Detect).
//
// Everything that needs sudo, a trust store, systemd or a browser goes through
// Platform, so tests swap it for platformtest.Fake and never touch the machine.
package platform

import (
	"context"
	"crypto/x509"
)

// Platform is the OS-specific behaviour Portby needs. Methods that change the
// machine return a short description of what they did, for `portby install`
// to show.
type Platform interface {
	// Name is the platform shown to the user: "Linux" or "WSL2".
	Name() string

	// InstallService installs `exe daemon` as a user service that starts
	// with the user's session, and makes sure it is enabled and running. It
	// is safe to repeat: the service is only restarted if its definition
	// changed or it isn't running.
	InstallService(ctx context.Context, exe string) (string, error)
	ServiceStatus(ctx context.Context) (ServiceStatus, error)

	// PortPermission reports whether exe may bind ports 80 and 443.
	PortPermission(ctx context.Context, exe string) (PortPermission, error)
	// AllowPrivilegedPorts lets exe bind ports 80 and 443 without running
	// as root, persistently. It may ask for the user's sudo password.
	AllowPrivilegedPorts(ctx context.Context, exe string) (string, error)

	// TrustStores are the certificate stores Portby's CA must be trusted in
	// for the user's browsers to accept its certificates.
	TrustStores() []TrustStore

	// PortHolders lists the programs listening on TCP port, including, on
	// WSL2 in mirrored networking mode, Windows programs (which share the
	// loopback ports with WSL but are invisible from inside it).
	PortHolders(ctx context.Context, port int) ([]Holder, error)

	// EnableContainerEngine makes sure a container engine's API socket comes
	// up with the user's session (rootless Podman's socket, if Podman is
	// installed).
	EnableContainerEngine(ctx context.Context) (string, error)
	// ContainerEngine finds a container engine whose API answers, or returns
	// an error saying where it looked.
	ContainerEngine(ctx context.Context) (Engine, error)

	// Mise returns `mise --version`, or an error if mise isn't available.
	Mise(ctx context.Context) (string, error)

	// OpenURL opens url in the user's default browser (on WSL2, the Windows
	// one) and returns without waiting for the browser.
	OpenURL(ctx context.Context, url string) error
}

// TrustStore is one place a root CA can be trusted.
type TrustStore interface {
	// Name describes the store to the user, such as "Linux system store".
	Name() string
	// Trusted reports whether ca is trusted in the store.
	Trusted(ctx context.Context, ca *x509.Certificate) (bool, error)
	// Prompt tells the user what Trust will ask of them, such as a sudo
	// password or a confirmation dialog.
	Prompt() string
	// Trust adds ca, read from the PEM file caFile, to the store.
	Trust(ctx context.Context, caFile string, ca *x509.Certificate) error
}

// ServiceStatus is the state of Portby's user service.
type ServiceStatus struct {
	// Unit is the service's name, such as "portby.service".
	Unit      string
	Installed bool
	Enabled   bool
	Active    bool
}

// PortPermission says whether Portby may bind ports 80 and 443 and how.
type PortPermission struct {
	Allowed bool
	// Persisted is true if the permission survives a reboot (and, for a
	// sysctl, doesn't depend on another program's settings file).
	Persisted bool
	// Detail says how it is allowed, or why not.
	Detail string
}

// Holder is a program listening on a port.
type Holder struct {
	// Program is the process name; empty if it can't be seen (another
	// user's process).
	Program string
	PID     int
	// Windows is true for a Windows program seen from WSL2.
	Windows bool
}

// Engine is a reachable container engine.
type Engine struct {
	// Name is "podman" or "docker".
	Name string
	// Address is the API's address, such as "unix:///var/run/docker.sock".
	Address string
}
