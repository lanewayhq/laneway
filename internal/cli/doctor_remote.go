package cli

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/platform"
	"github.com/lanewayhq/laneway/internal/rootdomain"
)

// checkRemoteAccess reports whether other devices should reach the
// Worktrees (ADR-0009), from the running daemon's status info: its Root
// Domain and the addresses it listens on beside loopback.
func checkRemoteAccess(ctx context.Context, r *report, plat platform.Platform, info api.DaemonInfo, globalConfig string) {
	const name = "remote access"
	// A daemon older than the Root Domain doesn't report one.
	custom := !rootdomain.IsDefault(info.RootDomain)
	if !custom && len(info.Listen) == 0 && info.ListenError == "" {
		r.check(statusOK, name, "off: only this machine reaches Worktrees (Root Domain localhost, listening on loopback only)", "")
		return
	}
	switch {
	case !custom && len(info.Listen) > 0:
		r.check(statusWarn, name, fmt.Sprintf("Laneway listens on %s, but the Root Domain is localhost, which every device "+
			"resolves to itself: other devices can't reach Worktrees by name", listenNames(info)),
			"set a Root Domain with `lane root-domain set <domain>`, under a domain you own")
	case custom && len(info.Listen) == 0 && info.ListenError == "":
		r.check(statusWarn, name, fmt.Sprintf("the Root Domain is %s, but Laneway listens only on loopback: "+
			"only this machine reaches Worktrees", info.RootDomain),
			"add the address your other devices reach this machine on to listen in "+globalConfig+
				`, such as listen = ["tailscale"]`)
	}
	checkListen(r, info, globalConfig)
	if custom {
		checkRootDomainDNS(ctx, r, plat, info)
	}
	if len(info.Listen) > 0 && plat.Name() == "WSL2" {
		r.check(statusWarn, "Windows firewall", "on WSL2, Windows' Hyper-V firewall may block other devices from ports 80 and 443; "+
			"Laneway can't tell from inside WSL",
			"if other devices can't connect, allow them from an elevated PowerShell on Windows: "+wslFirewallRule)
	}
	if ips := listenIPs(info, true); len(ips) > 0 {
		r.check(statusOK, "reachable from", joinAddrs(ips)+": every Worktree, the Dashboard and the Service Domains, for any device "+
			"that reaches these addresses; the Dashboard can remove Worktrees, so let only your devices reach them "+
			"(such as with Tailscale's ACLs)", "")
	}
}

// wslFirewallRule is the PowerShell command that lets other devices reach
// ports 80 and 443 in WSL2 through Windows' Hyper-V firewall. The GUID is
// WSL's VMCreatorId.
//
// TODO(#130): replace with the command confirmed by hand.
const wslFirewallRule = `New-NetFirewallHyperVRule -Name Laneway -DisplayName "Laneway (HTTP and HTTPS)" -Direction Inbound ` +
	`-VMCreatorId '{40E0AC32-46A5-438A-A0B2-2B479E8F2E90}' -Protocol TCP -LocalPorts 80,443`

// listenNames are the addresses listen names, as written.
func listenNames(info api.DaemonInfo) string {
	var names []string
	for _, l := range info.Listen {
		names = append(names, l.Name)
	}
	return strings.Join(names, ", ")
}

// listenIPs are the IPs of listen's addresses that have one, or, with
// listening, only of those Laneway listens on now.
func listenIPs(info api.DaemonInfo, listening bool) []netip.Addr {
	var ips []netip.Addr
	for _, l := range info.Listen {
		if ip, err := netip.ParseAddr(l.IP); err == nil && (l.Listening || !listening) {
			ips = append(ips, ip)
		}
	}
	return ips
}

// checkRootDomainDNS reports whether names under the Root Domain resolve
// to an address other devices reach Laneway on: one it listens on beside
// loopback.
func checkRootDomainDNS(ctx context.Context, r *report, plat platform.Platform, info api.DaemonInfo) {
	const name = "DNS"
	dns := checkDNS(ctx, plat, info, info.RootDomain)
	if !dns.ok() {
		var problems []string
		for _, n := range dns.failed() {
			problems = append(problems, n.problem)
		}
		r.check(statusFail, name, "names under "+info.RootDomain+" don't reach Laneway: "+strings.Join(problems, "; "),
			"create the record "+dns.record())
		return
	}
	listened := listenIPs(info, true)
	// The addresses Laneway listens on, or would, that names resolve to,
	// for each name that resolves to none other devices reach.
	var stuck []netip.Addr
	onlyLoopback := true
	for _, n := range dns.names {
		if slices.ContainsFunc(n.resolved, func(ip netip.Addr) bool { return slices.Contains(listened, ip) }) {
			continue
		}
		for _, ip := range n.resolved {
			if slices.Contains(dns.targets, ip) && !slices.Contains(stuck, ip) {
				stuck = append(stuck, ip)
				onlyLoopback = onlyLoopback && ip.IsLoopback() && !slices.Contains(dns.extra, ip)
			}
		}
	}
	switch {
	case len(stuck) == 0:
		r.check(statusOK, name, fmt.Sprintf("names under %s resolve to %s, where Laneway listens", info.RootDomain, dns.resolvedTo()), "")
	case onlyLoopback:
		r.check(statusWarn, name, fmt.Sprintf("names under %s resolve to %s, which is loopback: only this machine reaches Laneway there",
			info.RootDomain, joinAddrs(stuck)), "create the record "+dns.record()+" instead")
	default:
		r.check(statusWarn, name, fmt.Sprintf("names under %s resolve to %s, where Laneway isn't listening yet",
			info.RootDomain, joinAddrs(stuck)), "see the listen lines")
	}
}

// checkListen reports each address listen names: whether it is up and
// listened on, and why not.
func checkListen(r *report, info api.DaemonInfo, globalConfig string) {
	if info.ListenError != "" {
		r.check(statusFail, "listen", "can't be read: "+info.ListenError,
			"fix listen in "+globalConfig+"; until then, Laneway keeps listening where it did")
	}
	for _, l := range info.Listen {
		name := "listen " + l.Name
		listened := "listened on"
		if l.IP != l.Name {
			listened = l.IP + " is listened on"
		}
		switch {
		case l.IP == "":
			fix := "Laneway listens once it has an address"
			switch {
			case l.Name != config.ListenTailscale:
			case strings.Contains(l.Error, "isn't installed"):
				fix = "install Tailscale (https://tailscale.com/download) and log in; " + fix
			default:
				fix = "start Tailscale and log in (`tailscale up`); " + fix
			}
			r.check(statusFail, name, "no address: "+l.Error, fix)
		case !l.Listening:
			r.check(statusFail, name, "not listened on: "+l.Error,
				"check that "+l.IP+" is up on this machine (`ip addr`); Laneway keeps trying")
		case l.Error != "":
			r.check(statusWarn, name, listened+" for HTTPS, but not plain HTTP: "+l.Error,
				"other devices can't open the CA page (http://"+rootdomain.Dashboard(info.RootDomain)+"/ca) there; "+
					"free port 80 on "+l.IP+", and Laneway keeps trying")
		default:
			r.check(statusOK, name, listened, "")
		}
	}
}
