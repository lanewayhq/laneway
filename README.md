# Laneway

Laneway is a local web development environment manager for Linux and WSL2. It serves every Project at its own HTTPS domain, starts a Project's processes only when they're needed, and gives each git worktree its own isolated environment: its own domain, database, `.env` and setup run. That makes it a good fit for working on several branches at once, or for AI coding agents that each work in their own worktree.

```sh
cd ~/code/shop
lane link            # https://shop.localhost, its database, its .env
git worktree add ../shop-checkout -b checkout
                     # https://shop-checkout.shop.localhost, with its own copy of the database
```

> **Status:** pre-1.0. It works day to day, but commands, config and the Plugin API can still change between releases.

## What it does

- **HTTPS domains.** The main checkout is served at `https://<project>.localhost`, every other worktree at `https://<worktree>.<project>.localhost`, with a local CA Laneway installs into your system's (and on WSL2, Windows') trust store. Extra HTTP Processes, such as Vite, get `https://<process>.<domain>`.
- **Native Processes that start lazily.** Your web server, queue worker and asset bundler run as ordinary processes on your machine, started on the first request and stopped when the worktree goes idle. Runtimes (PHP, Node, Ruby…) come from [mise](https://mise.jdx.dev).
- **Shared Services, also lazy.** Postgres, MySQL, MariaDB, Redis, Mailpit and RustFS (S3-compatible object storage) run as one container per engine and version, shared by every Project, started on first use. Mailpit's inbox and RustFS's S3 API and console are served over HTTPS at Service Domains such as `https://s3.laneway.localhost`. Needs Podman (preferred) or Docker; SQLite Projects need neither.
- **A database per worktree.** Each worktree gets its own database (a copy of the main one, or a fresh one), plus a separate Test Database when your tests use one, so test runs never touch each other's data.
- **A Bucket per worktree.** A Project that declares RustFS gives each worktree its own private Bucket, a copy of the main one's, plus any public or private Buckets the Project declares, so no worktree sees or deletes another's files. Databases and Buckets of removed worktrees are kept for 24 hours before they're dropped.
- **An Env Patch, not an env rewrite.** Laneway writes only the `.env` keys it owns (such as `APP_URL`, `DB_*`, `MAIL_*` and, with RustFS, `AWS_*`) and never touches the rest.
- **Config for your AI tools.** `lane harness add claude` (or `t3`) merges Laneway's instructions into your agent's config files without overwriting yours.
- **A Dashboard** at `https://laneway.localhost` with every Project, Process, Service and log.

## Plugins

Laneway's knowledge of stacks, tools and services comes from **Plugins**: Laravel, Rails and Vite, the Postgres, MySQL, MariaDB, Redis, Mailpit and RustFS engines, and the Claude Code, T3 Code and Laravel Boost generators. They live in [lanewayhq/plugins](https://github.com/lanewayhq/plugins) and are listed in a signed Catalog. Laneway fetches the ones a Project needs the first time you Link it, and again whenever it starts if any are missing, such as after an upgrade. You can also add your own (`lane plugin add <dir|github.com/owner/repo>`) or write one: see the [Plugin authoring guide](https://github.com/lanewayhq/plugins/blob/main/docs/authoring.md).

## Install

Laneway runs on Linux (x86_64 and arm64) and WSL2 with systemd. You need git, and for Services Podman or Docker; mise provides your Projects' runtimes.

```sh
curl -fsSL https://raw.githubusercontent.com/lanewayhq/laneway/main/install.sh | sh
```

It downloads the latest release into `~/.local/bin/lane` after checking it against the release's checksums. Set `LANEWAY_VERSION=v0.1.0` for a specific release, or `LANEWAY_INSTALL_DIR` for another directory. Running it again upgrades Laneway and restarts its service. Or, with Go 1.27:

```sh
go install github.com/lanewayhq/laneway/cmd/lane@latest
```

Then set up the service, ports 80 and 443, and the local CA (it asks for sudo at most twice), and check everything:

```sh
lane install
lane doctor
```

## Quick start

```sh
cd ~/code/my-laravel-app     # any git repository
lane link                    # shows the .env changes it will make, then Links the Project
lane worktree ready          # waits for its database and setup steps
lane open                    # https://my-laravel-app.localhost
```

`lane info` shows the worktree's URL, Processes, databases, Buckets and Services; `lane status` lists every Project and its Worktrees, with whether each Worktree's work is merged into the Default Branch. `lane init` writes the resolved config to `laneway.toml`, so you can see and change what Laneway runs. `lane help` lists every command.

For a full walkthrough, from a fresh machine to a Laravel + React + Postgres app with worktrees, see [docs/guides/laravel-react-postgres.md](docs/guides/laravel-react-postgres.md). It ends with object storage: a Bucket per worktree for Laravel's `s3` disk. For MariaDB, including an app on Laravel's `mysql` connection whose production runs MariaDB, see [docs/guides/mariadb.md](docs/guides/mariadb.md).

## Learn more

- [GLOSSARY.md](GLOSSARY.md): the words Laneway uses (Project, Worktree, Process, Service, Plugin…) and what they mean.
- [docs/adr](docs/adr): the decisions behind its design.

## Development

```sh
go test ./...                                              # fast suite: a real daemon, fake container engine
LANEWAY_SLOW=1 go test ./e2e -run RealContainerEngine      # slow tier: real Docker and Podman
```

Issues and pull requests: [github.com/lanewayhq/laneway](https://github.com/lanewayhq/laneway). See [CONTRIBUTING.md](CONTRIBUTING.md), and [docs/development-flow.md](docs/development-flow.md) for how features, fixes and releases flow through this repository and lanewayhq/plugins.

## License

Laneway is licensed under the [Apache License 2.0](LICENSE).
