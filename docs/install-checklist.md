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
- [ ] Offline on a fresh install: remove `~/.local/state/laneway/plugins` and `~/.local/state/laneway/catalog` with the daemon stopped, disconnect from the network (before starting it, since it fetches what Linked Projects need when it starts), start it, and `lane link` a Laravel app. It fails with "this Project needs Plugins from the Catalog that Laneway couldn't fetch: laravel" (named from the Catalog snapshot built into Laneway). If no snapshot was built in yet, it Links with a warning that Laneway has never been able to fetch the Catalog instead.
- [ ] A Project with only a SQLite database (`[database] engine = "sqlite"`) Links and serves with Podman and Docker stopped, and fetches no Plugins.
- [ ] Offline on a fresh install, as above, `lane link` a Laravel app whose `laneway.toml` declares `rustfs = "1"` under `[services]`. The message names `rustfs` among the Plugins it couldn't fetch.

## Object storage (RustFS)

Run this before a release, and after changing Service Domains, Buckets, or the `rustfs` or Laravel Plugin. It goes through RustFS end to end, on the real image: the Service Domains (HTTPS from PHP and the browser), the Laravel Recipe's `.env` keys, a Bucket per Worktree, copying, the size limit, CORS, the console and Orphans. Run it with Podman, and again with Docker if you have it. Use a Laravel app from `laravel new` (the steps below call it `shop`) with a database, Linked and Ready, and with its Runtimes from mise as in the Laravel guide. The guide's §10 is the reference for what each step should do. Note the distribution: PHP trusting Laneway's CA is the part most likely to differ between them.

