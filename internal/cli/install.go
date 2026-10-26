package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/client"
	"github.com/mrsafalpiya/portby.dev/internal/paths"
)

// daemonStartTimeout bounds how long install waits for the service's daemon.
const daemonStartTimeout = 30 * time.Second

// runInstall sets the machine up for Portby. Every step checks first and
// skips what is already done, so running it again is safe and doesn't prompt
// again.
func runInstall(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "install")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	p, err := paths.FromEnv(env.Getenv)
	if err != nil {
		return err
	}
	plat := platformOf(env)
	exe, err := executable()
	if err != nil {
		return fmt.Errorf("finding the portby binary: %w", err)
	}
	out := env.Stdout
	fmt.Fprintf(out, "Installing Portby on %s (%s)\n\n", plat.Name(), exe)

	// Ports first: the daemon the service starts binds 443.
	perm, err := plat.PortPermission(ctx, exe)
	if err != nil {
		return err
	}
	if perm.Allowed && perm.Persisted {
		fmt.Fprintf(out, "Ports 80/443: already allowed (%s)\n", perm.Detail)
	} else {
		fmt.Fprintln(out, "Ports 80/443: allowing Portby to bind them. This needs sudo, so you may be asked for your password.")
		done, err := plat.AllowPrivilegedPorts(ctx, exe)
		if err != nil {
			return fmt.Errorf("allowing ports 80 and 443: %w", err)
		}
		fmt.Fprintf(out, "Ports 80/443: %s\n", done)
	}

	done, err := plat.InstallService(ctx, exe)
	if err != nil {
		return fmt.Errorf("installing the service: %w", err)
	}
	fmt.Fprintf(out, "Service: %s\n", done)
	c := client.New(p.Socket)
	if err := waitForDaemon(ctx, c); err != nil {
		return err
	}

	if done, err := plat.EnableContainerEngine(ctx); err != nil {
		fmt.Fprintf(out, "Container engine: %v (Services won't work until one is running)\n", err)
	} else {
		fmt.Fprintf(out, "Container engine: %s\n", done)
	}

	// The daemon creates the CA when it first starts, so this comes after it.
	ca, err := readCA(p.CARootCert())
	if err != nil {
		return err
	}
	for _, store := range plat.TrustStores() {
		trusted, err := store.Trusted(ctx, ca)
		if err != nil {
			return fmt.Errorf("checking the %s: %w", store.Name(), err)
		}
		if trusted {
			fmt.Fprintf(out, "CA: already trusted in the %s\n", store.Name())
			continue
		}
		fmt.Fprintf(out, "CA: %s\n", store.Prompt())
		if err := store.Trust(ctx, p.CARootCert(), ca); err != nil {
			return fmt.Errorf("trusting Portby's CA in the %s: %w", store.Name(), err)
		}
		fmt.Fprintf(out, "CA: trusted in the %s\n", store.Name())
	}

	fmt.Fprintln(out, "\nPortby is installed. Restart open browsers (Firefox in particular) so they pick up the CA, then run `portby doctor` to check.")
	return nil
}

// waitForDaemon waits until the daemon answers on its socket.
func waitForDaemon(ctx context.Context, c *client.Client) error {
	ctx, cancel := context.WithTimeout(ctx, daemonStartTimeout)
	defer cancel()
	for {
		_, err := c.Daemon(ctx)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the daemon didn't come up within %s (%v); see `journalctl --user -u portby.service`",
				daemonStartTimeout, err)
		case <-time.After(100 * time.Millisecond):
		}
	}
}
