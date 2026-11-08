package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Linux is the Platform for Linux, and for Linux on WSL2 when WSL is set.
// Everything it does to the machine goes through Run. Decisions here follow
// spike #3 (WSL2 networking, privileged ports and CA trust).
type Linux struct {
	// WSL is true on WSL2: the CA is trusted on the Windows side too, URLs
	// open in the Windows browser and Windows programs can hold ports.
	WSL    bool
	Getenv func(string) string
	Run    Runner
	// Root is prepended to the system paths Linux reads (/proc, /etc, /run,
	// /mnt/c); "" in production, a temporary directory in tests.
	Root string
}

var _ Platform = (*Linux)(nil)

func (l *Linux) Name() string {
	if l.WSL {
		return "WSL2"
	}
	return "Linux"
}

// sys is a system path under Root.
func (l *Linux) sys(path string) string {
	return filepath.Join(l.Root, path)
}

func (l *Linux) run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := l.Run.Run(ctx, Cmd{Name: name, Args: args})
	return string(out), err
}

func (l *Linux) sudo(ctx context.Context, args ...string) error {
	_, err := l.Run.Run(ctx, Cmd{Name: "sudo", Args: args, Interactive: true})
	return err
}

// Port permission. Lowering net.ipv4.ip_unprivileged_port_start is preferred:
// it is a kernel setting, so it survives upgrades of the binary. Laneway writes
// its own sysctl.d file rather than relying on whoever set the live value.
// A cap_net_bind_service file capability is the fallback; it is lost whenever
// the binary is replaced, so `lane install` must be re-run after upgrades.

const (
	portStartSysctl = "net.ipv4.ip_unprivileged_port_start"
	portStartProc   = "/proc/sys/net/ipv4/ip_unprivileged_port_start"
	sysctlFile      = "/etc/sysctl.d/99-laneway.conf"
	sysctlLine      = portStartSysctl + "=80"
)

func (l *Linux) portStart() (int, error) {
	data, err := os.ReadFile(l.sys(portStartProc))
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", portStartSysctl, err)
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func (l *Linux) ownSysctlFile() bool {
	data, err := os.ReadFile(l.sys(sysctlFile))
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.ReplaceAll(line, " ", "") == sysctlLine {
			return true
		}
	}
	return false
}

func (l *Linux) PortPermission(ctx context.Context, exe string) (PortPermission, error) {
	start, err := l.portStart()
	if err != nil {
		return PortPermission{}, err
	}
	if start <= 80 {
		if l.ownSysctlFile() {
			return PortPermission{Allowed: true, Persisted: true,
				Detail: fmt.Sprintf("%s=%d, set in %s", portStartSysctl, start, sysctlFile)}, nil
		}
		return PortPermission{Allowed: true, Detail: fmt.Sprintf(
			"%s=%d for now, but %s doesn't set it, so it may revert on reboot", portStartSysctl, start, sysctlFile)}, nil
	}
	if out, err := l.run(ctx, "getcap", exe); err == nil && strings.Contains(out, "cap_net_bind_service") {
		return PortPermission{Allowed: true, Persisted: true, Detail: fmt.Sprintf(
			"cap_net_bind_service set on %s with setcap (re-run `lane install` after upgrading Laneway)", exe)}, nil
	}
	return PortPermission{Detail: fmt.Sprintf("%s is %d and %s has no cap_net_bind_service", portStartSysctl, start, exe)}, nil
}

