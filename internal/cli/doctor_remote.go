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
	custom := info.RootDomain != rootdomain.Default
	if !custom && len(info.Listen) == 0 && info.ListenError == "" {
		r.check(statusOK, name, "off: only this machine reaches Worktrees (Root Domain localhost, listening on loopback only)", "")
		return
	}
	checkListen(r, info, globalConfig)
	if custom {
		checkRootDomainDNS(ctx, r, plat, info)
	}
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
	var listened []netip.Addr
	for _, l := range info.Listen {
		if ip, err := netip.ParseAddr(l.IP); err == nil && l.Listening {
			listened = append(listened, ip)
		}
	}
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
		at := l.IP
		if l.Name == config.ListenTailscale && l.IP != "" {
			at = "Tailscale's address " + l.IP
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
			r.check(statusWarn, name, at+" is listened on for HTTPS, but not plain HTTP: "+l.Error,
				"other devices can't open the CA page (http://"+rootdomain.Dashboard(info.RootDomain)+"/ca) there; "+
					"free port 80 on "+l.IP+", and Laneway keeps trying")
		default:
			r.check(statusOK, name, at+" is listened on", "")
		}
	}
}