- [ ] Before the `rustfs` Plugin is installed (`lane plugin list` doesn't list it), `https://s3.laneway.localhost/` in a browser answers 502 and says nothing is served there yet, because no installed Engine declares it.
- [ ] `lane exec composer require league/flysystem-aws-s3-v3 "^3.0"`, then add `rustfs = "1"` under `[services]` in `laneway.toml`, plus a public Bucket wired into `[env]`:
      `[buckets.public]` with `public = true`, and `AWS_PUBLIC_BUCKET = "{rustfs_bucket_public}"`, `AWS_PUBLIC_URL = "{rustfs_endpoint}/{rustfs_bucket_public}"` under `[env]`. Add the guide's `public_s3` disk to `config/filesystems.php`. Commit.
- [ ] `lane link` (the app is Linked and Ready already). The first time on this machine it prints `Fetched Plugin rustfs … from the Catalog`. The Env Patch diff sets `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` to `laneway`, `AWS_DEFAULT_REGION=us-east-1`, `AWS_BUCKET=shop`, `AWS_ENDPOINT=https://s3.laneway.localhost`, `AWS_URL=https://s3.laneway.localhost/shop`, `AWS_USE_PATH_STYLE_ENDPOINT=true`, `AWS_PUBLIC_BUCKET=shop-public` and `AWS_PUBLIC_URL=https://s3.laneway.localhost/shop-public`, and doesn't touch `FILESYSTEM_DISK`.
- [ ] `lane worktree ready` exits 0 (the first time, after pulling the RustFS image). `lane info` lists `Buckets  shop on rustfs:1` and `shop-public on rustfs:1 (public)`, and `rustfs:1` among the Services on port 9000. `lane services` shows `rustfs` running.
- [ ] `curl https://s3.laneway.localhost/` (no `-k`) answers `403` with `AccessDenied` XML: TLS and the Service Domain work, and RustFS refuses unsigned requests.
- [ ] From PHP: `lane exec php artisan tinker --execute "Storage::disk('s3')->put('hello.txt', 'hello'); echo Storage::disk('s3')->get('hello.txt');"` prints `hello`. A `cURL error 60` here means PHP doesn't trust Laneway's CA: note the distribution and PHP build.
- [ ] `Storage::disk('s3')->temporaryUrl('hello.txt', now()->addMinutes(5))` opens in the browser and shows `hello`, with no certificate warning (WSL2: in a Windows browser). `Storage::disk('s3')->url('hello.txt')` answers 403 there.
- [ ] `Storage::disk('public_s3')->put('logo.txt', 'public')`, then `Storage::disk('public_s3')->url('logo.txt')` opens in the browser without signing.
- [ ] `Storage::disk('s3')->put('acl.txt', 'x', 'public')` returns `true`, and `Storage::disk('s3')->url('acl.txt')` still answers 403: RustFS ignores the ACL (on AWS with ACLs disabled the put fails instead).
- [ ] CORS: `curl -i -X OPTIONS -H 'Origin: https://shop.localhost' -H 'Access-Control-Request-Method: PUT' https://s3.laneway.localhost/shop/upload.txt` answers 200 with `access-control-allow-origin: https://shop.localhost`. With `Origin: https://other.localhost` it answers 403. After `lane worktree alias demo`, the same request from `https://demo.shop.localhost` answers 200 within a few seconds; remove the Alias afterwards.
- [ ] `lane worktree add feature-x`, then `lane worktree ready` there exits 0. Its `.env` has `AWS_BUCKET=shop--feature-x` and `AWS_PUBLIC_BUCKET=shop-public--feature-x`. `Storage::disk('s3')->get('hello.txt')` there prints `hello` (copied from the main Worktree). Delete it there, and the main Worktree still has it.
- [ ] Console: the Dashboard's Services page (`/services`) has a **Console** link on RustFS, to `https://rustfs.laneway.localhost/rustfs/console/`. It opens without a certificate warning, logging in as `laneway`/`laneway` works, and the four Buckets (`shop`, `shop-public`, `shop--feature-x`, `shop-public--feature-x`) are listed. `https://rustfs.laneway.localhost/` answers 403 `AccessDenied`.
- [ ] A client outside the app reaches `http://127.0.0.1:9000` as `laneway`/`laneway` (path-style, region `us-east-1`), and lists the same Buckets.
- [ ] Size limit: add `bucket_copy_limit = "1B"` at the top of `laneway.local.toml`, then `lane worktree add small`. `lane worktree ready` there exits 0 and prints `lane worktree: warning: the main Worktree's Bucket shop holds … B, more than the 1 B limit on copying a Bucket, so shop--small was created empty …`, naming `bucket_copy_limit`; `shop--small` is empty. Remove that line and the `small` Worktree afterwards.
- [ ] The first copy downloads `rc`: with RustFS's container removed (`lane services down rustfs`, then `podman rm` the container `podman ps -a` lists for it; its volume and files stay) and the network off, `lane worktree add offline` fails Preparing at `bucket shop--offline` with output about downloading `rc`. Back online, `lane worktree ready --retry` there exits 0. Remove the `offline` Worktree afterwards.
- [ ] Idle: once nothing has used RustFS for `service_idle_timeout` (set `service_idle_timeout = "1m"` in `~/.config/laneway/config.toml` to save waiting), `lane services` shows it stopped. Opening the Console link starts it again, and the Buckets and their objects are still there. So does a request to `https://s3.laneway.localhost/`.
- [ ] Orphans: `git worktree remove` the `feature-x` Worktree. `lane gc` lists `shop--feature-x` and `shop-public--feature-x` with the kind `bucket`, beside its databases. Drop them, and the console no longer lists them.
- [ ] Undo: `lane unlink`, then `lane gc --yes`. `shop` and `shop-public` are dropped with the database. With no Linked Project using RustFS, `https://s3.laneway.localhost/` and `https://rustfs.laneway.localhost/` answer 502 saying no Project uses it yet and naming `rustfs = "1"` to declare. Link the app again afterwards if you keep it.

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
- [ ] Do the same offline. `lane status` and `lane info` warn that Laneway couldn't fetch the Project's Plugins and say to run `lane doctor`. If the Project's config names an Engine, such as `engine = "postgres"` under `[database]`, `lane info`'s `Config` line says "the postgres Engine isn't installed; run `lane doctor` to fetch it from the Catalog", not `must be sqlite`. Back online, `lane doctor` fetches them and the warning and the `Config` error go.

## Results

| Date | Machine (distro, WSL mode) | Section | Result | Notes |
|---|---|---|---|---|
| | | | | |
