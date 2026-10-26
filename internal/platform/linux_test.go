package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeRunner records commands instead of running them. respond scripts the
// output of a command by its command line; unscripted commands succeed
// silently. Programs in missing aren't found by LookPath.
type fakeRunner struct {
	mu      sync.Mutex
	cmds    []Cmd
	missing map[string]bool
	respond func(Cmd) (string, error)
}

func (r *fakeRunner) LookPath(name string) (string, error) {
	if r.missing[name] {
		return "", errors.New(name + ": not found")
	}
	return "/usr/bin/" + name, nil
}

func (r *fakeRunner) Run(_ context.Context, c Cmd) ([]byte, error) {
	r.mu.Lock()
	r.cmds = append(r.cmds, c)
	respond := r.respond
	r.mu.Unlock()
	if respond == nil {
		return nil, nil
	}
	out, err := respond(c)
	return []byte(out), err
}

// lines are the command lines run so far, program base names only.
func (r *fakeRunner) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var lines []string
	for _, c := range r.cmds {
		lines = append(lines, cmdline(c))
	}
	return lines
}

func cmdline(c Cmd) string {
	return strings.Join(append([]string{filepath.Base(c.Name)}, c.Args...), " ")
}

// machine is a fake root filesystem plus environment for a Linux.
type machine struct {
	t    *testing.T
	root string
	home string
	env  map[string]string
	run  *fakeRunner
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	m := &machine{t: t, root: t.TempDir(), home: t.TempDir(), run: &fakeRunner{missing: map[string]bool{}}}
	m.env = map[string]string{"HOME": m.home, "PATH": "/home/me/.local/share/mise/shims:/usr/bin"}
	return m
}

// file writes a file under the fake root.
func (m *machine) file(path, content string) *machine {
	m.t.Helper()
	full := filepath.Join(m.root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		m.t.Fatal(err)
	}
	return m
}

func (m *machine) dir(path string) *machine {
	m.t.Helper()
	if err := os.MkdirAll(filepath.Join(m.root, path), 0o755); err != nil {
		m.t.Fatal(err)
	}
	return m
}

func (m *machine) linux() *Linux {
	return &Linux{Getenv: func(k string) string { return m.env[k] }, Run: m.run, Root: m.root}
}

func (m *machine) wsl() *Linux {
	l := m.linux()
	l.WSL = true
	return l
}

const unprivilegedPortStart = "proc/sys/net/ipv4/ip_unprivileged_port_start"

func TestAllowPrivilegedPortsWritesPortbysOwnSysctlFile(t *testing.T) {
	m := newMachine(t).file(unprivilegedPortStart, "1024\n")

	if _, err := m.linux().AllowPrivilegedPorts(t.Context(), "/usr/bin/portby"); err != nil {
		t.Fatal(err)
	}

	if len(m.run.cmds) != 1 {
		t.Fatalf("ran %q, want one sudo command", m.run.lines())
	}
	c := m.run.cmds[0]
	line := cmdline(c)
	for _, want := range []string{"sudo sh -c", "> /etc/sysctl.d/99-portby.conf",
		"net.ipv4.ip_unprivileged_port_start=80", "sysctl -w net.ipv4.ip_unprivileged_port_start=80"} {
		if !strings.Contains(line, want) {
			t.Errorf("command %q does not contain %q", line, want)
		}
	}
	if !c.Interactive {
		t.Error("sudo must be interactive, to ask for the password")
	}
}

func TestAllowPrivilegedPortsKeepsALowEnoughLiveValueButStillWritesItsFile(t *testing.T) {
	// Another tool's sysctl.d file set it; Portby must not depend on that.
	m := newMachine(t).file(unprivilegedPortStart, "80\n").
		file("etc/sysctl.d/99-lerd-ports.conf", "net.ipv4.ip_unprivileged_port_start=80\n")

	if _, err := m.linux().AllowPrivilegedPorts(t.Context(), "/usr/bin/portby"); err != nil {
		t.Fatal(err)
	}

	line := strings.Join(m.run.lines(), "\n")
	if !strings.Contains(line, "/etc/sysctl.d/99-portby.conf") {
		t.Errorf("did not write Portby's own file: %q", line)
	}
	if strings.Contains(line, "sysctl -w") {
		t.Errorf("changed the live value although it was already 80: %q", line)
	}
}

func TestAllowPrivilegedPortsFallsBackToSetcap(t *testing.T) {
	m := newMachine(t).file(unprivilegedPortStart, "1024\n")
	m.run.respond = func(c Cmd) (string, error) {
		if strings.Contains(cmdline(c), "sysctl.d") {
			return "", errors.New("sh: /etc/sysctl.d/99-portby.conf: Read-only file system")
		}
		return "", nil
	}

	done, err := m.linux().AllowPrivilegedPorts(t.Context(), "/home/me/bin/portby")
	if err != nil {
		t.Fatal(err)
	}

	lines := m.run.lines()
	if got := lines[len(lines)-1]; got != "sudo setcap cap_net_bind_service=+ep /home/me/bin/portby" {
		t.Fatalf("last command %q, want setcap on the binary", got)
	}
	if !strings.Contains(done, "upgrad") {
		t.Errorf("result %q should say setcap must be re-applied after upgrades", done)
	}
}

