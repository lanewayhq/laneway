package cli

import (
	"context"
	"log/slog"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/daemon"
	"github.com/mrsafalpiya/portby.dev/internal/paths"
)

// runDaemon runs the daemon until ctx is cancelled (main cancels it on SIGINT
// or SIGTERM).
func runDaemon(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "daemon")
	httpsAddr := fs.String("https-addr", daemon.DefaultProxyAddr,
		"loopback `address` the HTTPS proxy listens on")
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
