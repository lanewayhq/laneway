# Manual: a new Laravel + Inertia + React + PostgreSQL app on Laneway

This manual takes a fresh Linux or WSL2 machine that only has the `lane` binary and ends with a Laravel app (the official React starter kit: Inertia, React, TypeScript, Tailwind) running on PostgreSQL. Along the way it uses everything Laneway does for a Laravel Project:

- HTTPS Domains: `https://shop.localhost`, with Vite's dev server and live reload at `https://vite.shop.localhost`
- Processes that start lazily: the web server, the queue worker and Vite start on the first request and stop when the Worktree is Idle
- Shared Services that also start lazily: one Postgres container for every Project, and Mailpit to catch mail
- An Env Patch: Laneway owns only `APP_URL`, `DB_*` and `MAIL_*` in `.env`, and leaves every other key alone
- A Test Database, so `php artisan test` runs against Postgres in its own database
- Worktrees: every git worktree gets its own Domain, its own copy of the database, its own `.env` and its own setup run
- Aliases, Orphans and `lane gc`, the Dashboard, `lane exec`, logs, and Harness config for AI agents

Runtimes come from **mise**, per project and from prebuilt packages only: nothing is compiled, and no PHP, Composer, Node or Bun is installed globally. The app is created with the `laravel` CLI (the Laravel installer), and Bun is its package manager.

Commands below use `shop` as the Project name and `~/code` as the place you keep projects. Change both to suit you.

---

## 1. Install the machine prerequisites

You need git, a container engine for the Services, and mise for the Runtimes.

### 1.1 Git and Podman

Laneway runs Postgres and Mailpit as containers. It prefers rootless Podman; Docker works too.

```sh
# Debian / Ubuntu
sudo apt update && sudo apt install -y git podman unzip

# Arch
sudo pacman -S --needed git podman unzip

# Fedora
sudo dnf install -y git podman unzip
```

`unzip` is optional. Composer works without it, just more slowly.

**WSL2 only:** Laneway runs as a systemd user service. Make sure `/etc/wsl.conf` contains:

```ini
[boot]
systemd=true
```

If you had to add it, run `wsl.exe --shutdown` from Windows and open a new shell.

### 1.2 mise

```sh
curl https://mise.run | sh                       # installs ~/.local/bin/mise
echo 'eval "$(~/.local/bin/mise activate bash)"' >> ~/.bashrc   # zsh: ~/.zshrc and "activate zsh"
exec $SHELL -l
mise --version
```

Then tell mise to trust every config file under `~/code`:

```sh
mkdir -p ~/code
mise settings add trusted_config_paths ~/code
```

**Why this matters:** each git worktree is a new directory with its own copy of `mise.toml`, and mise refuses config files it hasn't been told to trust. When mise refuses, Laneway falls back to `PATH`, finds no PHP, and the new Worktree's setup fails at `composer install`. Trusting `~/code` (where Laneway also puts worktrees, see §8) avoids running `mise trust` in every worktree.

---

## 2. Set up Laneway

`lane install` writes the systemd unit with the `PATH` of the shell you run it from. The daemon only needs `mise` on that `PATH`: it asks mise for each Worktree's Runtimes itself. So run this after §1.2:

```sh
lane install
```

It asks for sudo at most twice: once to let Laneway bind ports 80 and 443, and once to add its local CA to the system trust store. It also enables `podman.socket`. On WSL2, Windows shows a "Security Warning… Laneway Local CA" dialog: click **Yes**, so Windows browsers trust `*.localhost` certificates.

```sh
lane doctor
```

Daemon, service, ports 443 and 80, port permission, the CA in each store, the container engine and mise should all be `ok`. No PHP or Node is reported yet: there's no project, and nothing is installed globally.

> Ran `lane install` before installing mise? Run it again. It rewrites the unit with your current `PATH` and restarts the daemon. Nothing else changes.

---

## 3. Create the project folder with its own Runtimes

Nothing is installed globally. The project's `mise.toml` declares PHP, Composer, Node and Bun, and they're only on your `PATH` inside the project, and in Laneway's Processes and setup steps for it.

