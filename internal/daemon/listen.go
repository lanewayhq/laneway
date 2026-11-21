package daemon

import (
	"context"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/config"
)

// DefaultListenInterval is how often the extra listen addresses are
// resolved again and retried.
const DefaultListenInterval = 10 * time.Second

// listening is the state of the extra addresses the HTTPS proxy and the
// plain-HTTP redirect listen on beside loopback: those the global config's
// listen names (ADR-0009). They are reconciled every ListenInterval, so an
// address that comes up late (Tailscale starting after Laneway) is listened
// on once it is there, one that changes is followed, and one that can't be
// bound is retried. None of that ever stops the daemon.
type listening struct {
	mu sync.Mutex
	// bound are the addresses the proxy listens on, each true if plain
	// HTTP is redirected there too.
	bound  map[netip.Addr]bool
	status []api.ListenAddress
	err    string

	stop chan struct{} // closed to end the loop
	done chan struct{} // closed when it has
}

// listenStatus is each extra address with its state, and why listen
// couldn't be read, if it couldn't.
func (d *Daemon) listenStatus() ([]api.ListenAddress, string) {
	d.listen.mu.Lock()
	defer d.listen.mu.Unlock()
	return slices.Clone(d.listen.status), d.listen.err
}

// keepListening reconciles the extra addresses every ListenInterval until
// stopListening.
func (d *Daemon) keepListening() {
	interval := d.cfg.ListenInterval
	if interval <= 0 {
		interval = DefaultListenInterval
	}
	d.listen.stop, d.listen.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(d.listen.done)
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-d.listen.stop:
				return
			case <-tick.C:
				d.reconcileListen()
			}
		}
	}()
}

// stopListening ends the loop keepListening started, so nothing is
// listened on once the proxy stops.
func (d *Daemon) stopListening() {
	if d.listen.stop == nil {
		return
	}
	close(d.listen.stop)
	<-d.listen.done
}

// reconcileListen makes the proxy and the redirect listen on exactly the
// addresses listen resolves to now. Calls are serialised: Start makes the
// first, then the loop.
func (d *Daemon) reconcileListen() {
	global, err := config.LoadGlobal(d.cfg.Paths.GlobalConfig())
	if err != nil {
		d.listen.mu.Lock()
		defer d.listen.mu.Unlock()
		if d.listen.err != err.Error() {
			d.log.Error("not changing the addresses listened on", "err", err)
		}
		d.listen.err = err.Error()
		return
	}
	status := make([]api.ListenAddress, len(global.Listen))
	wanted := map[netip.Addr]bool{}
	for i, name := range global.Listen {
		status[i].Name = name
		ip, err := d.resolveListen(name)
		if err != nil {
			status[i].Error = err.Error()
			continue
		}
		status[i].IP = ip.String()
		wanted[ip] = true
	}

	d.listen.mu.Lock()
	defer d.listen.mu.Unlock()
	if d.listen.bound == nil {
		d.listen.bound = map[netip.Addr]bool{}
	}
	for ip, http := range d.listen.bound {
		if !wanted[ip] {
			d.proxy.Unlisten(ip)
			if http {
				d.redirect.Unlisten(ip)
			}
			delete(d.listen.bound, ip)
			d.log.Info("stopped listening", "ip", ip)
		}
	}
	errs := map[netip.Addr]string{}
	for _, ip := range slices.SortedFunc(maps.Keys(wanted), netip.Addr.Compare) {
		http, ok := d.listen.bound[ip]
		if !ok {
			if err := d.proxy.Listen(ip); err != nil {
				errs[ip] = fmt.Sprintf("can't listen on %s: %v", ip, err)
				continue
			}
		}
		if d.redirect != nil && !http {
			if err := d.redirect.Listen(ip); err != nil {
				errs[ip] = fmt.Sprintf("plain HTTP isn't redirected to HTTPS on %s: %v", ip, err)
			} else {
				http = true
			}
		}
		d.listen.bound[ip] = http
	}
	for i := range status {
		if status[i].IP == "" {
			continue
		}
		ip := netip.MustParseAddr(status[i].IP)
		_, status[i].Listening = d.listen.bound[ip]
		status[i].Error = errs[ip]
	}
	d.logListenChanges(status)
	d.listen.status, d.listen.err = status, ""
}

// resolveListen is the IP one of listen's addresses names now.
func (d *Daemon) resolveListen(name string) (netip.Addr, error) {
	if name != config.ListenTailscale {
		return netip.ParseAddr(name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return d.cfg.Platform.TailscaleIPv4(ctx)
}

// logListenChanges logs each address whose state differs from the last
// reconcile's, so retrying every interval doesn't repeat itself. The caller
// holds d.listen.mu.
func (d *Daemon) logListenChanges(status []api.ListenAddress) {
	for _, st := range status {
		if slices.Contains(d.listen.status, st) {
			continue
		}
		switch {
		case st.Error != "":
			d.log.Warn("listen", "address", st.Name, "ip", st.IP, "listening", st.Listening, "err", st.Error)
		case st.Listening:
			d.log.Info("listening", "address", st.Name, "ip", st.IP)
		}
	}
}
