package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/client"
	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/paths"
	"github.com/lanewayhq/laneway/internal/platform"
	"github.com/lanewayhq/laneway/internal/rootdomain"
)

// `lane root-domain` shows and changes the Root Domain (ADR-0009): the
// domain every name Laneway serves is under. Changing it writes root_domain
// into the personal config, so the next daemon start keeps it, then asks
// the daemon to apply it at once.

const rootDomainUsage = "use `lane root-domain` to show it, `lane root-domain set <domain> [--skip-dns-check]` or `lane root-domain reset`"

func runRootDomain(ctx context.Context, env Env, args []string) error {
	if len(args) == 0 {
		return showRootDomain(ctx, env)
	}
	switch args[0] {
	case "set":
		return runRootDomainSet(ctx, env, args[1:])
	case "reset":
		if err := parseFlags(newFlags(env, "root-domain reset"), args[1:]); err != nil {
			return err
		}
		return changeRootDomain(ctx, env, rootdomain.Default)
	default:
		return usageError{fmt.Sprintf("unknown subcommand %q; %s", args[0], rootDomainUsage)}
	}
}

func runRootDomainSet(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "root-domain set <domain>")
	skipDNS := fs.Bool("skip-dns-check", false, "switch even if names under the domain don't resolve to an address Laneway listens on yet")
	// The domain may come before or after the flags.
	if err := fs.Parse(args); err != nil {
		return parseFlags(fs, args)
	}
	if fs.NArg() == 0 {
		return usageError{"missing the domain, such as `lane root-domain set lw.example.com`"}
	}
	root := fs.Arg(0)
	if err := parseFlags(fs, fs.Args()[1:]); err != nil {
		return err
	}
	if err := rootdomain.Check(root); err != nil {
		return fmt.Errorf("the Root Domain %w", err)
	}
	if !*skipDNS && !rootdomain.IsDefault(root) {
		c, err := apiClient(env)
		if err != nil {
			return err
		}
		info, err := c.Daemon(ctx)
		if err != nil {
			return err
		}
		if dns := checkDNS(ctx, platformOf(env), info, root); !dns.ok() {
			return errors.New(dns.failure())
		}
	}
	return changeRootDomain(ctx, env, root)
}

// changeRootDomain writes root into the personal config, then has the
// daemon apply it, and says what that changed.
func changeRootDomain(ctx context.Context, env Env, root string) error {
	p, err := paths.FromEnv(env.Getenv)
	if err != nil {
		return err
	}
	if err := config.SetString(p.GlobalConfig(), "root_domain", root); err != nil {
		return err
	}
	res, err := client.New(p.Socket).SetRootDomain(ctx, root)
	if errors.Is(err, client.ErrDaemonNotRunning) {
		fmt.Fprintf(env.Stdout, "Set root_domain = %q in %s; the daemon isn't running, and uses it when it starts\n", root, p.GlobalConfig())
		return nil
	}
	if err != nil {
		return fmt.Errorf("set root_domain = %q in %s, but the daemon couldn't apply it: %w; it uses it when it next starts", root, p.GlobalConfig(), err)
	}
	printRootDomainChange(env.Stdout, res)
	return nil
}

