package cli

import (
	"context"
	"log/slog"
	"time"

	"github.com/lanewayhq/laneway/internal/daemon"
	"github.com/lanewayhq/laneway/internal/paths"
)

// runDaemon runs the daemon until ctx is cancelled (main cancels it on SIGINT
// or SIGTERM).
func runDaemon(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "daemon")
	httpsAddr := fs.String("https-addr", daemon.DefaultProxyAddr,
		"loopback `address` the HTTPS proxy listens on")
	httpAddr := fs.String("http-addr", daemon.DefaultHTTPAddr,
		"loopback `address` plain HTTP is redirected to HTTPS from")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	p, err := paths.FromEnv(env.Getenv)
	if err != nil {
		return err
	}
	d, err := daemon.Start(daemon.Config{
		Paths:     p,
		Logger:    slog.New(slog.NewTextHandler(env.Stderr, nil)),
		ProxyAddr: *httpsAddr,
		HTTPAddr:  *httpAddr,
	})
	if err != nil {
		return err
	}
	stopped := make(chan error, 1)
	go func() { stopped <- d.Wait() }()
	select {
	case err := <-stopped:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return d.Shutdown(shutdownCtx)
	}
}
