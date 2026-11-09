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

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/client"
	"github.com/lanewayhq/laneway/internal/gitrepo"
	"github.com/lanewayhq/laneway/internal/paths"
	"github.com/lanewayhq/laneway/internal/platform"
	"github.com/lanewayhq/laneway/internal/runtimes"
)

// The ports Laneway serves Domains on.
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
	fmt.Fprintf(env.Stdout, "Laneway doctor (%s)\n\n", plat.Name())

	daemonCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	daemon, daemonErr := apiClient(env)
	var info api.DaemonInfo
	if daemonErr == nil {
		info, daemonErr = daemon.Daemon(daemonCtx)
	}
	if daemonErr != nil {
		r.check(statusFail, "daemon", daemonErr.Error(),
			"run `lane install`, or `systemctl --user start laneway` if it is installed")
	} else {
		detail := fmt.Sprintf("running (pid %d, HTTPS on %s", info.PID, info.ProxyAddr)
		if info.HTTPAddr != "" {
			detail += ", HTTP redirect on " + info.HTTPAddr
		}
		r.check(statusOK, "daemon", detail+")", "")
	}

	checkService(ctx, r, plat)
	for _, port := range []int{httpsPort, httpPort} {
		checkPort(ctx, r, plat, port, "", info.PID)
	}
	if daemonErr == nil {
		// Each Service's port, which Laneway listens on to start it.
		services, err := daemon.Services(daemonCtx)
		if err != nil {
			r.check(statusWarn, "Service ports", "can't list the Services: "+err.Error(), "")
		}
		for _, s := range services {
			checkPort(ctx, r, plat, s.Port, s.Name, info.PID)
		}
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
		r.check(statusWarn, "mise", err.Error()+"; Runtimes (PHP, Node, Ruby) are looked up on PATH instead",
			"install mise (https://mise.jdx.dev) to pin Runtime versions per Project")
	} else {
		r.check(statusOK, "mise", v, "")
	}
	checkRuntimes(ctx, r, env)
	checkTestDatabase(ctx, r, env)

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
	dir := worktreeDir(ctx, env)
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

// checkTestDatabase reports why the tests of the current Worktree's Project
// may not use its Test Database, such as a phpunit.xml that hard-codes
// DB_DATABASE, once the daemon, if running, has fetched the Catalog Plugins
// the Project needs, such as its Recipe. A config that can't be resolved
// isn't reported here: link and starts report it.
func checkTestDatabase(ctx context.Context, r *report, env Env) {
	dir := worktreeDir(ctx, env)
	checkProjectPlugins(ctx, r, env, dir)
	plugins, err := openPlugins(env)
	if err != nil {
		return
	}
	defer plugins.Close()
	res, err := plugins.ResolveDir(dir)
	if err != nil {
		return
	}
	for _, w := range res.TestDatabaseWarnings {
		r.check(statusWarn, "Test Database", w, "")
	}
}

// checkProjectPlugins has the daemon, if running, fetch the Catalog Plugins
// the Project in dir needs, as Linking it would, and reports what it
// fetched, or why it couldn't.
func checkProjectPlugins(ctx context.Context, r *report, env Env, dir string) {
	c, err := apiClient(env)
	if err != nil {
		return
	}
	res, err := c.FetchPlugins(ctx, api.FetchPluginsRequest{Dir: dir})
	var apiErr *api.Error
	switch {
	case errors.Is(err, client.ErrDaemonNotRunning), errors.As(err, &apiErr) && apiErr.Code == api.CodeNotGitRepo:
		return
	case err != nil:
		r.check(statusWarn, "Plugins", err.Error(), "once Laneway can reach the Catalog, run `lane doctor` again")
		return
	}
	for _, w := range res.Warnings {
		r.check(statusWarn, "Plugins", w, "")
	}
	for _, p := range res.Fetched {
		r.check(statusOK, "Plugins", fmt.Sprintf("fetched %s %s from the Catalog for this Project", p.Name, p.Version), "")
	}
}