func printRootDomainChange(w io.Writer, res api.RootDomainResponse) {
	if res.Previous == res.RootDomain {
		fmt.Fprintf(w, "Root Domain is already %s; nothing changed\n", res.RootDomain)
		fmt.Fprintf(w, "Dashboard: %s\n", res.Dashboard)
		return
	}
	fmt.Fprintf(w, "Root Domain is now %s (was %s)\n", res.RootDomain, res.Previous)
	fmt.Fprintf(w, "Dashboard: %s\n", res.Dashboard)
	fmt.Fprintf(w, "Applied the Env Patch of %s again\n", plural(len(res.Patched), "Worktree", "Worktrees"))
	for _, r := range res.Restarted {
		fmt.Fprintf(w, "Restarted %s: %s\n", r.Domain, strings.Join(r.Processes, ", "))
	}
	if !rootdomain.IsDefault(res.RootDomain) {
		fmt.Fprintf(w, "Install Laneway's CA on your other devices from %s\n", res.CAPage)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// showRootDomain prints the Root Domain, the addresses Laneway listens on
// and whether DNS sends names under the Root Domain to them.
func showRootDomain(ctx context.Context, env Env) error {
	c, err := apiClient(env)
	if err != nil {
		return err
	}
	info, err := c.Daemon(ctx)
	if err != nil {
		return err
	}
	w := env.Stdout
	fmt.Fprintf(w, "Root Domain:  %s\n", info.RootDomain)
	fmt.Fprintf(w, "Dashboard:    %s\n", info.Dashboard)
	if !rootdomain.IsDefault(info.RootDomain) {
		fmt.Fprintf(w, "CA page:      %s\n", info.CAPage)
	}
	fmt.Fprintf(w, "Listening on: %s (loopback)\n", loopback(info))
	for _, l := range info.Listen {
		fmt.Fprintf(w, "              %s\n", listenLine(l))
	}
	if info.ListenError != "" {
		fmt.Fprintf(w, "              listen can't be read: %s\n", info.ListenError)
	}
	if rootdomain.IsDefault(info.RootDomain) {
		fmt.Fprintf(w, "DNS:          not needed: every device resolves names under localhost to itself, so only this machine reaches them\n")
		return nil
	}
	dns := checkDNS(ctx, platformOf(env), info, info.RootDomain)
	if dns.ok() {
		fmt.Fprintf(w, "DNS:          names under %s resolve to %s, where Laneway listens\n", info.RootDomain, dns.resolvedTo())
		return nil
	}
	fmt.Fprintf(w, "DNS:          not working:\n")
	for _, n := range dns.failed() {
		fmt.Fprintf(w, "                %s\n", n.problem)
	}
	fmt.Fprintf(w, "              create the record %s\n", dns.record())
	return nil
}

// listenLine is how `lane root-domain` shows one extra listen address.
func listenLine(l api.ListenAddress) string {
	name := l.Name
	if l.IP != "" && l.IP != l.Name {
		name += " " + l.IP
	}
	switch {
	case l.Listening && l.Error == "":
		return name + " (listening)"
	case l.Listening:
		return name + " (listening; " + l.Error + ")"
	case l.Error == "":
		return name + " (not listening)"
	default:
		return name + " (not listening: " + l.Error + ")"
	}
}

// loopback is the loopback address the daemon's proxy listens on.
func loopback(info api.DaemonInfo) string {
	host, _, err := net.SplitHostPort(info.ProxyAddr)
	if err != nil {
		return info.ProxyAddr
	}
	return host
}

// dnsCheck is whether names under a Root Domain resolve to an address
// Laneway listens on, or would from listen.
type dnsCheck struct {
	root    string
	targets []netip.Addr // loopback, then extra: what Laneway listens on
	extra   []netip.Addr // each extra listen address with an IP
	names   []dnsName
}

// dnsName is one name looked up.
type dnsName struct {
	name     string
	resolved []netip.Addr
	ok       bool   // one of resolved is a target
	problem  string // why not, if not
}

// checkDNS looks up a random name under root, as a wildcard record answers
// it, and the Dashboard's name, laneway.<root>.
func checkDNS(ctx context.Context, plat platform.Platform, info api.DaemonInfo, root string) dnsCheck {
	dns := dnsCheck{root: root}
	if ip, err := netip.ParseAddr(loopback(info)); err == nil {
		dns.targets = append(dns.targets, ip)
	}
	for _, l := range info.Listen {
		if ip, err := netip.ParseAddr(l.IP); err == nil {
			dns.extra = append(dns.extra, ip)
		}
	}
	dns.targets = append(dns.targets, dns.extra...)
	for _, name := range []string{randomLabel() + "." + root, rootdomain.Dashboard(root)} {
		n := dnsName{name: name}
		lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ips, err := plat.LookupHost(lookupCtx, name)
		cancel()
		n.resolved = ips
		switch {
		case err != nil:
			n.problem = fmt.Sprintf("%s doesn't resolve (%v)", name, err)
		case !slices.ContainsFunc(ips, func(ip netip.Addr) bool { return slices.Contains(dns.targets, ip) }):
			n.problem = fmt.Sprintf("%s resolves to %s, where Laneway doesn't listen", name, joinAddrs(ips))
		default:
			n.ok = true
		}
		dns.names = append(dns.names, n)
	}
	return dns
}

// randomLabel is a label no record names on its own, so only a wildcard
// record answers for it.
func randomLabel() string {
	b := make([]byte, 6)
	rand.Read(b)
	return "lane-check-" + hex.EncodeToString(b)
}

func (c dnsCheck) ok() bool { return len(c.failed()) == 0 }

func (c dnsCheck) failed() []dnsName {
	var out []dnsName
	for _, n := range c.names {
		if !n.ok {
			out = append(out, n)
		}
	}
	return out
}

// resolvedTo are the addresses Laneway listens on that the names resolve
// to.
func (c dnsCheck) resolvedTo() string {
	var ips []netip.Addr
	for _, n := range c.names {
		for _, ip := range n.resolved {
			if slices.Contains(c.targets, ip) && !slices.Contains(ips, ip) {
				ips = append(ips, ip)
			}
		}
	}
	return joinAddrs(ips)
}

// record is the DNS record that sends every name under the Root Domain to
// Laneway: a wildcard for the first address other devices reach it on.
func (c dnsCheck) record() string {
	for _, ip := range c.extra {
		kind := "A"
		if ip.Is6() {
			kind = "AAAA"
		}
		return fmt.Sprintf("*.%s %s %s", c.root, kind, ip)
	}
	return fmt.Sprintf("*.%s A <the address your other devices reach this machine on>, and add that address to listen in your global config", c.root)
}

// failure is the error `lane root-domain set` fails with.
func (c dnsCheck) failure() string {
	var b strings.Builder
	fmt.Fprintf(&b, "DNS doesn't send names under %s to an address Laneway listens on (%s):\n", c.root, joinAddrs(c.targets))
	for _, n := range c.failed() {
		fmt.Fprintf(&b, "  %s\n", n.problem)
	}
	fmt.Fprintf(&b, "Create this record, then run this again (or use --skip-dns-check to switch anyway):\n  %s\n", c.record())
	b.WriteString("Nothing was changed.")
	return b.String()
}

func joinAddrs(ips []netip.Addr) string {
	s := make([]string, len(ips))
	for i, ip := range ips {
		s[i] = ip.String()
	}
	return strings.Join(s, ", ")
}
