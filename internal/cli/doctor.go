package cli

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/gitrepo"
	"github.com/mrsafalpiya/portby.dev/internal/paths"
	"github.com/mrsafalpiya/portby.dev/internal/platform"
	"github.com/mrsafalpiya/portby.dev/internal/runtimes"
)

// The ports Portby serves Domains on.
const (
	httpPort  = 80
	httpsPort = 443
)

// Check statuses, the first word of each doctor line.
const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "FAIL"
)

// report prints doctor's checks and counts the failures.
type report struct {
	w        io.Writer
	failures int
}

// check prints one result line, plus a "fix:" line when fix isn't empty.
func (r *report) check(status, name, detail, fix string) {
	if status == statusFail {
		r.failures++
	}
	fmt.Fprintf(r.w, "%-4s  %s: %s\n", status, name, detail)
	if fix != "" {
		fmt.Fprintf(r.w, "      fix: %s\n", fix)
	}
}

func runDoctor(ctx context.Context, env Env, args []string) error {
	fs := newFlags(env, "doctor")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	p, err := paths.FromEnv(env.Getenv)
	if err != nil {
		return err
	}
	plat := platformOf(env)
	r := &report{w: env.Stdout}
	fmt.Fprintf(env.Stdout, "Portby doctor (%s)\n\n", plat.Name())

	daemonCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	daemon, daemonErr := apiClient(env)
	var info api.DaemonInfo
	if daemonErr == nil {
		info, daemonErr = daemon.Daemon(daemonCtx)
	}
	if daemonErr != nil {
		r.check(statusFail, "daemon", daemonErr.Error(),
			"run `portby install`, or `systemctl --user start portby` if it is installed")
	} else {
		r.check(statusOK, "daemon", fmt.Sprintf("running (pid %d, HTTPS on %s)", info.PID, info.ProxyAddr), "")
	}

	checkService(ctx, r, plat)
	for _, port := range []int{httpsPort, httpPort} {
		checkPort(ctx, r, plat, port, info.PID)
	}
	checkPortPermission(ctx, r, plat)
	checkCA(ctx, r, plat, p.CARootCert())

	if e, err := plat.ContainerEngine(ctx); err != nil {
		r.check(statusFail, "container engine", err.Error(),
			"install Podman and run `systemctl --user enable --now podman.socket`, or start Docker")
	} else {
		r.check(statusOK, "container engine", fmt.Sprintf("%s at %s", e.Name, e.Address), "")
	}

	if v, err := plat.Mise(ctx); err != nil {
		r.check(statusWarn, "mise", err.Error()+"; Runtimes (PHP, Node) are looked up on PATH instead",
			"install mise (https://mise.jdx.dev) to pin Runtime versions per Project")
	} else {
		r.check(statusOK, "mise", v, "")
	}
	checkRuntimes(ctx, r, env)

	if r.failures > 0 {
		fmt.Fprintln(env.Stdout)
		return fmt.Errorf("%d problem(s) found", r.failures)
	}
	return nil
}

// checkRuntimes reports the Runtimes resolved for the current Worktree (or
// directory, outside a git repository), and how they fit what the Project
// declares. A Runtime that is neither found nor needed isn't mentioned.
func checkRuntimes(ctx context.Context, r *report, env Env) {
	dir := env.Dir
	if c, err := gitrepo.Resolve(ctx, env.Dir); err == nil {
		dir = c.Dir
	}
	rt := runtimes.Environ(ctx, dir, environOf(env))
	found := rt.Find(ctx)
	warned := map[string]bool{}
	for _, w := range append(rt.Warnings, runtimes.Check(dir, found)...) {
		warned[w.Runtime] = true
		name := w.Runtime
		if name == "mise" {
			name = "mise env"
		}
		r.check(statusWarn, name, w.Message, w.Fix)
	}
	for _, f := range found {
		if warned[f.Name] || f.Version == "" {
			continue
		}
		from := "from PATH"
		if f.Source == "mise" {
			from = "through mise"
		}
		r.check(statusOK, f.Name, fmt.Sprintf("%s at %s (%s)", f.Version, f.Path, from), "")
	}
}

