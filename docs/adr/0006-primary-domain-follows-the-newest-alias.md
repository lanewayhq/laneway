# A Worktree's newest Alias becomes its Primary Domain, and browsers are redirected to it

Harnesses name worktrees at random (T3 Code's `t3-298e5f25`), and a Worktree's Domain is named after git's name for it, so its URL was random too. An Alias only added another name: `APP_URL`, `portby info`'s `url` and Vite's live-reload host stayed on the random Domain. Now a Worktree's newest Alias is its Primary Domain, and everything Portby gives out follows it: the Env Patch's `{url}` and `{domain}`, `portby info`'s `url`, `<process>.<Primary Domain>` hosts and the Dashboard. The Domain stays the Worktree's identity, and its database names still come from its label, so naming a Worktree never touches its data. Changing the Primary Domain re-applies the Env Patch and restarts the Worktree's running Processes.

Every other name of the Worktree (its Domain and older Aliases) still serves it, but a browser loading a page there is sent to the Primary Domain with an uncached 307. Cookies are per host, and code that compares the request host with `APP_URL` (Sanctum's stateful domains, OAuth callbacks) misbehaves on any other name, so a browser should end up on a single origin. Only page loads are redirected, so fetches, form posts, webhooks and `curl` keep working on the old names. Process hosts are never redirected, because stale pages still load assets from them. The redirect isn't cached, so removing the Alias undoes it.

## Considered Options

- **Renaming the Worktree's label**, so the Domain itself changes. Rejected: the Domain names a running Worktree internally, its database names come from the label, and the glossary ties the Domain to git's name for the Worktree.
- **Choosing the primary Alias with a flag.** Rejected: an agent should only have to run `portby worktree alias <name>`.
- **No redirect.** Rejected: a tab opened on the random Domain before the Worktree was named would stay there, logged in separately from the links in `APP_URL`'s emails.
- **Redirecting every request.** Rejected: it would break POSTs, cross-origin fetches and webhooks, and a cached redirect would outlive the Alias.