mise's default `php` plugin (`vfox:jdx/vfox-php`) **builds PHP from source**: it needs a compiler toolchain and about 15 `-dev` packages, takes a long time, and you only get `pdo_pgsql` if `libpq` headers happen to be installed. Don't use it. Use these prebuilt ones:

| Tool     | mise backend               | What you get                                                                                                          |
| -------- | -------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| PHP      | `conda:php`                | Prebuilt PHP from conda-forge. Installs in seconds, with `pdo_pgsql`, `pgsql`, `intl`, `pcntl`, OPcache, `zip`, `sodium`, `bcmath` and `pdo_sqlite`. |
| Composer | `github:composer/composer` | The official `composer.phar` from Composer's GitHub releases, installed as `composer`                                 |
| Node     | `node` (mise core)         | Official Node binaries. Vite+ (`vp`) runs on Node, and the starter kit's own setup hook runs `npm install` once        |
| Bun      | `bun` (mise core)          | Official Bun binaries. The package manager: installs `node_modules` and writes `bun.lock`                             |

```sh
mkdir -p ~/code/shop && cd ~/code/shop

cat > mise.toml <<'TOML'
[tools]
"conda:php" = "8.5"
"github:composer/composer" = { version = "2", bin = "composer" }
node = "24"
bun = "1"
TOML

mise install
php -v                 # PHP 8.5.x (only inside ~/code/shop)
php -m | grep -i pgsql # pdo_pgsql, pgsql
composer --version
node -v
bun --version
```

`mise install` puts the binaries in mise's own store (`~/.local/share/mise/installs`), the way every mise tool is installed. Nothing goes into the global config or onto your `PATH` outside the project. `mise trust` isn't needed because `~/code` is trusted (§1.2).

Notes:

- Without `bin = "composer"` the file is installed as `composer.phar` and `composer` isn't found. If you installed it before adding `bin`, delete `~/.local/share/mise/installs/github-composer-composer` and run `mise install` again.
- If `mise install` fails with "HTTP timed out after 3.00s" for GitHub, run `MISE_FETCH_REMOTE_VERSIONS_TIMEOUT=30s mise install`.
- Bun installs the packages, but keep Node: `vp` is a Node script (`#!/usr/bin/env node`), and the starter kit's post-create hook runs `npm install` before the installer switches to Bun.
- conda-forge only publishes PHP 8.5.x right now (`mise ls-remote conda:php`). Laravel 13 needs PHP ^8.3, so that's fine.
- This PHP has no `gd` and no `redis` extension. If you later switch the cache or queue to Redis (§9.4), use Predis instead of phpredis.
- `composer diagnose` reports `Checking pubkeys: FAIL`. That only affects `composer self-update`. mise updates Composer, so you can ignore it.

Commit `mise.toml` with the app (§5.5). Teammates, every worktree and Laneway's Processes then use the same versions. Laneway never installs Runtimes: it asks mise (`mise env`) in each Worktree, then checks the result against `composer.json`'s `require.php` / `ext-*` and `package.json`'s `engines`. Any mismatch shows up in `lane doctor` and when the Worktree starts.

---

## 4. Create the app in that folder

### 4.1 Install the Laravel installer

The `laravel` CLI is a Composer package. Install it with the project's Composer, so run this inside `~/code/shop`, where `composer` exists:

```sh
cd ~/code/shop
composer global require laravel/installer
```

`composer global` installs into Composer's per-user folder, `~/.config/composer`, and puts the command in `~/.config/composer/vendor/bin`. That folder isn't on your `PATH`, and this manual leaves it that way: it runs the installer by its full path, the one time it's needed.

```sh
~/.config/composer/vendor/bin/laravel --version
```

This installs a Composer package, not a Runtime. `laravel` is a PHP script (`#!/usr/bin/env php`), so it runs on the PHP of whatever folder you're in, and outside a project it fails with "php: not found". Update it later with `composer global update laravel/installer`, again from inside a project.

### 4.2 Create the app

`laravel new` won't create the app in the current folder: `laravel new .` stops with "Application already exists!", and `--force` is refused for `.`. From the parent, `laravel new shop --force` runs `rm -rf shop` first, which deletes your `mise.toml`. So create the app in a hidden subfolder, using the project's Runtimes, and move it up:

```sh
cd ~/code/shop
~/.config/composer/vendor/bin/laravel new .starter-kit --react --bun
(shopt -s dotglob && mv .starter-kit/* .) && rmdir .starter-kit
php artisan --version     # Laravel Framework 13.x
ls bun.lock
```

`--react` picks the React starter kit, and the installer then asks:

- **which of the starter kit's features to keep**
- **Pest or PHPUnit**: either works with everything below
- **whether to install Laravel Boost**: see §9.1 if you say yes

For starter kits the installer runs `composer create-project laravel/react-starter-kit .starter-kit --stability=dev`, so you get the current kit: Laravel 13, Inertia 3, React 19 and Vite+.

`--bun` makes Bun the package manager. The kit's own post-create hook runs `npm install` first, because the kit ships no lockfile. The installer then runs `bun install` and `bun run build`, and `bun.lock` is created. Laneway's Laravel Recipe picks the package manager from the lockfile, so it uses `bun install` in every Worktree from now on. Bun also copies the kit's `pnpm-workspace.yaml` into `package.json` as `"workspaces": ["."]`. That's harmless: leave both files.

It takes about two minutes, and it migrates a temporary SQLite database, which the next step removes. Moving the app afterwards is safe: `vendor/` and `node_modules/.bin` only use relative paths.

Notes:

- **Don't pass `--database=pgsql`.** For a starter kit the installer leaves the database question out and uses SQLite. With `--database=pgsql`, it runs `php artisan migrate` against Postgres before Laneway has created a database, so that step fails, and it writes `DB_DATABASE=.starter_kit` (from the folder name). Switch to Postgres in §5.2 instead.
- The installer sets `APP_URL=http://localhost:8000` in `.env`. That's harmless: the next step deletes `.env`, and Laneway's Env Patch sets `APP_URL`.
- Run it in a terminal. When its output isn't a terminal (piped to `tee`, in CI), the installer adds `--no-ansi` to every command, and `bun install` stops with "bun install has no --no-ansi flag". In that case, run `bun install && bun run build` in `.starter-kit` yourself afterwards.

---

## 5. Get the app ready for Laneway (before linking)

Do all of this **before** `lane link`. Laneway reads these files when it first prepares the Worktree, and some decisions, such as the Test Database, are made then.

### 5.1 Make it a git repository

Laneway works on git repositories: `lane init` (§5.4) and `lane link` both stop with "not inside a git repository" without one. `laravel new` only runs `git init` when you pass `--git`, so do it now:

```sh
git init -b main
```

You commit at the end of this section (§5.5), once the app is ready.

### 5.2 Switch to PostgreSQL

Laneway's Laravel Recipe reads `DB_CONNECTION` from `.env`, or from `.env.example` when there's no `.env`. Point the template at Postgres, then delete the SQLite `.env` so Laneway builds a fresh one:

```sh
sed -i 's/^DB_CONNECTION=sqlite$/DB_CONNECTION=pgsql/' .env.example
rm -f .env database/database.sqlite
```

You don't need to fill in `DB_HOST`, `DB_DATABASE` and the rest: those keys are part of the Env Patch.

### 5.3 Give the tests their own Postgres database (Test Database)

Out of the box the kit's tests use in-memory SQLite (`phpunit.xml` sets `DB_CONNECTION=sqlite` and `DB_DATABASE=:memory:`), and Laneway leaves that alone. To run tests against Postgres, in a database that is wiped independently of your dev data:

```sh
# 1. Stop phpunit.xml from forcing SQLite (it wins over env files, and Laneway never edits it)
sed -i '/<env name="DB_CONNECTION"/d; /<env name="DB_DATABASE"/d' phpunit.xml

# 2. An env file for tests, with its own app key
cp .env.example .env.testing
sed -i 's/^APP_ENV=.*/APP_ENV=testing/' .env.testing
php artisan key:generate --env=testing

# 3. Keep it out of git: Laneway writes this Worktree's database name into it
echo '.env.testing' >> .gitignore
```

The Recipe now detects a Test Database (a `.env.testing` exists, and `phpunit.xml` no longer forces SQLite). Every Worktree gets an empty `<database>_test`, and Laneway patches `.env.testing` to point at it. New worktrees get a copy of the main Worktree's `.env.testing`.

