package platform

import (
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPortHoldersNamesLinuxPrograms(t *testing.T) {
	m := newMachine(t)
	m.run.respond = func(c Cmd) (string, error) {
		if cmdline(c) == "ss -Hltnp sport = :443" {
			return `LISTEN 0 511 0.0.0.0:443 0.0.0.0:* users:(("nginx",pid=4242,fd=6),("nginx",pid=4243,fd=6))
LISTEN 0 511 [::]:443 [::]:* users:(("nginx",pid=4242,fd=7))
LISTEN 0 4096 127.0.0.1:443 0.0.0.0:*
`, nil
		}
		return "", nil
	}

	got, err := m.linux().PortHolders(t.Context(), 443)
	if err != nil {
		t.Fatal(err)
	}

	// The listener without users: belongs to another user, so its program
	// can't be seen.
	want := []Holder{{Program: "nginx", PID: 4242}, {Program: "nginx", PID: 4243}, {}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestPortHoldersOnMirroredWSLIncludeWindowsPrograms(t *testing.T) {
	m := newMachine(t)
	m.run.respond = func(c Cmd) (string, error) {
		line := cmdline(c)
		switch {
		case line == "wslinfo --networking-mode":
			return "mirrored\n", nil
		case strings.HasPrefix(line, "powershell.exe") && strings.Contains(line, "Get-NetTCPConnection") &&
			strings.Contains(line, "-LocalPort 443"):
			return "9100 nginx\r\n9100 nginx\r\n4 System\r\n", nil
		}
		return "", nil
	}

	got, err := m.wsl().PortHolders(t.Context(), 443)
	if err != nil {
		t.Fatal(err)
	}

	want := []Holder{{Program: "nginx", PID: 9100, Windows: true}, {Program: "System", PID: 4, Windows: true}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestPortHoldersOnNATModeWSLDontAskWindows(t *testing.T) {
	m := newMachine(t)
	m.run.respond = func(c Cmd) (string, error) {
		if cmdline(c) == "wslinfo --networking-mode" {
			return "nat\n", nil
		}
		return "", nil
	}

	if _, err := m.wsl().PortHolders(t.Context(), 443); err != nil {
		t.Fatal(err)
	}

	for _, line := range m.run.lines() {
		if strings.HasPrefix(line, "powershell.exe") {
			t.Fatalf("asked Windows in NAT mode: %q", line)
		}
	}
}

func TestOpenURLOnWSLUsesTheWindowsDefaultBrowser(t *testing.T) {
	m := newMachine(t)
	const url = "https://shop.localhost/?a=1&b=2"

	if err := m.wsl().OpenURL(t.Context(), url); err != nil {
		t.Fatal(err)
	}

	// rundll32 passes the URL through untouched; cmd.exe /c start would cut
	// it at the "&".
	if len(m.run.cmds) != 1 {
		t.Fatalf("ran %q, want one command", m.run.lines())
	}
	c := m.run.cmds[0]
	if got := cmdline(c); got != "rundll32.exe url.dll,FileProtocolHandler "+url {
		t.Fatalf("ran %q", got)
	}
	if !c.Detach || c.Dir != filepath.Join(m.root, "/mnt/c") {
		t.Fatalf("got %+v, want it detached and run from C:", c)
	}
}

func TestOpenURLOnWSLFallsBackToPowerShell(t *testing.T) {
	m := newMachine(t)
	m.run.respond = func(c Cmd) (string, error) {
		if strings.HasPrefix(cmdline(c), "rundll32.exe") {
			return "", errors.New("exec format error")
		}
		return "", nil
	}

	if err := m.wsl().OpenURL(t.Context(), "https://it's.localhost/"); err != nil {
		t.Fatal(err)
	}

	lines := m.run.lines()
	if got := lines[len(lines)-1]; got != `powershell.exe -NoProfile -NonInteractive -Command Start-Process 'https://it''s.localhost/'` {
		t.Fatalf("ran %q", got)
	}
}

func TestOpenURLOnLinuxUsesXdgOpen(t *testing.T) {
	m := newMachine(t)

	if err := m.linux().OpenURL(t.Context(), "https://shop.localhost/"); err != nil {
		t.Fatal(err)
	}

	if got := m.run.lines(); !slices.Equal(got, []string{"xdg-open https://shop.localhost/"}) || !m.run.cmds[0].Detach {
		t.Fatalf("ran %q, want xdg-open, detached", got)
	}
}

// engineSocket serves a Docker-compatible /_ping on a Unix socket at path.
func engineSocket(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			w.Write([]byte("OK"))
			return
		}
		http.NotFound(w, r)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
}

// shortDir is a temp dir with a path short enough for Unix sockets.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "portby-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestContainerEnginePrefersRootlessPodman(t *testing.T) {
	m := newMachine(t)
	m.root = shortDir(t)
	runtime := shortDir(t)
	m.env["XDG_RUNTIME_DIR"] = runtime
	engineSocket(t, filepath.Join(m.root, "var/run/docker.sock"))
	engineSocket(t, filepath.Join(runtime, "podman/podman.sock"))

	got, err := m.linux().ContainerEngine(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want := Engine{Name: "podman", Address: "unix://" + filepath.Join(runtime, "podman/podman.sock")}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestContainerEngineSkipsAStaleSocket(t *testing.T) {
	m := newMachine(t)
	m.root = shortDir(t)
	runtime := shortDir(t)
	m.env["XDG_RUNTIME_DIR"] = runtime
	// A socket file with nothing listening, as after podman.socket stops.
	stale := filepath.Join(runtime, "podman/podman.sock")
	os.MkdirAll(filepath.Dir(stale), 0o755)
	os.WriteFile(stale, nil, 0o600)
	engineSocket(t, filepath.Join(m.root, "var/run/docker.sock"))

	got, err := m.linux().ContainerEngine(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if got.Name != "docker" {
		t.Fatalf("got %+v, want Docker", got)
	}
}

func TestContainerEngineHonoursDockerHost(t *testing.T) {
	m := newMachine(t)
	m.root = shortDir(t)
	sock := filepath.Join(shortDir(t), "engine.sock")
	engineSocket(t, sock)
	m.env["DOCKER_HOST"] = "unix://" + sock

	got, err := m.linux().ContainerEngine(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if got.Address != "unix://"+sock {
		t.Fatalf("got %+v, want DOCKER_HOST's socket", got)
	}
}

func TestContainerEngineSaysWhereItLooked(t *testing.T) {
	m := newMachine(t)
	m.root = shortDir(t)
	m.env["XDG_RUNTIME_DIR"] = shortDir(t)

	_, err := m.linux().ContainerEngine(t.Context())

	if err == nil || !strings.Contains(err.Error(), "docker.sock") || !strings.Contains(err.Error(), "podman.sock") {
		t.Fatalf("got %v, want an error listing the sockets tried", err)
	}
}

func TestEnableContainerEngineEnablesThePodmanSocket(t *testing.T) {
	m := newMachine(t)
	m.run.respond = func(c Cmd) (string, error) {
		if cmdline(c) == "systemctl --user is-enabled podman.socket" {
			return "disabled\n", errors.New("exit status 1")
		}
		return "", nil
	}

	if _, err := m.linux().EnableContainerEngine(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := m.run.lines(); !slices.Contains(got, "systemctl --user enable --now podman.socket") {
		t.Fatalf("ran %q", got)
	}
}

func TestEnableContainerEngineLeavesAnEnabledSocketAlone(t *testing.T) {
	m := newMachine(t)
	m.run.respond = func(c Cmd) (string, error) {
		if cmdline(c) == "systemctl --user is-enabled podman.socket" {
			return "enabled\n", nil
		}
		return "", nil
	}

	if _, err := m.linux().EnableContainerEngine(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := m.run.lines(); !slices.Equal(got, []string{"systemctl --user is-enabled podman.socket"}) {
		t.Fatalf("ran %q", got)
	}
}

func TestEnableContainerEngineWithoutPodmanUsesDocker(t *testing.T) {
	m := newMachine(t)
	m.run.missing["podman"] = true

	done, err := m.linux().EnableContainerEngine(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(m.run.cmds) != 0 || !strings.Contains(done, "Docker") {
		t.Fatalf("ran %q, said %q", m.run.lines(), done)
	}
}

func TestMise(t *testing.T) {
	m := newMachine(t)
	m.run.respond = func(c Cmd) (string, error) {
		if cmdline(c) == "mise --version" {
			return "2026.10.4 linux-x64 (2026-10-20)\n", nil
		}
		return "", nil
	}
	if got, err := m.linux().Mise(t.Context()); err != nil || got != "2026.10.4 linux-x64 (2026-10-20)" {
		t.Fatalf("got %q, %v", got, err)
	}

	m.run.missing["mise"] = true
	if _, err := m.linux().Mise(t.Context()); err == nil {
		t.Fatal("no error without mise")
	}
}

func TestDetectRecognisesWSL(t *testing.T) {
	env := map[string]string{"WSL_DISTRO_NAME": "Arch"}
	if got := Detect(func(k string) string { return env[k] }).Name(); got != "WSL2" {
		t.Errorf("with WSL_DISTRO_NAME got %q, want WSL2", got)
	}
}
