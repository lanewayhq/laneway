# Manual checklist: `portby install` on Linux and WSL2

`portby install` changes the machine: it needs sudo, the system trust store, systemd and, on WSL2, the Windows certificate store and a Windows dialog. None of that runs in automated tests (they use `platformtest.Fake`), so run this checklist by hand on a Linux machine and on WSL2 after changing `internal/platform` or `internal/cli/install.go` / `doctor.go`.

Record each result as pass/fail, plus anything surprising, in the table at the end.

## Before you start

- [ ] Build the binary to where it will stay. The service runs this exact path, and the setcap fallback is tied to it. Don't use `go run`.
      `go build -o ~/.local/bin/portby ./cmd/portby`
- [ ] No other Portby daemon is running (`pgrep -a portby`), and nothing is listening on 443 (`ss -ltnp 'sport = :443'`; on WSL2 also `powershell.exe "Get-NetTCPConnection -State Listen -LocalPort 443"`).
- [ ] WSL2 only: systemd is on (`/etc/wsl.conf` has `[boot]` and `systemd=true`), and note the networking mode (`wslinfo --networking-mode`).
- [ ] `portby doctor` lists `FAIL` for the service, port permission and CA, and exits 1.

## Install

- [ ] `portby install` asks for the sudo password at most twice: once for the ports and once for the Linux CA store. sudo caches it, so often it asks only once.
- [ ] WSL2: a Windows "Security Warning: You are about to install a certificate from a certification authority (CA) claiming to represent: Portby Local CA…" dialog appears. Click **Yes**. No UAC prompt.
- [ ] It ends with "Portby is installed".

### Port permission

- [ ] `cat /etc/sysctl.d/99-portby.conf` contains `net.ipv4.ip_unprivileged_port_start=80`.
- [ ] `sysctl net.ipv4.ip_unprivileged_port_start` prints `80`.
- [ ] If install said it fell back to setcap: `getcap ~/.local/bin/portby` shows `cap_net_bind_service=ep`.

### Service

- [ ] `systemctl --user status portby.service` is `enabled` and `active (running)`.
- [ ] `~/.config/systemd/user/portby.service` has `ExecStart=<your binary> daemon` and `Environment=PATH=<your shell's PATH>`.
- [ ] `ss -ltnp 'sport = :443'` shows `portby` listening on `127.0.0.1:443`.

### CA trust (Linux)

- [ ] The root is in the distribution's anchor directory as `portby-local-ca.crt` (Arch: `/etc/ca-certificates/trust-source/anchors/`, Fedora: `/etc/pki/ca-trust/source/anchors/`, Debian/Ubuntu: `/usr/local/share/ca-certificates/`).
- [ ] Arch/Fedora: `trust list | grep -i -A2 portby` lists it.
- [ ] Link a Project with a Primary Process (any `portby.toml` with `[processes.web]`), then `curl https://<project>.localhost/` succeeds **without** `-k`.
- [ ] Linux desktop only: Chrome and Firefox open `https://<project>.localhost/` without a certificate warning. On Debian/Ubuntu, NSS doesn't read the system store, so expect a warning here and note it.

### CA trust (WSL2, Windows side)

- [ ] `powershell.exe "Get-ChildItem Cert:\CurrentUser\Root | Where-Object Subject -like '*Portby*'"` lists the root.
- [ ] Chrome and Edge open `https://<project>.localhost/` with no warning. Note whether an already-open browser needed a restart.
- [ ] Firefox does too, after a restart. If it doesn't, note the value of `security.enterprise_roots.enabled` in `about:config`.
- [ ] Mirrored mode: the first request to a new Domain may take about 300 ms longer (the `::1` attempt times out first). That's expected.

## `portby open`

- [ ] In the Linked Project, `portby open` opens `https://<project>.localhost/` in the default browser. On WSL2 that's the Windows default browser, and no console window or "UNC paths are not supported" message appears.
- [ ] In a directory outside any Linked Project, `portby open` fails and tells you to run `portby link`.

## Running it again

- [ ] `portby install` again: no sudo prompt, no Windows dialog, every step says "already". The daemon isn't restarted (`systemctl --user status portby` shows the same start time).
- [ ] `portby doctor` reports `ok` for daemon, service, port 443 ("bound by Portby"), port permission, the CA in every store, container engine and mise (`warn` for mise is fine if it isn't installed), and exits 0.

## Port conflicts

- [ ] With Portby running, start something on port 80, e.g. `python3 -m http.server 80 --bind 127.0.0.1`. `portby doctor` reports `FAIL port 80: in use by python3 (pid N)`. Stop it.
- [ ] `systemctl --user stop portby`, then hold 443 with a root process, e.g. `sudo python3 -m http.server 443 --bind 127.0.0.1`. `portby doctor` reports port 443 in use by "a process Portby can't see (another user's)". Stop it, then `systemctl --user start portby`.
- [ ] WSL2 mirrored mode: `systemctl --user stop portby`, then hold 443 on Windows, e.g. start Laravel Herd or run `powershell.exe "$l=[Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,443); $l.Start(); Start-Sleep 60"`. `portby doctor` reports `port 443: in use by Windows program <name> (pid N)`. Stop it, then start Portby again.

## After a restart

- [ ] Reboot (WSL2: `wsl.exe --shutdown` from Windows, then open a new shell).
- [ ] `sysctl net.ipv4.ip_unprivileged_port_start` is still `80`.
- [ ] The service started on its own (`systemctl --user is-active portby` prints `active`), and `portby doctor` is all `ok`.

## Results

| Date | Machine (distro, WSL mode) | Section | Result | Notes |
|---|---|---|---|---|
| | | | | |