### 5.4 Write `laneway.toml` and fix the Vite Process for Vite+

Write the Recipe's resolved config to a file you can edit and commit:

```sh
lane init
```

The Recipe found a `web` Process (`php artisan serve`, the Primary Process, with `public/` served directly by Laneway), a `queue` worker (`QUEUE_CONNECTION=database`), a `vite` Process (`laravel-vite-plugin` is in `package.json`), Postgres 18 and Mailpit as Services, and its setup steps, which install packages with `bun install` because the app has a `bun.lock`.

**The `vite` command needs a fix.** The current starter kit uses Vite+: `vite` is an alias of `@voidzero-dev/vite-plus-core`, and there is no `node_modules/.bin/vite`, only `vp`. The Recipe's command runs `node_modules/.bin/vite`, so the Process would exit with "not found". `vp dev` takes the same flags, so change the command in `laneway.toml`:

```toml
[processes.vite]
command = "rm -f public/hot; LARAVEL_BYPASS_ENV_CHECK=1 node_modules/.bin/vp dev --config {vite_config} --host 127.0.0.1 --port {port} --strictPort"
http = true
```

`{vite_config}` is a config Laneway generates around your `vite.config.ts`. It moves Vite's live-reload socket to `wss://vite.<domain>` behind Laneway's proxy, so HMR works over HTTPS without changing your project.

Then add migrations to the setup steps (`on_create` in the `[worktree]` table `lane init` wrote), so each new Worktree's database is migrated for its own branch. A cloned database is already migrated, so this is a no-op until the branch adds a migration:

```toml
[worktree]
on_create = ["composer install", "grep -Eq \"^APP_KEY=[\\\"']?[^\\\"'[:space:]#]\" .env || php artisan key:generate", "bun install", "php artisan storage:link", "php artisan migrate --force"]
```

The full file should now look like this:

```toml
[processes.queue]
command = "php artisan queue:work"

[processes.vite]
command = "rm -f public/hot; LARAVEL_BYPASS_ENV_CHECK=1 node_modules/.bin/vp dev --config {vite_config} --host 127.0.0.1 --port {port} --strictPort"
http = true

[processes.web]
command = "PHP_INI_SCAN_DIR=$PHP_INI_SCAN_DIR:{php_ini_dir} PHP_CLI_SERVER_WORKERS=8 php artisan serve --host=127.0.0.1 --port={port} --no-reload"
primary = true
static_dir = "public"

[worktree]
inherit_setup = false
on_create = ["composer install", "grep -Eq \"^APP_KEY=[\\\"']?[^\\\"'[:space:]#]\" .env || php artisan key:generate", "bun install", "php artisan storage:link", "php artisan migrate --force"]

[database]
engine = "postgres"
worktree = "clone"

[env]
APP_URL = "{url}"
DB_CONNECTION = "pgsql"
DB_DATABASE = "{db}"
DB_HOST = "{db_host}"
DB_PASSWORD = "{db_password}"
DB_PORT = "{db_port}"
DB_USERNAME = "{db_username}"
MAIL_HOST = "{mailpit_host}"
MAIL_MAILER = "smtp"
MAIL_PORT = "{mailpit_port}"

[linked_env]
CACHE_PREFIX = "{project}_{worktree}_"
REDIS_PREFIX = "{project}_{worktree}_"

[services]
mailpit = "v1"
postgres = "18"
```

Optional additions:

```toml
# Run the scheduler too (no Process does by default)
[processes.scheduler]
command = "php artisan schedule:work"

# Keep the queue worker running even when nobody is browsing
[processes.queue]
start_mode = "always"      # lazy (default) | always | manual

# Give this Project longer before it goes Idle (default 15m)
idle_timeout = "1h"
```

For settings only you want, use a gitignored `laneway.local.toml`. It overrides `laneway.toml` key by key.

### 5.5 Commit

```sh
git add -A
git commit -m "chore: new Laravel React starter kit on Laneway"
```

---

## 6. Link it and open it

```sh
lane link
```

Laneway shows each env file it will create or change, as a diff of only the keys it owns, and asks before writing. Use `--yes` in scripts. Then it prepares the main Worktree, in this order:

1. **database**: the first connection to port 5432 starts the shared `postgres:18` container (the image is pulled the first time, so this one is slow), and Laneway creates the database `shop`
2. **test database**: an empty `shop_test`
3. **env**: `.env` is created from `.env.example` with the Env Patch applied (`APP_URL=https://shop.localhost`, `DB_HOST=127.0.0.1`, `DB_PORT=5432`, `DB_DATABASE=shop`, `DB_USERNAME=laneway`, `DB_PASSWORD=laneway`, `MAIL_MAILER=smtp`, `MAIL_HOST=127.0.0.1`, `MAIL_PORT=1025`), and `.env.testing` gets the same keys with `DB_DATABASE=shop_test`. Before Laneway first changes an existing file, it saves the original as `<file>.laneway.bak`.
4. **setup steps** (`[worktree] on_create`): `composer install`, `php artisan key:generate` (only if `APP_KEY` is empty), `bun install`, `storage:link`, `migrate --force`

Wait for it, then open the app:

```sh
lane worktree ready     # exits 0 when Ready; 1 if a step failed, with that step's output
lane open               # https://shop.localhost
```

The first request starts `web`, `queue` and `vite` together and waits for `web` to accept connections. Register a user to check that Postgres, sessions and Inertia all work.

---

## 7. Day-to-day

### 7.1 Where things are

| What                    | Where                                                                    |
| ----------------------- | ------------------------------------------------------------------------ |
| The app                 | `https://shop.localhost`                                                 |
| Vite dev server and HMR | `https://vite.shop.localhost` (laravel-vite-plugin finds it via `public/hot`) |
| Caught mail (Mailpit)   | `https://mail.laneway.localhost`                                          |
| Laneway Dashboard        | `https://laneway.localhost`: Projects, Worktrees, Processes, Services, Orphans |
| Postgres                | `127.0.0.1:5432`, user `laneway`, password `laneway`, database `shop`|
| Logs on disk            | `~/.local/state/laneway/logs/<domain>/`                                   |

Mail: the starter kit sends password-reset and email-verification mail. With `MAIL_MAILER=smtp` pointed at Mailpit, all of it lands in the inbox at `mail.laneway.localhost`, and none of it leaves your machine.

Database GUIs (TablePlus, DBeaver, `psql`) connect to `127.0.0.1:5432` as `laneway`/`laneway`. Laneway itself listens on 5432, so the first connection also starts the Postgres container. Each Worktree's database is listed by `lane info`.

### 7.2 Commands

```sh
lane status                 # every Project, its Worktrees and their Processes
lane info                   # this Worktree: URL, Processes, databases, Services
lane info --json            # the same, in a stable format for scripts and agents

lane logs                   # output of this Worktree's Processes
lane logs -f web            # follow one Process: web, queue, vite
lane up | down | restart    # the whole Worktree...
lane restart queue          # ...or one Process (do this after changing a Job class)

lane exec php artisan migrate         # run a command with this Worktree's Runtimes
lane exec php artisan tinker
lane exec bun run build

lane services               # shared Services and their state
lane services up postgres   # start or stop one by hand
```

Every command acts on the Worktree containing the current directory. `--worktree <name|domain|path>` picks another one.

Things to know:

- **Lazy and Idle.** After 15 minutes with no requests and no open connections, the Worktree's lazy Processes stop. The next request starts them again. Services stop too, 15 minutes after their last connection once no running Worktree uses them. Change these globally in `~/.config/laneway/config.toml` (`idle_timeout = "30m"`, `service_idle_timeout = "1h"`) or per Project in `laneway.toml`.
- **Changing `.env` yourself:** `artisan serve` runs with `--no-reload`, so run `lane restart` afterwards. When Laneway changes `.env` itself, it restarts the Processes for you.
- **Edited Blade views and PHP** show on the next request: Laneway's PHP ini turns off OPcache's revalidation delay.
- **Without the `vite` Process** (`lane down vite`), Laravel loads built assets, so run `lane exec bun run build` first, or pages fail with "Vite manifest not found".
- If the `web` Process crashes, the Domain shows Laneway's error page with its last output instead of a bare 502.

### 7.3 Tests

```sh
lane exec php artisan test             # runs against shop_test, never shop
lane exec php artisan test --parallel  # each test worker uses its own copy of shop_test
```

