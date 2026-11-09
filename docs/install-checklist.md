# Manual checklist: `lane install` on Linux and WSL2

`lane install` changes the machine: it needs sudo, the system trust store, systemd and, on WSL2, the Windows certificate store and a Windows dialog. None of that runs in automated tests (they use `platformtest.Fake`), so run this checklist by hand on a Linux machine and on WSL2 after changing `internal/platform` or `internal/cli/install.go` / `doctor.go`.

Record each result as pass/fail, plus anything surprising, in the table at the end.

## Before you start

- [ ] Build the binary to where it will stay. The service runs this exact path, and the setcap fallback is tied to it. Don't use `go run`.
      `go build -o ~/.local/bin/lane ./cmd/lane`
- [ ] No other Laneway daemon is running (`pgrep -ax lane`), and nothing is listening on 443 (`ss -ltnp 'sport = :443'`; on WSL2 also `powershell.exe "Get-NetTCPConnection -State Listen -LocalPort 443"`).
- [ ] WSL2 only: systemd is on (`/etc/wsl.conf` has `[boot]` and `systemd=true`), and note the networking mode (`wslinfo --networking-mode`).
- [ ] `lane doctor` lists `FAIL` for the service, port permission and CA, and exits 1.

## Install

- [ ] `lane install` asks for the sudo password at most twice: once for the ports and once for the Linux CA store. sudo caches it, so often it asks only once.
- [ ] WSL2: a Windows "Security Warning: You are about to install a certificate from a certification authority (CA) claiming to represent: Laneway Local CA…" dialog appears. Click **Yes**. No UAC prompt.
- [ ] It ends with "Laneway is installed".

### Port permission

- [ ] `cat /etc/sysctl.d/99-laneway.conf` contains `net.ipv4.ip_unprivileged_port_start=80`.
- [ ] `sysctl net.ipv4.ip_unprivileged_port_start` prints `80`.
- [ ] If install said it fell back to setcap: `getcap ~/.local/bin/lane` shows `cap_net_bind_service=ep`.

### Service

- [ ] `systemctl --user status laneway.service` is `enabled` and `active (running)`.
- [ ] `~/.config/systemd/user/laneway.service` has `ExecStart=<your binary> daemon` and `Environment=PATH=<your shell's PATH>`.
- [ ] `ss -ltnp 'sport = :443'` shows `lane` listening on `127.0.0.1:443`.

### CA trust (Linux)

- [ ] The root is in the distribution's anchor directory as `laneway-local-ca.crt` (Arch: `/etc/ca-certificates/trust-source/anchors/`, Fedora: `/etc/pki/ca-trust/source/anchors/`, Debian/Ubuntu: `/usr/local/share/ca-certificates/`).
- [ ] Arch/Fedora: `trust list | grep -i -A2 laneway` lists it.
- [ ] Link a Project with a Primary Process (any `laneway.toml` with `[processes.web]`), then `curl https://<project>.localhost/` succeeds **without** `-k`.
- [ ] Linux desktop only: Chrome and Firefox open `https://<project>.localhost/` without a certificate warning. On Debian/Ubuntu, NSS doesn't read the system store, so expect a warning here and note it.

### CA trust (WSL2, Windows side)

- [ ] `powershell.exe "Get-ChildItem Cert:\CurrentUser\Root | Where-Object Subject -like '*Laneway*'"` lists the root.
- [ ] Chrome and Edge open `https://<project>.localhost/` with no warning. Note whether an already-open browser needed a restart.
- [ ] Firefox does too, after a restart. If it doesn't, note the value of `security.enterprise_roots.enabled` in `about:config`.
- [ ] Mirrored mode: the first request to a new Domain may take about 300 ms longer (the `::1` attempt times out first). That's expected.

## Plugins from the Catalog

The binary carries no Recipes, Integrations, Engines or Generators: a fresh install fetches the first-party Plugins a Project needs from the signed Catalog (`catalog.DefaultURL`, the latest release of lanewayhq/plugins). The binary trusts the Catalog signed with the key in `internal/catalog/fetch.go` (`catalogKey`).