// worktreeDir is the root of the Worktree the CLI runs in, or its directory
// outside a git repository.
func worktreeDir(ctx context.Context, env Env) string {
	if c, err := gitrepo.Resolve(ctx, env.Dir); err == nil {
		return c.Dir
	}
	return env.Dir
}

func checkService(ctx context.Context, r *report, plat platform.Platform) {
	s, err := plat.ServiceStatus(ctx)
	switch {
	case err != nil:
		r.check(statusFail, "service", err.Error(), "")
	case !s.Installed:
		r.check(statusFail, "service", s.Unit+" is not installed", "run `lane install`")
	case !s.Enabled:
		r.check(statusFail, "service", s.Unit+" is installed but not enabled", "run `lane install`")
	case !s.Active:
		r.check(statusFail, "service", s.Unit+" is enabled but not running",
			"see `journalctl --user -u "+s.Unit+"`, then `systemctl --user restart "+s.Unit+"`")
	default:
		r.check(statusOK, "service", s.Unit+" is enabled and running", "")
	}
}

// checkPort reports who holds port, which Laneway needs for what (such as
// "redis:7"; "" for its own HTTP and HTTPS ports). daemonPID is the running
// daemon's, or 0.
func checkPort(ctx context.Context, r *report, plat platform.Platform, port int, what string, daemonPID int) {
	name := fmt.Sprintf("port %d", port)
	if what != "" {
		name += " (" + what + ")"
	}
	holders, err := plat.PortHolders(ctx, port)
	if err != nil {
		r.check(statusWarn, name, "can't tell who is listening: "+err.Error(), "")
		return
	}
	var others []string
	for _, h := range holders {
		if daemonPID != 0 && h.PID == daemonPID && !h.Windows {
			r.check(statusOK, name, "bound by Laneway", "")
			return
		}
		others = append(others, h.String())
	}
	switch {
	case len(others) > 0:
		r.check(statusFail, name, "in use by "+strings.Join(others, ", "),
			fmt.Sprintf("stop it or move it off port %d; Laneway needs this port", port))
	default:
		r.check(statusFail, name, "free, but Laneway is not listening on it",
			"start the daemon with `lane install`; if it is running, see `journalctl --user -u laneway.service`")
	}
}

func checkPortPermission(ctx context.Context, r *report, plat platform.Platform) {
	exe, _ := executable()
	perm, err := plat.PortPermission(ctx, exe)
	switch {
	case err != nil:
		r.check(statusFail, "port permission", err.Error(), "")
	case !perm.Allowed:
		r.check(statusFail, "port permission", "Laneway may not bind ports 80 and 443: "+perm.Detail, "run `lane install`")
	case !perm.Persisted:
		r.check(statusWarn, "port permission", perm.Detail, "run `lane install` so it survives a reboot")
	default:
		r.check(statusOK, "port permission", perm.Detail, "")
	}
}

func checkCA(ctx context.Context, r *report, plat platform.Platform, caFile string) {
	ca, err := readCA(caFile)
	for _, store := range plat.TrustStores() {
		name := "CA in " + store.Name()
		if err != nil {
			r.check(statusFail, name, err.Error(), "start the daemon (`lane install`); it creates the CA")
			continue
		}
		trusted, terr := store.Trusted(ctx, ca)
		switch {
		case terr != nil:
			r.check(statusFail, name, "can't tell: "+terr.Error(), "")
		case !trusted:
			r.check(statusFail, name, "Laneway's CA is not trusted", "run `lane install`")
		default:
			r.check(statusOK, name, "Laneway's CA is trusted", "")
		}
	}
}

// readCA reads Laneway's root CA certificate.
func readCA(file string) (*x509.Certificate, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("Laneway's CA doesn't exist yet (%s)", file)
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

// executable is the path of the running lane binary, with symlinks
// resolved: what the service runs and what a file capability is set on.
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}