`lane gc` cleans up the copies that parallel runs leave behind.

---

## 8. Worktrees: one environment per branch

This is where Laneway saves the most work. Every git worktree is a complete, isolated copy of the app.

```sh
lane worktree add feature-x               # new branch feature-x
lane worktree add hotfix --branch fix/login   # or check out / create a named branch
```

The worktree goes in `~/code/shop.worktrees/feature-x`, next to the repository rather than inside it. Change the location with `root` in the `[worktree]` table of `laneway.toml`. A plain `git worktree add`, or a worktree an AI tool creates, works exactly the same: Laneway finds worktrees by watching git's own records.

Laneway then prepares the new Worktree:

| Piece          | Main Worktree              | `feature-x`                                                                         |
| -------------- | -------------------------- | ----------------------------------------------------------------------------------- |
| Domain         | `shop.localhost`           | `feature-x.shop.localhost` (Vite at `vite.feature-x.shop.localhost`)                |
| Database       | `shop`                     | `shop__feature_x`, a **copy** of `shop` with its data                               |
| Test Database  | `shop_test`                | `shop__feature_x_test`, empty                                                       |
| `.env`         | yours                      | a copy of the main Worktree's `.env` (same `APP_KEY`), with its own URL and database |
| Cache/Redis keys | Laravel's defaults       | `CACHE_PREFIX` / `REDIS_PREFIX` = `shop_feature-x_`, so queues and caches never cross |
| Setup steps    | run at link                | run again: `composer install`, `bun install`, `migrate --force`                     |
| Gitignored files | yours                    | the paths in `[worktree] copy` in `laneway.toml`, copied from the main Worktree (see §9.2)       |

```sh
cd ~/code/shop.worktrees/feature-x
lane worktree ready && lane open
```

The Domain is named after git's name for the worktree (its directory), not its branch.

**How a Worktree gets its database** (`[database] worktree` in `laneway.toml`):

- `clone` (default): a copy of the main Worktree's database. Experiment freely; `main` is untouched.
- `fresh`: an empty database. Add `php artisan migrate --seed --force` to `[worktree] on_create`.
- `shared`: uses the main Worktree's database.

**Aliases** give a Worktree a nicer name, such as one after its task, which matters most for the randomly named worktrees a **Harness** (an AI coding tool such as Claude Code or T3 Code) creates (`t3-298e5f25.shop.localhost`):

```sh
lane worktree alias checkout   # Added the Alias checkout.shop.localhost …; its URL is now https://checkout.shop.localhost/
lane worktree alias checkout --remove
```

The newest Alias becomes the Worktree's **Primary Domain**, and the Worktree's URL is under it (`https://checkout.shop.localhost/`). That is the URL Laneway gives out everywhere: `url` in `lane info --json`, `lane open`, the Dashboard and the other Processes' hosts (`vite.checkout.shop.localhost`). Agents set up with `lane harness add` (§9) name their linked worktree this way once they know their task.

- **`.env` follows it.** Laneway re-applies the Worktree's `.env` (and `.env.testing`), so `APP_URL` and anything else rendered from `{url}` or `{domain}` use the new name, and restarts the Worktree's running Processes (`php artisan serve`, queue workers, Vite) so they read it. Vite's live reload then connects at `vite.checkout.shop.localhost`. This happens in the background once the Worktree is Ready, so `.env` changes a moment after the command returns, and an Alias added while the Worktree is still being prepared takes effect when preparing finishes. A stopped Worktree only gets its `.env` rewritten and isn't started; its Processes pick up the new URL at their next start. The databases keep their names.
- **The old names keep serving it.** `t3-298e5f25.shop.localhost` and any older Alias still reach the Worktree, but a browser loading a page there (a `GET` navigation) gets a temporary redirect (`307`, not cached) to the same path and query on the Primary Domain, so tabs opened before the Worktree was named move over on their next reload. Fetches, form posts, webhooks and `curl` on the old names are served as before, and `vite.<old name>` is never redirected.
- **You log in again once.** Cookies belong to a host, so the browser has no session on the new name until you log in there.
- **Removing the Alias moves the URL back** to the previous Alias, or to the Worktree's own Domain if it has none: the command says where, `.env` and the running Processes follow the same way, and the redirect goes with it.

