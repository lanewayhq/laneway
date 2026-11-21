package platform

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
)

// Network: the addresses other devices reach this machine on (ADR-0009).

// windowsTailscale is where Tailscale's installer puts its CLI on Windows.
const windowsTailscale = windowsDrive + "/Program Files/Tailscale/tailscale.exe"

func (l *Linux) TailscaleIPv4(ctx context.Context) (netip.Addr, error) {
	var cmd Cmd
	if l.WSL {
		// Tailscale runs on Windows; WSL shares its address in mirrored
		// networking mode.
		name, err := l.Run.LookPath("tailscale.exe")
		if err != nil {
			name = l.sys(windowsTailscale)
			if _, serr := os.Stat(name); serr != nil {
				return netip.Addr{}, errors.New("Tailscale isn't installed on Windows (no tailscale.exe on PATH or in " +
					`C:\Program Files\Tailscale)`)
			}
		}
		cmd = Cmd{Name: name, Args: []string{"ip", "-4"}, Dir: l.sys(windowsDrive)}
	} else {
		name, err := l.Run.LookPath("tailscale")
		if err != nil {
			return netip.Addr{}, errors.New("Tailscale isn't installed (no tailscale on PATH)")
		}
		cmd = Cmd{Name: name, Args: []string{"ip", "-4"}}
	}
	out, err := l.Run.Run(ctx, cmd)
	if err != nil {
		// Its stderr says why: stopped, or logged out.
		return netip.Addr{}, fmt.Errorf("Tailscale has no IPv4 address: %w", err)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	ip, err := netip.ParseAddr(strings.TrimSpace(line))
	if err != nil || !ip.Is4() {
		return netip.Addr{}, fmt.Errorf("Tailscale has no IPv4 address: `tailscale ip -4` printed %q", strings.TrimSpace(string(out)))
	}
	return ip, nil
}

func (l *Linux) LookupHost(ctx context.Context, name string) ([]netip.Addr, error) {
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", name)
	if err != nil {
		return nil, err
	}
	for i, ip := range ips {
		ips[i] = ip.Unmap()
	}
	return ips, nil
}
