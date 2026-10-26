// Package platformtest provides a fake platform.Platform that records what
// Portby asked of the machine instead of doing it.
package platformtest

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/mrsafalpiya/portby.dev/internal/platform"
)

// The names of the Fake's trust stores.
const (
	LinuxStore   = "Linux system store"
	WindowsStore = "Windows certificate store"
)

var _ platform.Platform = (*Fake)(nil)

// Fake is an in-memory platform.Platform. It starts as a Linux machine where
// nothing is set up and no port is in use. Call New to get one.
type Fake struct {
	mu        sync.Mutex
	wsl       bool
	service   platform.ServiceStatus
	ports     platform.PortPermission
	trusted   map[string]map[[32]byte]bool // store → CA fingerprints
	holders   map[int][]platform.Holder
	engine    *platform.Engine
	engineOn  bool
	mise      string
	opened    []string
	actions   []string
	installed string // the exe InstallService was given
	onInstall func()
}

// New returns a Fake for a plain Linux machine where nothing is set up yet.
func New() *Fake {
	return &Fake{
		service: platform.ServiceStatus{Unit: "portby.service"},
		ports:   platform.PortPermission{Detail: "net.ipv4.ip_unprivileged_port_start is 1024"},
		trusted: map[string]map[[32]byte]bool{},
		holders: map[int][]platform.Holder{},
	}
}

// SetWSL2 makes the Fake a WSL2 machine, which adds the Windows trust store.
func (f *Fake) SetWSL2() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wsl = true
}

// SetPortHolders sets the programs listening on port.
func (f *Fake) SetPortHolders(port int, holders ...platform.Holder) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.holders[port] = holders
}

// SetContainerEngine makes e the reachable container engine.
func (f *Fake) SetContainerEngine(e platform.Engine) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.engine = &e
}

// SetMise makes mise available, reporting version.
func (f *Fake) SetMise(version string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mise = version
}

// OnInstallService makes InstallService call start, standing in for the
// service manager starting the daemon.
func (f *Fake) OnInstallService(start func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onInstall = start
}

// Actions lists every change made to the fake machine, in order: "install
// service", "allow ports", "trust <store>", "enable engine".
func (f *Fake) Actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.actions)
}

// InstalledExecutable is the binary the service was installed to run.
func (f *Fake) InstalledExecutable() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.installed
}

// OpenedURLs are the URLs OpenURL was called with, in order.
func (f *Fake) OpenedURLs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.opened)
}

func (f *Fake) Name() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.wsl {
		return "WSL2"
	}
	return "Linux"
}

func (f *Fake) InstallService(_ context.Context, exe string) (string, error) {
	f.mu.Lock()
	f.actions = append(f.actions, "install service")
	f.installed = exe
	f.service.Installed, f.service.Enabled, f.service.Active = true, true, true
	hook := f.onInstall
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return "installed and started portby.service", nil
}

func (f *Fake) ServiceStatus(context.Context) (platform.ServiceStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.service, nil
}

func (f *Fake) PortPermission(context.Context, string) (platform.PortPermission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ports, nil
}

func (f *Fake) AllowPrivilegedPorts(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, "allow ports")
	f.ports = platform.PortPermission{Allowed: true, Persisted: true,
		Detail: "net.ipv4.ip_unprivileged_port_start=80 (fake)"}
	return "set net.ipv4.ip_unprivileged_port_start=80", nil
}

func (f *Fake) TrustStores() []platform.TrustStore {
	f.mu.Lock()
	defer f.mu.Unlock()
	stores := []platform.TrustStore{store{f, LinuxStore}}
	if f.wsl {
		stores = append(stores, store{f, WindowsStore})
	}
	return stores
}

func (f *Fake) PortHolders(_ context.Context, port int) ([]platform.Holder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.holders[port]), nil
}

func (f *Fake) EnableContainerEngine(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.engineOn {
		f.actions = append(f.actions, "enable engine")
		f.engineOn = true
	}
	return "podman.socket is enabled", nil
}

func (f *Fake) ContainerEngine(context.Context) (platform.Engine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.engine == nil {
		return platform.Engine{}, errors.New("no container engine answered (fake)")
	}
	return *f.engine, nil
}

func (f *Fake) Mise(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mise == "" {
		return "", errors.New("mise not found on PATH")
	}
	return f.mise, nil
}

func (f *Fake) OpenURL(_ context.Context, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, url)
	return nil
}

// store is one of the Fake's trust stores.
type store struct {
	f    *Fake
	name string
}

func (s store) Name() string { return s.name }

func (s store) Prompt() string { return fmt.Sprintf("(fake) adding Portby's CA to the %s", s.name) }

func (s store) Trusted(_ context.Context, ca *x509.Certificate) (bool, error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	return s.f.trusted[s.name][sha256.Sum256(ca.Raw)], nil
}

func (s store) Trust(_ context.Context, _ string, ca *x509.Certificate) error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	s.f.actions = append(s.f.actions, "trust "+s.name)
	if s.f.trusted[s.name] == nil {
		s.f.trusted[s.name] = map[[32]byte]bool{}
	}
	s.f.trusted[s.name][sha256.Sum256(ca.Raw)] = true
	return nil
}
