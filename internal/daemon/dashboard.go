package daemon

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/lanewayhq/laneway/internal/dashboard"
)

// The Dashboard is served on a Unix socket next to the API socket (so the
// directory's 0700 keeps other users out), and the proxy forwards
// d.cfg.DashboardDomain to it (router.Upstream). Its handler mounts the API
// routes under /api.

// dashboardSocket is where the Dashboard's server listens.
func (d *Daemon) dashboardSocket() string {
	return filepath.Join(filepath.Dir(d.cfg.Paths.Socket), "dashboard.sock")
}

// startDashboard binds the Dashboard's socket and serves it in the
// background. The API socket is bound first, so no other daemon owns a file
// left here.
func (d *Daemon) startDashboard() error {
	socket := d.dashboardSocket()
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing stale Dashboard socket: %w", err)
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("serving the Dashboard: %w", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("securing the Dashboard socket: %w", err)
	}
	d.dashboard = &http.Server{
		Handler:           dashboard.Handler(d.cfg.DashboardDomain, d.routes()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go d.dashboard.Serve(ln)
	return nil
}

// dashboardUpstream is the dial address the proxy forwards the Dashboard's
// Domain to.
func (d *Daemon) dashboardUpstream() string {
	return "unix/" + d.dashboardSocket()
}