func checkService(ctx context.Context, r *report, plat platform.Platform) {
	s, err := plat.ServiceStatus(ctx)
	switch {
	case err != nil:
		r.check(statusFail, "service", err.Error(), "")
	case !s.Installed:
		r.check(statusFail, "service", s.Unit+" is not installed", "run `portby install`")
	case !s.Enabled:
		r.check(statusFail, "service", s.Unit+" is installed but not enabled", "run `portby install`")
	case !s.Active:
		r.check(statusFail, "service", s.Unit+" is enabled but not running",
			"see `journalctl --user -u "+s.Unit+"`, then `systemctl --user restart "+s.Unit+"`")
	default:
		r.check(statusOK, "service", s.Unit+" is enabled and running", "")
	}
}

// checkPort reports who holds port. daemonPID is the running daemon's, or 0.
func checkPort(ctx context.Context, r *report, plat platform.Platform, port, daemonPID int) {
	name := fmt.Sprintf("port %d", port)
	holders, err := plat.PortHolders(ctx, port)
	if err != nil {
		r.check(statusWarn, name, "can't tell who is listening: "+err.Error(), "")
		return
	}
	var others []string
	for _, h := range holders {
		if daemonPID != 0 && h.PID == daemonPID && !h.Windows {
			r.check(statusOK, name, "bound by Portby", "")
			return
		}
		others = append(others, describeHolder(h))
	}
	switch {
	case len(others) > 0:
		r.check(statusFail, name, "in use by "+strings.Join(others, ", "),
			fmt.Sprintf("stop it or move it off port %d; Portby needs this port", port))
	case port == httpsPort:
		r.check(statusFail, name, "free, but Portby is not listening on it",
			"start the daemon with `portby install`; if it is running, see `journalctl --user -u portby.service`")
	default:
		r.check(statusOK, name, "free", "")
	}
}

func describeHolder(h platform.Holder) string {
	program := h.Program
	if program == "" {
		program = "a process Portby can't see (another user's)"
	}
	if h.Windows {
		program = "Windows program " + program
	}
	if h.PID != 0 {
		return fmt.Sprintf("%s (pid %d)", program, h.PID)
	}
	return program
}

func checkPortPermission(ctx context.Context, r *report, plat platform.Platform) {
	exe, _ := executable()
	perm, err := plat.PortPermission(ctx, exe)
	switch {
	case err != nil:
		r.check(statusFail, "port permission", err.Error(), "")
	case !perm.Allowed:
		r.check(statusFail, "port permission", "Portby may not bind ports 80 and 443: "+perm.Detail, "run `portby install`")
	case !perm.Persisted:
		r.check(statusWarn, "port permission", perm.Detail, "run `portby install` so it survives a reboot")
	default:
		r.check(statusOK, "port permission", perm.Detail, "")
	}
}

func checkCA(ctx context.Context, r *report, plat platform.Platform, caFile string) {
	ca, err := readCA(caFile)
	for _, store := range plat.TrustStores() {
		name := "CA in " + store.Name()
		if err != nil {
			r.check(statusFail, name, err.Error(), "start the daemon (`portby install`); it creates the CA")
			continue
		}
		trusted, terr := store.Trusted(ctx, ca)
		switch {
		case terr != nil:
			r.check(statusFail, name, "can't tell: "+terr.Error(), "")
		case !trusted:
			r.check(statusFail, name, "Portby's CA is not trusted", "run `portby install`")
		default:
			r.check(statusOK, name, "Portby's CA is trusted", "")
		}
	}
}

// readCA reads Portby's root CA certificate.
func readCA(file string) (*x509.Certificate, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("Portby's CA doesn't exist yet (%s)", file)
	}
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s holds no PEM certificate", file)
	}
	return x509.ParseCertificate(block.Bytes)
}

// executable is the path of the running portby binary, with symlinks
// resolved: what the service runs and what a file capability is set on.
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}
