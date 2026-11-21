# A Root Domain the developer owns lets their other devices reach Worktrees at the same URLs

A developer wants to open their Worktrees on their phone, at the same URLs their PC uses, because `APP_URL`, Vite's and Reverb's hosts and the other Env Patch values are fixed per Worktree. `.localhost` can't do this: every device resolves it to itself. So a developer may set a **Root Domain** in their personal config, such as `lw.example.com`, and every Domain, Alias, `<process>.<domain>` host, the Dashboard and its Service Domains move under it. `localhost` remains the default. The developer points a wildcard DNS record, `*.lw.example.com`, at an address their devices reach the PC on, usually its Tailscale IP. Laneway also listens on that address, beside loopback, when its personal config names it (`listen`, which takes `tailscale` or an IP). Nothing assumes Tailscale: any network that gives the PC a stable address works, and with neither setting Laneway behaves as before.

One public wildcard record covers every depth, so `vite.<worktree>.<project>.lw.example.com` works with no new code to resolve names. Certificates still come from Laneway's local CA, issued on demand for any name. Each device the developer uses installs the CA's root once, from a page served over plain HTTP at `http://laneway.<Root Domain>/ca`. This is only for the developer's own devices: sharing a Worktree with someone else is a separate problem.

Once a Root Domain is set, `.localhost` names stop being served. A page load there gets a page naming the new URL, and anything else gets an error. We do not redirect them as ADR-0006 does for older Aliases. A browser on the PC resolves `.localhost` itself, so a redirect would keep a stray `.localhost` URL in a developer's own env or code working on the PC while it fails on the phone. Failing on the PC shows the mistake where it can be fixed. Domains and Aliases under the Root Domain keep ADR-0006's redirect to the Primary Domain.

## Considered Options

- **Tailscale MagicDNS names (`<machine>.<tailnet>.ts.net`), Serve or Funnel.** Rejected: one hostname per machine, with no subdomains or custom records, and `tailscale cert` issues no wildcards. Worktree and Process hosts need many names at any depth.
- **Cloudflare Tunnel.** Rejected: free certificates cover one level (`*.example.com`), so `<process>.<worktree>.<project>` hosts need a paid certificate plan. It also puts dev apps on the public internet.
- **sslip.io-style names with the IP in them (`glowyfly.100-64-1-2.sslip.io`).** Rejected as the main path: the URL carries an IP and depends on a third party's DNS. It remains a Root Domain a developer without a domain can set.
- **Laneway answering DNS itself, with Tailscale Split DNS for a private name such as `lw.internal`.** Rejected for now: it ties the feature to Tailscale and needs port 53 and more firewall setup on WSL2. A loopback-only version of it is how #127 may let the PC resolve its own Worktrees offline.
- **Public certificates via ACME DNS-01.** Rejected: every Domain and Worktree name would be published in Certificate Transparency logs, exposing client and branch names. Each Worktree would need its own wildcard certificate for its Process hosts, and the Let's Encrypt limit of 50 certificates per domain per week doesn't fit Harnesses creating Worktrees all day. It would spare each device installing the CA, which matters only when sharing with other people.
- **Listening on every interface with a list of allowed source addresses.** Rejected: one wrong setting exposes every Worktree, the Dashboard and Service Domains on any network the PC joins. Laneway listens only on the addresses named in config, adding one when it comes up.
- **Redirecting or serving `.localhost` names alongside the Root Domain.** Rejected: serving both splits cookies and origins (ADR-0006), and redirecting hides stale URLs on the PC.
- **A Root Domain per Project.** Rejected: a committed `laneway.toml` could then force a domain on teammates, and one developer's devices need just one.

## Consequences

- With a public record pointing at a tailnet IP, the PC needs internet (and the address up) to resolve its own Worktrees. Until #127, the workaround offline is setting the Root Domain back to `localhost`, which changes URLs while offline.
- Changing the Root Domain re-applies every Worktree's Env Patch and restarts its running Processes. Buckets' CORS origins follow, as they follow Aliases. `lane root-domain set` first checks that a random name under the new Root Domain resolves to an address Laneway listens on, so a missing wildcard record doesn't rewrite every `APP_URL` to a broken name.
- Every device on the network Laneway listens on reaches every Worktree, the Dashboard (which can remove Worktrees) and the Service Domains. Who that is, is the network's call, such as Tailscale's ACLs.
- Values Laneway doesn't own still name `.localhost`. `lane doctor` and the Dashboard list env keys that do.
- The generated Vite config can no longer rely on Vite's and laravel-vite-plugin's defaults, which allow only `localhost` names (`allowedHosts`, CORS).
- ADR-0003's dashboard at `laneway.localhost` is at `laneway.<Root Domain>`.