func (l *Linux) AllowPrivilegedPorts(ctx context.Context, exe string) (string, error) {
	start, err := l.portStart()
	if err != nil {
		return "", err
	}
	script := fmt.Sprintf("printf '%%s\\n' '# Written by `lane install`: lets Laneway bind ports 80 and 443 without root.' '%s' > %s",
		sysctlLine, sysctlFile)
	if start > 80 {
		script += " && sysctl -w " + sysctlLine
	}
	sysctlErr := l.sudo(ctx, "sh", "-c", script)
	if sysctlErr == nil {
		return fmt.Sprintf("set %s in %s (this lets any local user bind ports 80-1023)", sysctlLine, sysctlFile), nil
	}
	if err := l.sudo(ctx, "setcap", "cap_net_bind_service=+ep", exe); err != nil {
		return "", fmt.Errorf("writing %s failed (%v), and so did setcap on %s: %w", sysctlFile, sysctlErr, exe, err)
	}
	return fmt.Sprintf("couldn't write %s (%v), so set cap_net_bind_service on %s instead; "+
		"re-run `lane install` after upgrading Laneway, because replacing the binary drops it", sysctlFile, sysctlErr, exe), nil
}

// The service: a systemd user unit running `lane daemon`.

const serviceUnit = "laneway.service"

func (l *Linux) unitPath() string {
	config := l.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(l.Getenv("HOME"), ".config")
	}
	return filepath.Join(config, "systemd", "user", serviceUnit)
}

// unit is the service's unit file. It deliberately has no sandboxing
// directives: many imply NoNewPrivileges=yes, under which a setcap file
// capability isn't gained. PATH is the installing shell's, so Processes find
// the user's Runtimes (mise shims, nvm); re-running install refreshes it.
func (l *Linux) unit(exe string) string {
	return fmt.Sprintf(`# Written by `+"`lane install`"+`; re-run it to update this file.
[Unit]
Description=Laneway daemon
Documentation=https://github.com/lanewayhq/laneway

[Service]
Type=simple
ExecStart=%s daemon
Environment=PATH=%s
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`, systemdQuote(exe), l.Getenv("PATH"))
}

// systemdQuote quotes a path for a unit file's ExecStart if it needs it.
func systemdQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func (l *Linux) checkSystemd() error {
	if _, err := os.Stat(l.sys("/run/systemd/system")); err == nil {
		return nil
	}
	if l.WSL {
		return errors.New("systemd isn't running, and Laneway runs as a systemd user service; " +
			"enable systemd by adding \"[boot]\" and \"systemd=true\" to /etc/wsl.conf, " +
			"run `wsl.exe --shutdown` from Windows, then run `lane install` again")
	}
	return errors.New("systemd isn't running, and Laneway runs as a systemd user service; " +
		"run `lane daemon` yourself instead")
}

func (l *Linux) systemctl(ctx context.Context, args ...string) (string, error) {
	out, err := l.run(ctx, "systemctl", append([]string{"--user"}, args...)...)
	return strings.TrimSpace(out), err
}

func (l *Linux) InstallService(ctx context.Context, exe string) (string, error) {
	if err := l.checkSystemd(); err != nil {
		return "", err
	}
	path, unit := l.unitPath(), l.unit(exe)
	old, err := os.ReadFile(path)
	changed := err != nil || string(old) != unit
	if changed {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
			return "", err
		}
		if _, err := l.systemctl(ctx, "daemon-reload"); err != nil {
			return "", err
		}
	}
	if _, err := l.systemctl(ctx, "enable", serviceUnit); err != nil {
		return "", err
	}
	if !changed {
		if state, _ := l.systemctl(ctx, "is-active", serviceUnit); state == "active" {
			return serviceUnit + " is already installed and running", nil
		}
	}
	if _, err := l.systemctl(ctx, "restart", serviceUnit); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %s and started %s", path, serviceUnit), nil
}

func (l *Linux) ServiceStatus(ctx context.Context) (ServiceStatus, error) {
	s := ServiceStatus{Unit: serviceUnit}
	if _, err := os.Stat(l.unitPath()); err != nil {
		return s, nil
	}
	s.Installed = true
	// Both exit non-zero for "no", so only the output counts.
	enabled, _ := l.systemctl(ctx, "is-enabled", serviceUnit)
	active, _ := l.systemctl(ctx, "is-active", serviceUnit)
	s.Enabled = enabled == "enabled"
	s.Active = active == "active"
	return s, nil
}