- [ ] Before any Link, `lane plugin list` lists no Plugins, and `~/.local/state/laneway/plugins` doesn't exist or is empty.
- [ ] Link a fresh Laravel app (`laravel new`, then `git init`) with `lane link`. It prints `Fetched Plugin laravel … from the Catalog`, plus one line for each Engine its `.env.example` uses (such as `mysql`, `redis` and `mailpit`; none for SQLite), and the app is served at `https://<project>.localhost/`.
- [ ] `lane plugin list` shows them, with source `Catalog`.
- [ ] `lane harness add claude` in that app prints `Fetched Plugin claude … from the Catalog` the first time, and not the second.
- [ ] Offline on a fresh install: remove `~/.local/state/laneway/plugins` and `~/.local/state/laneway/catalog` with the daemon stopped, start it, disconnect from the network, and `lane link` a Laravel app. It fails with "this Project needs Plugins from the Catalog that Laneway couldn't fetch: laravel" (named from the Catalog snapshot built into Laneway). If no snapshot was built in yet, it Links with a warning that Laneway has never been able to fetch the Catalog instead.
- [ ] A Project with only a SQLite database (`[database] engine = "sqlite"`) Links and serves with Podman and Docker stopped, and fetches no Plugins.

## `lane open`

- [ ] In the Linked Project, `lane open` opens `https://<project>.localhost/` in the default browser. On WSL2 that's the Windows default browser, and no console window or "UNC paths are not supported" message appears.
- [ ] In a directory outside any Linked Project, `lane open` fails and tells you to run `lane link`.

## Running it again

- [ ] `lane install` again: no sudo prompt, no Windows dialog, every step says "already". The daemon isn't restarted (`systemctl --user status laneway` shows the same start time).
- [ ] `lane doctor` reports `ok` for daemon, service, port 443 ("bound by Laneway"), port permission, the CA in every store, container engine and mise (`warn` for mise is fine if it isn't installed), and exits 0.

## Port conflicts

- [ ] With Laneway running, start something on port 80, e.g. `python3 -m http.server 80 --bind 127.0.0.1`. `lane doctor` reports `FAIL port 80: in use by python3 (pid N)`. Stop it.
- [ ] `systemctl --user stop laneway`, then hold 443 with a root process, e.g. `sudo python3 -m http.server 443 --bind 127.0.0.1`. `lane doctor` reports port 443 in use by "a process Laneway can't see (another user's)". Stop it, then `systemctl --user start laneway`.
- [ ] WSL2 mirrored mode: `systemctl --user stop laneway`, then hold 443 on Windows, e.g. start Laravel Herd or run `powershell.exe "$l=[Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,443); $l.Start(); Start-Sleep 60"`. `lane doctor` reports `port 443: in use by Windows program <name> (pid N)`. Stop it, then start Laneway again.

## After a restart

- [ ] Reboot (WSL2: `wsl.exe --shutdown` from Windows, then open a new shell).
- [ ] `sysctl net.ipv4.ip_unprivileged_port_start` is still `80`.
- [ ] The service started on its own (`systemctl --user is-active laneway` prints `active`), and `lane doctor` is all `ok`.

## After an upgrade

- [ ] With a Project Linked, remove the installed Plugins (`systemctl --user stop laneway`, `rm -r ~/.local/state/laneway/plugins`, `systemctl --user start laneway`), as an upgrade that leaves them behind does. Without running anything else, `lane plugin list` lists the Project's Plugins again within a few seconds, and `lane info` in the Project shows its Recipe and no `Config` error.
- [ ] Do the same offline. `lane status` and `lane info` warn that Laneway couldn't fetch the Project's Plugins and say to run `lane doctor`. Back online, `lane doctor` fetches them and the warning goes.

## Results

| Date | Machine (distro, WSL mode) | Section | Result | Notes |
|---|---|---|---|---|
| | | | | |
