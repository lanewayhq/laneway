# Open your Worktrees on your phone

By default every Domain is under `localhost`: `https://shop.localhost`, `https://feature-x.shop.localhost`, `https://laneway.localhost`. Every device resolves `localhost` to itself, so only your PC reaches them, and your phone never will.

This guide moves every name under a **Root Domain** you own instead, such as `lw.example.com`. Your PC and your phone (or tablet, or laptop) then open your Worktrees at the same URLs, all the time: `https://shop.lw.example.com`, `https://feature-x.shop.lw.example.com`, `https://laneway.lw.example.com`. They have to be the same, because each Worktree's Env Patch writes its URL into `APP_URL`, Vite's and Reverb's hosts, `AWS_URL` and similar keys, and the app builds its links, asset URLs, WebSocket connections and cookies from them.

Everything follows the Root Domain: each Worktree's Domain and Aliases, `<process>.<domain>` hosts such as `vite.feature-x.shop.lw.example.com`, the Dashboard at `laneway.<Root Domain>` and its Service Domains (`mail.laneway.<Root Domain>`, `s3.laneway.<Root Domain>`). Vite's dev server and its live reload, Reverb's WebSockets, Mailpit's inbox and RustFS's presigned URLs all work on your phone over HTTPS, with certificates from Laneway's own CA. The design, and the options it rejected, are in [ADR-0009](../adr/0009-root-domain-for-reaching-worktrees-from-other-devices.md).

You need:

- **A domain you own**, where you can add DNS records. You'll use a subdomain of it, so its other records aren't touched.
- **An address your devices reach your PC on**, which doesn't change. Tailscale is the easiest: your PC's Tailscale IP stays the same wherever it is, and only your own devices reach it. Nothing requires Tailscale: an address from ZeroTier, Netbird or your LAN (reserved in your router so it doesn't change) works too.
- **Laneway set up on your PC** (`lane install`, and `lane doctor` all OK).

The examples use the Root Domain `lw.example.com`, the Tailscale IP `100.101.102.103` and a Project called `shop`. Change them to yours.

---

## 1. Pick a Root Domain and create its DNS record

Pick a name under your domain for Laneway, such as `lw.example.com`. Every name Laneway serves will end in it.

At your DNS provider, create one wildcard record that points every name under it at your PC's address:

| Name               | Type | Value             |
| ------------------ | ---- | ----------------- |
| `*.lw.example.com` | A    | `100.101.102.103` |

Use `AAAA` for an IPv6 address. `tailscale ip -4` prints your PC's Tailscale IPv4 address (on WSL2, `tailscale.exe ip -4`).

- One record covers every depth: `shop.lw.example.com`, `feature-x.shop.lw.example.com` and `vite.feature-x.shop.lw.example.com` all match it. You never add a record per Project or Worktree.
- If your DNS provider can proxy records, as Cloudflare can, make this one **DNS only**. Nothing on the internet can reach a Tailscale address, so a proxy can't either.
- Some routers drop DNS answers that point at private LAN addresses ("DNS rebind protection"). If you use a LAN address and names don't resolve at home, allow your Root Domain in the router's settings.
- A name only your own DNS server answers, such as `dev.internal`, works too, as long as every device uses that DNS server.

The record is public, so anyone can look up the address in it. They learn nothing else: the wildcard names no Project or Worktree, and the certificates come from Laneway's own CA, so your Worktrees' names don't appear in public certificate logs either.

## 2. Listen on that address

Laneway listens only on loopback (`127.0.0.1`) unless you tell it otherwise. Add the address to your personal config, `~/.config/laneway/config.toml`, beside `idle_timeout` and the other keys at the top of the file:

```toml
listen = ["tailscale"]
```

Each entry is an IP address, such as `"100.101.102.103"` or `"192.168.1.20"`, or `tailscale`, which Laneway turns into your PC's Tailscale IPv4 address itself. Laneway listens on ports 443 and 80 there, beside loopback.

- Laneway picks the change up within about ten seconds; there's no need to restart it.
- If the address isn't up yet, for example because Tailscale starts after Laneway, Laneway keeps trying, and listens on it once it appears. If the address changes, Laneway follows it.
- Laneway never listens on every interface: `0.0.0.0` and `::` are refused.
- `listen` and `root_domain` only go in your personal config. A Project's `laneway.toml` and `laneway.local.toml` can't set them, so a Project never forces your setup on a teammate.

`lane root-domain` shows what Laneway listens on:

```
Root Domain:  localhost
Dashboard:    https://laneway.localhost/
Listening on: 127.0.0.1 (loopback)
              tailscale 100.101.102.103 (listening)
DNS:          not needed: every device resolves names under localhost to itself, so only this machine reaches them
```

If an address isn't listened on, the line says why, such as `tailscale (not listening: Tailscale has no IPv4 address: …)`.

### 2.1 WSL2: mirrored networking and Windows' firewall

On WSL2, Tailscale runs on Windows, not inside WSL, and `tailscale` finds Windows' Tailscale address through `tailscale.exe`. For Laneway, inside WSL, to listen on a Windows address, WSL has to use mirrored networking. In `%UserProfile%\.wslconfig` on Windows:

```ini
[wsl2]
networkingMode=mirrored
```

Then run `wsl.exe --shutdown` from Windows and open a new shell. `wslinfo --networking-mode` prints `mirrored` once it's on.

In mirrored mode, Windows' Hyper-V firewall stands between other devices and WSL. Whether it lets them in depends on how Windows is set up, and `lane doctor` asks Windows and says, on its `Windows firewall` line. If it blocks them, your phone times out; allow ports 80 and 443 with this command, in a PowerShell window opened with **Run as administrator** on Windows:

```powershell
New-NetFirewallHyperVRule -Name Laneway -DisplayName "Laneway (HTTP and HTTPS)" -Direction Inbound -VMCreatorId '{40E0AC32-46A5-438A-A0B2-2B479E8F2E90}' -Protocol TCP -LocalPorts 80,443
```

The GUID is the one Windows uses for WSL. Laneway never changes Windows' firewall itself. The machine this was checked on let other devices in without a rule, so the command hasn't been tried where one is needed; if it doesn't work for you, please open an issue.

## 3. Switch to the Root Domain

```sh
lane root-domain set lw.example.com
```

Before it changes anything, it looks up a random name under `lw.example.com` and `laneway.lw.example.com`, and checks that each resolves to an address Laneway listens on (or will, from `listen`). This stops a missing or wrong record from rewriting every `APP_URL` to a name that doesn't work. If the check fails, it says which name failed and the record to create, such as `*.lw.example.com A 100.101.102.103`, and changes nothing. A new record can take a few minutes to reach your resolver; `--skip-dns-check` switches anyway, at your own risk.

When it succeeds, it writes `root_domain = "lw.example.com"` into your personal config, keeping the rest of the file, and the daemon applies it at once:

```
Root Domain is now lw.example.com (was localhost)
Dashboard: https://laneway.lw.example.com/
Applied the Env Patch of 3 Worktrees again
Restarted shop.lw.example.com: web, vite
Install Laneway's CA on your other devices from http://laneway.lw.example.com/ca
```

- **Every Worktree's Env Patch is applied again**, so `APP_URL`, Reverb's host, `AWS_ENDPOINT`, `AWS_URL` and anything else rendered from `{url}` or `{domain}` use the new names. Worktrees that aren't running get it too, and start with the right URLs.
- **Running Processes restart** so they read it. Stopped ones aren't started.
- **The generated Vite config** accepts hosts under the Root Domain, so Vite doesn't answer your phone with "Blocked request. This host is not allowed", allows pages under it to load its modules, and its live reload connects at `vite.<domain>` under it.
- **Buckets' allowed browser origins** follow, so browser uploads to RustFS keep working.
- **Databases keep their names and data.** Changing the Root Domain never touches them.
- You'll have to log in again in each app, since its cookies belonged to the old names. Passkeys belong to the host they were registered on, so register new ones if you use them.

`lane root-domain` now also checks DNS:

```
Root Domain:  lw.example.com
Dashboard:    https://laneway.lw.example.com/
CA page:      http://laneway.lw.example.com/ca
Listening on: 127.0.0.1 (loopback)
              tailscale 100.101.102.103 (listening)
DNS:          names under lw.example.com resolve to 100.101.102.103, where Laneway listens
```

Editing `root_domain` in `~/.config/laneway/config.toml` by hand works too: the daemon applies it, with the same steps, when it next starts (`systemctl --user restart laneway`). It doesn't check DNS first.

Once a Root Domain is set, `.localhost` names aren't served any more, even on your PC. A page you open at a `.localhost` name shows **This address has moved**, with a link to the same page under the Root Domain; anything else (an asset, a fetch, a form post, a WebSocket) gets a `404` with one line naming the new URL, and never reaches the app. This is on purpose: a stale `.localhost` URL in your own env or code breaks on your PC, where you'll notice it, not only on your phone. §5 covers fixing them.

## 4. Trust Laneway's CA on your phone

Your PC already trusts Laneway's CA (`lane install` added it). Each other device installs its root certificate once, from a page Laneway serves over plain HTTP, so it opens before the device trusts anything:

```
http://laneway.lw.example.com/ca
```

The easiest way there: open the Dashboard on your PC. Its overview has a **Remote access** card with the Root Domain, the addresses Laneway listens on, and a QR code for this page. Scan it with your phone's camera. Your phone must be on the network the address belongs to: for a Tailscale address, with the Tailscale app connected.

The page has the steps for each platform. In short:

- **iPhone and iPad:** open the page in Safari and tap **Download the profile**, then **Allow**. In **Settings**, tap **Profile Downloaded** (or **General** → **VPN & Device Management**) → **Laneway** → **Install**. Then **Settings** → **General** → **About** → **Certificate Trust Settings**, and turn on full trust for Laneway's certificate (the page names it). Without that last step, Safari still warns.
- **Android:** tap **Download the certificate**, then in **Settings** search for **CA certificate** (usually **Security & privacy** → **More security settings** → **Encryption & credentials** → **Install a certificate** → **CA certificate**), tap **Install anyway** and pick `laneway-root.crt`. Chrome and most other browsers then trust it.
- **Firefox on Android** keeps its own list: install the certificate as above, then turn on the debug menu (**Settings** → **About Firefox**, tap the logo until it says so) and, in **Settings** → **Secret Settings**, turn on **Use third party CA certificates**.

Only the public root certificate is ever offered; its private key never leaves your PC. Install it only on devices you own: a device that trusts it trusts every certificate Laneway issues.

Now open `https://laneway.lw.example.com` on your phone for the Dashboard, and `https://shop.lw.example.com` for the app.

## 5. Fix env keys that still name `.localhost`

Laneway only changes the keys in the Env Patch. A key you added yourself, such as `VITE_WEBSITE_URL=https://website.localhost`, keeps its old value, and now breaks on your PC as well as your phone (§3). Laneway lists these keys, by file and key name (never their values, since env files hold secrets):

- `lane doctor` warns about each Worktree file with such keys.
- The Dashboard shows a warning on the Project's page.
- `lane info --json` has them as `staleEnvKeys`.

The list covers keys outside the Env Patch whose values name a host under `localhost` or under your previous Root Domain, in each Worktree's `.env` and test env file. Change each value to the Worktree's URL, which `lane info` shows, then `lane restart` so the Processes read it.

If the value is the Worktree's own URL or Domain, let Laneway own the key instead: add it to the `[env]` table of `laneway.toml` (or `laneway.local.toml`, for you only) with a template, such as `VITE_APP_URL = "{url}"`. It's then part of the Env Patch, and follows the Root Domain, Aliases and every Worktree from then on.

URLs written in code or other config files aren't listed. They fail on your PC with the page or one-line error from §3, which names the URL to use; your browser's network tab shows which request it was.

## 6. What's exposed, and who can reach it

Every device that can reach an address in `listen` can reach, on ports 443 and 80:

- **every Worktree** of every Project, as if it were your PC;
- **the Dashboard**, which can start and stop Processes and remove Worktrees: other devices aren't limited to reading it;
- **the Service Domains**: Mailpit's inbox, with every mail your apps caught, and RustFS's S3 API and console (logged in as `laneway`/`laneway`).

Nothing else is: databases, Redis and the other Services' ports, and your Processes' own ports, stay on loopback. Laneway only issues certificates for names it serves under your Root Domain, so someone who reaches the address can't make it issue one for any other name.

Who that is depends on the address, and is up to your network, not Laneway:

- **A Tailscale address:** the devices on your tailnet that its access controls allow, including any you've shared your PC with. Restrict them with Tailscale's ACLs if you share your tailnet.
- **A LAN address:** everyone on that network, including guests on the same Wi-Fi.

Because Laneway never listens on every interface, joining a café's Wi-Fi doesn't expose anything. To stop other devices reaching your PC, remove the entry from `listen`; Laneway stops listening on it within seconds.

## 7. Working offline

With a public DNS record, your PC resolves its own Worktrees through the internet, and the record points at an address that must be up. Without internet (or with Tailscale down, for a Tailscale address), even your PC can't open them. Until Laneway can resolve them on your PC itself ([#127](https://github.com/lanewayhq/laneway/issues/127)), switch back to `localhost` while you're offline:

```sh
lane root-domain reset
```

It never checks DNS. Everything moves back under `localhost` as in §3, in reverse: Env Patches are applied again, running Processes restart, and `.localhost` names are served again. Your phone can't reach anything meanwhile. When you're back online:

```sh
lane root-domain set lw.example.com
```

## 8. When something doesn't work

Run `lane doctor` on your PC first. As well as its other checks, it looks at your Root Domain and `listen` setup and says what's wrong, such as a name that doesn't resolve to an address Laneway listens on, an address that isn't up, or a half-done setup (listening beyond loopback with the Root Domain still `localhost`, or a Root Domain with nothing but loopback). `lane root-domain` shows the same state in brief, and the Dashboard's **Remote access** card shows each address and whether Laneway listens on it.

| Symptom | Cause and fix |
| --- | --- |
| `lane root-domain set`: "DNS doesn't send names under lw.example.com to an address Laneway listens on" | The wildcard record is missing, points at another address, or hasn't reached your resolver yet. Create the record it prints (§1), wait a few minutes and try again. If the record says `<the address your other devices reach this machine on>`, add that address to `listen` first (§2). |
| `lane root-domain` shows `tailscale (not listening: Tailscale isn't installed …)` or `… has no IPv4 address` | Install Tailscale (on WSL2, on Windows), log in and connect. Laneway starts listening within seconds. |
| The phone times out, but the PC opens the Worktree | The phone isn't on the network the address belongs to (connect Tailscale on it), or, on WSL2, Windows' firewall blocks it (§2.1). |
| The phone warns about the certificate | The CA isn't installed or, on iPhone, not fully trusted (§4). In Firefox on Android, turn on **Use third party CA certificates**. |
| A page shows **This address has moved** | Something still uses a `.localhost` URL (§3). Follow the link, and fix the bookmark, env key (§5) or code it came from. |
| Assets, fetches or WebSockets fail with a `404` naming another URL | The same, for a request other than a page load: fix the URL it names. |

## 9. Going back

`lane root-domain reset` moves everything back under `localhost`. To stop other devices reaching your PC too, remove `listen` from `~/.config/laneway/config.toml`, and delete the DNS record. On WSL2, remove the firewall rule in an administrator PowerShell:

```powershell
Remove-NetFirewallHyperVRule -Name Laneway
```

Uninstall Laneway's CA from your other devices: on iPhone, **Settings** → **General** → **VPN & Device Management** → **Laneway** → **Remove Profile**; on Android, under **Encryption & credentials** → **Trusted credentials** → **User**.