func TestPortPermission(t *testing.T) {
	const ownFile = "etc/sysctl.d/99-portby.conf"
	tests := []struct {
		name             string
		start            string
		ownFile          bool
		getcap           string
		allowed, persist bool
	}{
		{name: "sysctl in Portby's file", start: "80", ownFile: true, allowed: true, persist: true},
		{name: "sysctl set by someone else", start: "80", allowed: true, persist: false},
		{name: "setcap", start: "1024", getcap: "/usr/bin/portby cap_net_bind_service=ep\n", allowed: true, persist: true},
		{name: "nothing", start: "1024", allowed: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine(t).file(unprivilegedPortStart, tt.start+"\n")
			if tt.ownFile {
				m.file(ownFile, "net.ipv4.ip_unprivileged_port_start=80\n")
			}
			m.run.respond = func(c Cmd) (string, error) {
				if cmdline(c) == "getcap /usr/bin/portby" {
					return tt.getcap, nil
				}
				return "", nil
			}

			got, err := m.linux().PortPermission(t.Context(), "/usr/bin/portby")
			if err != nil {
				t.Fatal(err)
			}
			if got.Allowed != tt.allowed || got.Persisted != tt.persist {
				t.Fatalf("got %+v, want allowed=%v persisted=%v", got, tt.allowed, tt.persist)
			}
		})
	}
}

func TestInstallServiceWritesAUserUnitAndStartsIt(t *testing.T) {
	m := newMachine(t).dir("run/systemd/system")
	l := m.linux()

	if _, err := l.InstallService(t.Context(), "/home/me/bin/portby"); err != nil {
		t.Fatal(err)
	}

	unit, err := os.ReadFile(filepath.Join(m.home, ".config/systemd/user/portby.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ExecStart=/home/me/bin/portby daemon\n",
		// Processes need the user's PATH (mise shims, nvm) to find PHP and Node.
		"Environment=PATH=/home/me/.local/share/mise/shims:/usr/bin\n",
		"WantedBy=default.target\n",
	} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit does not contain %q:\n%s", want, unit)
		}
	}
	// A file capability is not gained under NoNewPrivileges, which many
	// sandboxing directives imply, so the unit has none of them.
	for _, banned := range []string{"NoNewPrivileges", "Protect", "Private", "SystemCallFilter"} {
		if strings.Contains(string(unit), banned) {
			t.Errorf("unit contains %q:\n%s", banned, unit)
		}
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable portby.service",
		"systemctl --user restart portby.service",
	}
	if got := m.run.lines(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestInstallServiceAgainLeavesARunningServiceAlone(t *testing.T) {
	m := newMachine(t).dir("run/systemd/system")
	l := m.linux()
	if _, err := l.InstallService(t.Context(), "/home/me/bin/portby"); err != nil {
		t.Fatal(err)
	}
	m.run.cmds = nil
	m.run.respond = func(c Cmd) (string, error) {
		if cmdline(c) == "systemctl --user is-active portby.service" {
			return "active\n", nil
		}
		return "", nil
	}

	if _, err := l.InstallService(t.Context(), "/home/me/bin/portby"); err != nil {
		t.Fatal(err)
	}

	for _, line := range m.run.lines() {
		if strings.Contains(line, "restart") || strings.Contains(line, "daemon-reload") {
			t.Errorf("second install ran %q", line)
		}
	}
}

func TestInstallServiceOnWSLWithoutSystemdSaysHowToEnableIt(t *testing.T) {
	m := newMachine(t)

	_, err := m.wsl().InstallService(t.Context(), "/home/me/bin/portby")

	if err == nil || !strings.Contains(err.Error(), "/etc/wsl.conf") {
		t.Fatalf("got %v, want an error pointing at /etc/wsl.conf", err)
	}
	if len(m.run.cmds) != 0 {
		t.Fatalf("ran %q without systemd", m.run.lines())
	}
}

func TestServiceStatus(t *testing.T) {
	m := newMachine(t)
	l := m.linux()
	m.run.respond = func(c Cmd) (string, error) {
		switch cmdline(c) {
		case "systemctl --user is-enabled portby.service":
			return "enabled\n", nil
		case "systemctl --user is-active portby.service":
			return "inactive\n", errors.New("exit status 3")
		}
		return "", nil
	}

	before, err := l.ServiceStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if before.Installed {
		t.Fatalf("got %+v before the unit exists", before)
	}

	unit := filepath.Join(m.home, ".config/systemd/user/portby.service")
	os.MkdirAll(filepath.Dir(unit), 0o755)
	os.WriteFile(unit, []byte("[Unit]\n"), 0o644)
	after, err := l.ServiceStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !after.Installed || !after.Enabled || after.Active {
		t.Fatalf("got %+v, want installed, enabled, not active", after)
	}
}
