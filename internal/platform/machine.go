package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Detect returns the Platform for the machine Laneway is running on, reading
// the environment through getenv.
func Detect(getenv func(string) string) Platform {
	l := &Linux{Getenv: getenv, Run: ExecRunner{}}
	if getenv("WSL_DISTRO_NAME") != "" {
		l.WSL = true
	} else if _, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop"); err == nil {
		l.WSL = true
	}
	return l
}

// Port holders.

// ssUser is one process in ss's users:(...) list.
var ssUser = regexp.MustCompile(`\("([^"]*)",pid=(\d+),fd=\d+\)`)

func (l *Linux) PortHolders(ctx context.Context, port int) ([]Holder, error) {
	out, err := l.run(ctx, "ss", "-Hltnp", fmt.Sprintf("sport = :%d", port))
	if err != nil {
		return nil, err
	}
	var holders []Holder
	seen := map[int]bool{}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		users := ssUser.FindAllStringSubmatch(line, -1)
		if len(users) == 0 {
			// ss only shows the processes of the user running it.
			holders = append(holders, Holder{})
			continue
		}
		for _, u := range users {
			pid, _ := strconv.Atoi(u[2])
			if !seen[pid] {
				seen[pid] = true
				holders = append(holders, Holder{Program: u[1], PID: pid})
			}
		}
	}
	if l.WSL && l.mirrored(ctx) {
		win, err := l.windowsPortHolders(ctx, port)
		if err != nil {
			return nil, err
		}
		holders = append(holders, win...)
	}
	return holders, nil
}

// mirrored reports whether WSL uses mirrored networking, where Windows and
// WSL share the loopback ports.
func (l *Linux) mirrored(ctx context.Context) bool {
	out, err := l.run(ctx, "wslinfo", "--networking-mode")
	return err == nil && strings.TrimSpace(out) == "mirrored"
}

// windowsPortHolders lists the Windows processes listening on port. In
// mirrored mode they make WSL's bind fail, but ss can't see them.
func (l *Linux) windowsPortHolders(ctx context.Context, port int) ([]Holder, error) {
	out, err := l.powershell(ctx, fmt.Sprintf(
		"Get-NetTCPConnection -State Listen -LocalPort %d -ErrorAction SilentlyContinue | "+
			"ForEach-Object { \"$($_.OwningProcess) $((Get-Process -Id $_.OwningProcess -ErrorAction SilentlyContinue).ProcessName)\" }; "+
			// No listener is a (silenced) error, which would make the exit status 1.
			"exit 0",
		port))
	if err != nil {
		return nil, fmt.Errorf("asking Windows who listens on port %d: %w", port, err)
	}
	var holders []Holder
	seen := map[int]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		pidText, name, _ := strings.Cut(strings.TrimSpace(line), " ")
		pid, err := strconv.Atoi(pidText)
		if err != nil || seen[pid] {
			continue
		}
		seen[pid] = true
		holders = append(holders, Holder{Program: strings.TrimSpace(name), PID: pid, Windows: true})
	}
	return holders, nil
}

// Opening URLs.

func (l *Linux) OpenURL(ctx context.Context, url string) error {
	if !l.WSL {
		_, err := l.Run.Run(ctx, Cmd{Name: "xdg-open", Args: []string{url}, Detach: true})
		if err != nil {
			return fmt.Errorf("couldn't open a browser (%w); open %s yourself", err, url)
		}
		return nil
	}
	// rundll32 hands the URL to Windows' default https handler without any
	// shell parsing; cmd.exe's start would split it at "&". It runs from C:
	// and detached, because the first browser window may not return.
	dir := l.sys(windowsDrive)
	_, err := l.Run.Run(ctx, Cmd{
		Name:   l.windowsExe("rundll32.exe", "rundll32.exe"),
		Args:   []string{"url.dll,FileProtocolHandler", url},
		Dir:    dir,
		Detach: true,
	})
	if err == nil {
		return nil
	}
	_, perr := l.Run.Run(ctx, Cmd{
		Name:   l.windowsExe("powershell.exe", "WindowsPowerShell/v1.0/powershell.exe"),
		Args:   []string{"-NoProfile", "-NonInteractive", "-Command", "Start-Process '" + strings.ReplaceAll(url, "'", "''") + "'"},
		Dir:    dir,
		Detach: true,
	})
	if perr != nil {
		return fmt.Errorf("couldn't open the Windows browser (rundll32.exe: %v; powershell.exe: %v); open %s yourself", err, perr, url)
	}
	return nil
}

// Container engine. Rootless Podman is preferred (ADR-0005), then Docker.

// engineSockets are the API sockets to try, in order.
func (l *Linux) engineSockets() []string {
	var socks []string
	if host := l.Getenv("DOCKER_HOST"); strings.HasPrefix(host, "unix://") {
		socks = append(socks, strings.TrimPrefix(host, "unix://"))
	}
	if runtime := l.Getenv("XDG_RUNTIME_DIR"); runtime != "" {
		socks = append(socks, filepath.Join(runtime, "podman", "podman.sock"))
	}
	socks = append(socks,
		l.sys(fmt.Sprintf("/run/user/%d/podman/podman.sock", os.Getuid())),
		l.sys("/run/podman/podman.sock"),
		l.sys("/var/run/docker.sock"),
	)
	var unique []string
	seen := map[string]bool{}
	for _, s := range socks {
		if !seen[s] {
			seen[s] = true
			unique = append(unique, s)
		}
	}
	return unique
}

func (l *Linux) ContainerEngine(ctx context.Context) (Engine, error) {
	socks := l.engineSockets()
	for _, sock := range socks {
		if pingEngine(ctx, sock) {
			name := "docker"
			if strings.Contains(sock, "podman") {
				name = "podman"
			}
			return Engine{Name: name, Address: "unix://" + sock}, nil
		}
	}
	return Engine{}, fmt.Errorf("no container engine answered at %s", strings.Join(socks, ", "))
}

// pingEngine reports whether a Docker-compatible API answers on sock. It
// dials rather than stats: a stale socket file can outlive its engine.
func pingEngine(ctx context.Context, sock string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
	defer c.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://engine/_ping", nil)
	if err != nil {
		return false
	}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	return resp.StatusCode == http.StatusOK && strings.TrimSpace(string(body)) == "OK"
}

func (l *Linux) EnableContainerEngine(ctx context.Context) (string, error) {
	if _, err := l.Run.LookPath("podman"); err != nil {
		if _, err := l.Run.LookPath("docker"); err == nil {
			return "Podman isn't installed, so Laneway will use Docker", nil
		}
		return "", errors.New("neither Podman nor Docker is installed")
	}
	// The socket is systemd-activated: enabling it brings the API back after
	// every login, and the first request starts Podman's service.
	if state, _ := l.systemctl(ctx, "is-enabled", "podman.socket"); state == "enabled" {
		return "podman.socket is already enabled", nil
	}
	if _, err := l.systemctl(ctx, "enable", "--now", "podman.socket"); err != nil {
		return "", err
	}
	return "enabled podman.socket (rootless Podman's API)", nil
}

// Runtimes.

func (l *Linux) Mise(ctx context.Context) (string, error) {
	path, err := l.Run.LookPath("mise")
	if err != nil {
		return "", errors.New("mise not found on PATH")
	}
	out, err := l.run(ctx, path, "--version")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