The main Worktree follows the same rule: `lane worktree alias demo` there makes `https://demo.shop.localhost/` its URL and redirects page loads from `shop.localhost`.

**Removing a Worktree:**

```sh
git worktree remove ../shop.worktrees/feature-x
```

Its databases become **Orphans**: Laneway keeps them for 24 hours in case the removal was a mistake (a worktree of the same name reclaims them), then drops them. To drop them now:

```sh
lane gc          # lists them and asks; --yes in scripts
```

Commands in `[worktree] on_remove` in `laneway.toml` run in the main Worktree when a Worktree is removed. They get `LANEWAY_WORKTREE`, `LANEWAY_DB`, `LANEWAY_WORKTREE_PATH` and similar variables.

---

## 9. AI agents (Claude Code, T3 Code)

### 9.1 Make the Harness wait for a Ready Worktree

```sh
lane harness add claude   # SessionStart hook in .claude/settings.json + a section in AGENTS.md, imported from CLAUDE.md
lane harness add t3       # a "Laneway: wait until Ready" script in t3.json that runs when T3 Code creates a worktree, + the AGENTS.md section
```

The hook runs `lane worktree ready`, so an agent's session in a new worktree starts only once its database, `.env` and `composer`/`bun install` are done. The `AGENTS.md` section tells the agent to use `lane info --json` for its URL and `lane exec` for commands, and, in a linked worktree without an Alias, to name it after its task once it is Ready (`lane worktree alias checkout-redesign`, see Aliases in §8). Projects set up before that instruction existed get it when `lane harness add` runs again. It's safe for teammates without Laneway: the hook does nothing if `lane` isn't installed.

These are Generators of Plugins built into Laneway, so `lane generate claude` and `lane generate t3` do the same, and `lane generate <plugin>` runs any added Plugin's Generators. Laneway merges what they generate into your files: JSON structurally, Markdown between `laneway:begin`/`laneway:end` markers, never touching the rest, and it writes nothing if one of the files can't be merged.

