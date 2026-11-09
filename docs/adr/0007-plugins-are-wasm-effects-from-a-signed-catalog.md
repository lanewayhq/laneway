# Recipes, Integrations, Engines and Generators come from Plugins: data or WASM that returns effects, fetched from a signed Catalog

Laneway's knowledge of stacks (Laravel), tools (Vite), Engines (Postgres, MySQL, Redis, Mailpit) and Harnesses (Claude Code, T3 Code) moves out of core into Plugins, so others can add support for theirs without a change to Laneway, and core shrinks to what every Project needs. First-party Plugins live in one `lanewayhq/plugins` repository and are listed in the Catalog. Laneway fetches a Catalog Plugin on its own the first time a Project needs it; any other Plugin is added by hand. SQLite stays in core, so a Project needs no container engine until it uses a Service.

Engines are data: an image, a port, a readiness probe from a fixed set of kinds, and their database operations as commands run inside the Service's container (`psql`, `mysqldump`), so Laneway no longer carries its own Postgres and MySQL clients. Everything else a Plugin does (Recipes, Integrations, hooks, CLI commands, Generators) is a WASM module run as a WASI command: Laneway passes a JSON request on stdin with the Project mounted read-only, and reads back JSON effects (env keys, Processes, commands to run, file blocks to merge, HTTP requests to listed hosts, warnings) which Laneway itself applies, under its usual rules: it only writes what it owns, merges rather than overwrites, and is safe to re-run. A Plugin can only return the kinds of effect its manifest declares, and the developer sees them when adding it.

The Catalog is signed with a key built into Laneway and lists a SHA-256 for each Plugin release; a hand-added Plugin is pinned to the hash it had when added. A Plugin stays at the version first installed until `lane plugin update`, because a new version can change the Env Patch of every Worktree, including ones a Harness is working in.

## Considered Options

- **Go's `plugin` package.** Rejected: it needs the exact toolchain Laneway was built with and doesn't work on Windows.
- **Native executables over stdin/stdout.** Rejected: one build per OS and architecture, and, because Catalog Plugins are fetched without asking, unsandboxed code from the internet would run on every Link.
- **Extism, or the WASM Component Model.** Rejected for now: every call has the same shape (data in, effects out), so a plain WASI command needs neither Extism's host functions nor typed interfaces. Extism's Go SDK has also had no release since March 2025. The Component Model is the upgrade if host functions are ever needed.
- **Hooks that act directly** (write files, run commands, open sockets). Rejected: returning effects is just as powerful, since running a command is an effect, but what a Plugin did stays visible and testable and can't break Laneway's guarantees.
- **Everything declarative.** Rejected: detecting and configuring a stack (reading Laravel's `.env`, Rails' `database.yml`) needs real logic.

## Consequences

- A Project has at most one Recipe, which alone may choose the Primary Process, plus any number of Integrations. Two Integrations that set the same thing are a resolve error naming both, settled in `laneway.toml`; no Plugin silently wins.
- Recipes and Integrations declare their detection rules as data, so Laneway can match a Project against Plugins it hasn't fetched yet.
- A Plugin's hooks fire only for Projects that use it. A machine-wide Plugin can only be turned on in personal config, never in a committed `laneway.toml`.
- Linking a Project that needs a Catalog Plugin not yet installed fails offline.
- Until the Catalog exists (lanewayhq/laneway#72), the first-party Plugins (Laravel, Rails, Vite, Claude Code, T3 Code, Laravel Boost, and the Postgres, MySQL, Redis and Mailpit Engines) are built into the `lane` binary rather than fetched. They are still Plugins: their WASM modules run in the same sandbox as any other, and they move to `lanewayhq/plugins` unchanged.
- Plugins declare the Plugin API version they target. Until Laneway reaches 1.0, only the current version is accepted and it may change freely.
