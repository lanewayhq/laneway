package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// Global is the global config file (paths.Paths.GlobalConfig): defaults for
// every Project, which a Project's own config overrides.
type Global struct {
	// IdleTimeout is how long a Worktree may go without requests or open
	// connections before it is Idle, such as "30m".
	IdleTimeout *time.Duration `toml:"idle_timeout"`
	// ServiceIdleTimeout is how long a Service may go without connections
	// and without running Worktrees using it before it is stopped, such
	// as "1h".
	ServiceIdleTimeout *time.Duration `toml:"service_idle_timeout"`
	// BucketCopyLimit is the biggest a main Worktree's Bucket may be for a
	// new Worktree's Bucket to start as a copy of it, such as "1GB"; above
	// it the new Bucket starts empty. A Project's bucket_copy_limit
	// overrides it. See Layer.BucketCopyLimit.
	BucketCopyLimit *Size `toml:"bucket_copy_limit"`
	// Plugins are the machine-wide Plugins (added with `lane plugin add
	// --global`) turned on in every Project, by name.
	Plugins []string `toml:"plugins"`
	// Listen are the addresses Laneway listens on beside loopback, so
	// the developer's other devices reach it (ADR-0009): each an IP
	// address, or ListenTailscale for the machine's Tailscale IPv4
	// address. Never one meaning every interface.
	Listen []string `toml:"listen"`
	// Warnings are problems that don't stop the file loading, such as
	// unknown keys, each prefixed with the file's path.
	Warnings []string `toml:"-"`
}

// LoadGlobal reads the global config file at path. A missing file (or an
// empty path) is an empty Global.
func LoadGlobal(path string) (Global, error) {
	var g Global
	if path == "" {
		return g, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return g, nil
	}
	if err != nil {
		return g, err
	}
	md, err := toml.Decode(string(data), &g)
	if err != nil {
		return Global{}, decodeError(path, err)
	}
	if g.IdleTimeout != nil && *g.IdleTimeout <= 0 {
		return Global{}, fmt.Errorf("%s: idle_timeout must be positive, such as \"30m\", not %s", path, *g.IdleTimeout)
	}
	if g.ServiceIdleTimeout != nil && *g.ServiceIdleTimeout <= 0 {
		return Global{}, fmt.Errorf("%s: service_idle_timeout must be positive, such as \"1h\", not %s", path, *g.ServiceIdleTimeout)
	}
	if err := checkPluginNames(g.Plugins); err != nil {
		return Global{}, fmt.Errorf("%s: %w", path, err)
	}
	for _, l := range g.Listen {
		if err := checkListen(l); err != nil {
			return Global{}, fmt.Errorf("%s: listen: %w", path, err)
		}
	}
	for _, key := range md.Undecoded() {
		g.Warnings = append(g.Warnings, fmt.Sprintf("%s: unknown key %q", path, key.String()))
	}
	return g, nil
}

// ListenTailscale is the listen keyword for the machine's Tailscale IPv4
// address.
const ListenTailscale = "tailscale"

// checkListen checks one of listen's addresses.
func checkListen(l string) error {
	if l == ListenTailscale {
		return nil
	}
	ip, err := netip.ParseAddr(l)
	if err != nil {
		return fmt.Errorf("%q is neither an IP address nor %q", l, ListenTailscale)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("%q means every interface, and Laneway never listens on every interface: "+
			"name the address your other devices reach this machine on, or %q", l, ListenTailscale)
	}
	return nil
}