**If you use Laravel Boost**, Boost writes `CLAUDE.md` and `AGENTS.md` itself, so Laneway leaves them alone and puts its instructions in `.ai/guidelines/laneway.md`, where Boost [takes custom guidelines](https://laravel.com/docs/boost#adding-custom-ai-guidelines). Boost adds them to its files when it runs, so run it after `lane harness add`:

```sh
lane exec php artisan boost:update   # or boost:install, the first time
```

If you add Boost after `lane harness add`, run `lane harness add` again: it moves its section out of `AGENTS.md` and `CLAUDE.md` into `.ai/guidelines/laneway.md`.

### 9.2 Get those files into new worktrees

A new worktree only contains committed files, and **the starter kit's `.gitignore` ignores `/.claude`, `/AGENTS.md` and `/CLAUDE.md`**, which Laravel Boost generates. As it is, the hook and the instructions never reach the worktrees agents work in.

**Commit `.claude/settings.json`**, which holds the hook. Claude Code has to find it as soon as a session starts in a new worktree, before Laneway has done anything there. Keep the rest of `.claude` (`settings.local.json`, Boost's skills) ignored:

```sh
sed -i 's#^/\.claude$#/.claude/*\n!/.claude/settings.json#' .gitignore
```

It has to be `/.claude/*`, not `/.claude`, because git can't un-ignore a file inside an ignored directory.

**Keep `AGENTS.md` and `CLAUDE.md` ignored and let Laneway copy them** from the main Worktree into each new one. Add a `copy` line to the `[worktree]` table `lane init` wrote in `laneway.toml` (a second `[worktree]` header is an error):

```toml
[worktree]
copy = ["AGENTS.md", "CLAUDE.md"]
```

Copying is the first thing Laneway does when it prepares a new Worktree, before its database. A directory is copied with everything in it, a file the new Worktree already has is kept, and a path the main Worktree doesn't have is skipped. With Laravel Boost, also copy what it generates for your agents, such as Claude Code's skills and MCP config:

```toml
[worktree]
copy = ["AGENTS.md", "CLAUDE.md", ".claude/skills", ".mcp.json"]
```

Then commit:

```sh
git add -A && git commit -m "chore: Laneway Harness config for agents"
```

That commits `.claude/settings.json`, `t3.json` and `laneway.toml`, and with Boost, `.ai/guidelines/laneway.md`. A Worktree gets its copies when it's prepared, so after `boost:update` changes the main Worktree's files, Worktrees that already exist keep their old copies.

### 9.3 What an agent sees

```sh
lane info --json   # url, Processes and their states, database, test database, Service hosts/ports/credentials
```

### 9.4 Optional: Redis for cache, queue or sessions

Set `CACHE_STORE=redis` (or `QUEUE_CONNECTION=redis`, `SESSION_DRIVER=redis`) in `.env.example` and `.env`. The Recipe then adds a shared `redis:7` Service and patches `REDIS_HOST`, `REDIS_PORT` and `REDIS_PASSWORD`. The conda PHP has no `redis` extension, so use Predis:

```sh
lane exec composer require predis/predis
# and in .env / .env.example: REDIS_CLIENT=predis
```

If you wrote `laneway.toml` with `lane init` before this change, add `redis = "7"` under `[services]` and the three `REDIS_*` keys under `[env]` (`"{redis_host}"`, `"{redis_port}"`, `"{redis_password}"`), or run `lane init` again and re-apply your edits.

---

## 10. Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `lane worktree ready` exits 1 at `composer install`: "php: not found" | Laneway's daemon can't get PHP from mise. Check `mise settings get trusted_config_paths` covers the worktree, and that `mise` was on your `PATH` when you ran `lane install` (if not, run it again; `systemctl --user cat laneway` shows the `PATH` the daemon has). `lane doctor`, run in the worktree, shows the PHP mise resolves there. Fix it, then `lane worktree ready --retry`. |
| `lane worktree ready` exits 1 at `bun install`: "bun: not found" | The Worktree's `mise.toml` has no `bun` (§3), or it isn't installed yet. Add it, run `mise install`, then `lane worktree ready --retry`. |
| The `vite` Process fails right away: `node_modules/.bin/vite: not found` | The Vite+ starter kit has no `vite` binary. Apply the `vp dev` override from §5.4, then `lane restart vite`. |
| Pages load without CSS or JS, or with a CORS or mixed-content error | Check `lane logs vite`. Check `public/hot` holds `https://vite.<domain>…` and not `http://127.0.0.1…`. A stale file from running `bun run dev` by hand is the usual cause: delete it, then `lane restart vite`. |
| Tests touch `shop` or run on SQLite | `phpunit.xml` still sets `DB_CONNECTION` / `DB_DATABASE` (§5.3). Laneway warns about `DB_DATABASE` in `lane doctor` and in the Worktree's setup warnings. |
| No Test Database even after adding `.env.testing` | The decision is made when the Worktree is prepared. Add `test = "on"` under `[database]` in `laneway.toml`. Existing Worktrees pick it up on `lane unlink` + `lane link` (their databases come straight back from Orphans). New ones get it right away. |
| `lane doctor`: `port 5432` / `443` "in use by …" | Another Postgres (or Herd, or Valet) holds the port. Doctor names the program. Stop it. |
| Browser warns about the certificate | Re-run `lane install` and accept the CA. On WSL2, restart Windows browsers; Firefox may need `security.enterprise_roots.enabled`. On Debian/Ubuntu desktops, Chrome and Firefox use their own NSS store. |
| Generated links, redirects or Boost's browser logger use `http://`; console shows "Redirect is not allowed for a preflight request" | Your Laneway is older than the fix that tells PHP's built-in server its requests came over HTTPS. Update Laneway, run `lane install` again, then `lane restart web`. Don't add `trustProxies` to the app for this. |
| A setup step failed | The Domain shows the setup error page with the failed step's output, as does `lane worktree ready`. Fix the cause, then `lane worktree ready --retry`, which starts again from the first step. |

---

## 11. Undo everything

```sh
cd ~/code/shop
lane unlink        # stops it; its databases become Orphans (re-linking within 24h takes them back)
lane gc --yes      # drop them now
```

Laneway's own state lives in `~/.local/state/laneway`, global config in `~/.config/laneway`, and the Service data in container volumes.
