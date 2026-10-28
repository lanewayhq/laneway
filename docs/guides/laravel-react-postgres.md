# Manual: a new Laravel + Inertia + React + PostgreSQL app on Portby

This manual takes a fresh Linux or WSL2 machine that only has the `portby` binary and ends with a Laravel app (the official React starter kit: Inertia, React, TypeScript, Tailwind) running on PostgreSQL. Along the way it uses everything Portby does for a Laravel Project:

- HTTPS Domains: `https://shop.localhost`, with Vite's dev server and live reload at `https://vite.shop.localhost`
- Processes that start lazily: the web server, the queue worker and Vite start on the first request and stop when the Worktree is Idle
- Shared Services that also start lazily: one Postgres container for every Project, and Mailpit to catch mail
- An Env Patch: Portby owns only `APP_URL`, `DB_*` and `MAIL_*` in `.env`, and leaves every other key alone
- A Test Database, so `php artisan test` runs against Postgres in its own database
- Worktrees: every git worktree gets its own Domain, its own copy of the database, its own `.env` and its own setup run
- Aliases, Orphans and `portby gc`, the Dashboard, `portby exec`, logs, and Harness config for AI agents

Runtimes come from **mise**, per project and from prebuilt packages only: nothing is compiled, and no PHP, Composer or Node is installed globally.

Commands below use `shop` as the Project name and `~/code` as the place you keep projects. Change both to suit you.

---

## 1. Install the machine prerequisites

You need git, a container engine for the Services, and mise for the Runtimes.

### 1.1 Git and Podman

Portby runs Postgres and Mailpit as containers. It prefers rootless Podman; Docker works too.

```sh
# Debian / Ubuntu
sudo apt update && sudo apt install -y git podman unzip

# Arch
sudo pacman -S --needed git podman unzip

# Fedora
sudo dnf install -y git podman unzip
```

`unzip` is optional. Composer works without it, just more slowly.

**WSL2 only:** Portby runs as a systemd user service. Make sure `/etc/wsl.conf` contains:

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

**Why this matters:** each git worktree is a new directory with its own copy of `mise.toml`, and mise refuses config files it hasn't been told to trust. When mise refuses, Portby falls back to `PATH`, finds no PHP, and the new Worktree's setup fails at `composer install`. Trusting `~/code` (where Portby also puts worktrees, see §8) avoids running `mise trust` in every worktree.

---

## 2. Set up Portby

`portby install` writes the systemd unit with the `PATH` of the shell you run it from. The daemon only needs `mise` on that `PATH`: it asks mise for each Worktree's Runtimes itself. So run this after §1.2:

```sh
portby install
```

It asks for sudo at most twice: once to let Portby bind ports 80 and 443, and once to add its local CA to the system trust store. It also enables `podman.socket`. On WSL2, Windows shows a "Security Warning… Portby Local CA" dialog: click **Yes**, so Windows browsers trust `*.localhost` certificates.

```sh
portby doctor
```

Daemon, service, ports 443 and 80, port permission, the CA in each store, the container engine and mise should all be `ok`. No PHP or Node is reported yet: there's no project, and nothing is installed globally.

> Ran `portby install` before installing mise? Run it again. It rewrites the unit with your current `PATH` and restarts the daemon. Nothing else changes.

---

## 3. Create the project folder with its own Runtimes

Nothing is installed globally. The project's `mise.toml` declares PHP, Composer and Node, and they're only on your `PATH` inside the project, and in Portby's Processes and setup steps for it.

mise's default `php` plugin (`vfox:jdx/vfox-php`) **builds PHP from source**: it needs a compiler toolchain and about 15 `-dev` packages, takes a long time, and you only get `pdo_pgsql` if `libpq` headers happen to be installed. Don't use it. Use these prebuilt ones:

| Tool     | mise backend               | What you get                                                                                                          |
| -------- | -------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| PHP      | `conda:php`                | Prebuilt PHP from conda-forge. Installs in seconds, with `pdo_pgsql`, `pgsql`, `intl`, `pcntl`, OPcache, `zip`, `sodium`, `bcmath` and `pdo_sqlite`. |
| Composer | `github:composer/composer` | The official `composer.phar` from Composer's GitHub releases, installed as `composer`                                 |
| Node     | `node` (mise core)         | Official Node binaries                                                                                                |

```sh
mkdir -p ~/code/shop && cd ~/code/shop

cat > mise.toml <<'TOML'
[tools]
"conda:php" = "8.5"
"github:composer/composer" = { version = "2", bin = "composer" }
node = "24"
TOML

mise install
php -v                 # PHP 8.5.x (only inside ~/code/shop)
php -m | grep -i pgsql # pdo_pgsql, pgsql
composer --version
node -v
```

`mise install` puts the binaries in mise's own store (`~/.local/share/mise/installs`), the way every mise tool is installed. Nothing goes into the global config or onto your `PATH` outside the project. `mise trust` isn't needed because `~/code` is trusted (§1.2).

Notes:

- Without `bin = "composer"` the file is installed as `composer.phar` and `composer` isn't found. If you installed it before adding `bin`, delete `~/.local/share/mise/installs/github-composer-composer` and run `mise install` again.
- If `mise install` fails with "HTTP timed out after 3.00s" for GitHub, run `MISE_FETCH_REMOTE_VERSIONS_TIMEOUT=30s mise install`.
- conda-forge only publishes PHP 8.5.x right now (`mise ls-remote conda:php`). Laravel 13 needs PHP ^8.3, so that's fine.
- This PHP has no `gd` and no `redis` extension. If you later switch the cache or queue to Redis (§9.4), use Predis instead of phpredis.
- `composer diagnose` reports `Checking pubkeys: FAIL`. That only affects `composer self-update`. mise updates Composer, so you can ignore it.

Commit `mise.toml` with the app (§5.4). Teammates, every worktree and Portby's Processes then use the same versions. Portby never installs Runtimes: it asks mise (`mise env`) in each Worktree, then checks the result against `composer.json`'s `require.php` / `ext-*` and `package.json`'s `engines`. Any mismatch shows up in `portby doctor` and when the Worktree starts.

---

## 4. Create the app in that folder

`composer create-project` refuses a directory that isn't empty ("Project directory … is not empty"), and `shop/` already has `mise.toml`. Composer has no flag to force it. The Laravel installer's `laravel new . --force` refuses the current directory too, and `laravel new shop --force` deletes `shop/` first. So create the app in a hidden subfolder, using the project's Runtimes, and move it up:

```sh
cd ~/code/shop
composer create-project laravel/react-starter-kit .starter-kit --stability=dev
(shopt -s dotglob && mv .starter-kit/* .) && rmdir .starter-kit
php artisan --version     # Laravel Framework 13.x
```

This is the command `laravel new shop --react` runs, so you don't need the Laravel installer. `--stability=dev` is required. Without it you get the starter kit's last *tagged* release, which is still on Laravel 12 and Inertia 2. With it you get the current kit: Laravel 13, Inertia 3, React 19 and Vite+.

It takes about two minutes. It runs `npm install` and migrates a temporary SQLite database, which the next step removes. Moving the app afterwards is safe: `vendor/` and `node_modules/.bin` only use relative paths.

---

## 5. Get the app ready for Portby (before linking)

Do all of this **before** `portby link`. Portby reads these files when it first prepares the Worktree, and some decisions, such as the Test Database, are made then.

### 5.1 Switch to PostgreSQL

Portby's Laravel Recipe reads `DB_CONNECTION` from `.env`, or from `.env.example` when there's no `.env`. Point the template at Postgres, then delete the SQLite `.env` so Portby builds a fresh one:

```sh
sed -i 's/^DB_CONNECTION=sqlite$/DB_CONNECTION=pgsql/' .env.example
rm -f .env database/database.sqlite
```

You don't need to fill in `DB_HOST`, `DB_DATABASE` and the rest: those keys are part of the Env Patch.

### 5.2 Give the tests their own Postgres database (Test Database)

Out of the box the kit's tests use in-memory SQLite (`phpunit.xml` sets `DB_CONNECTION=sqlite` and `DB_DATABASE=:memory:`), and Portby leaves that alone. To run tests against Postgres, in a database that is wiped independently of your dev data:

```sh
# 1. Stop phpunit.xml from forcing SQLite (it wins over env files, and Portby never edits it)
sed -i '/<env name="DB_CONNECTION"/d; /<env name="DB_DATABASE"/d' phpunit.xml

# 2. An env file for tests, with its own app key
cp .env.example .env.testing
sed -i 's/^APP_ENV=.*/APP_ENV=testing/' .env.testing
php artisan key:generate --env=testing

# 3. Keep it out of git: Portby writes this Worktree's database name into it
echo '.env.testing' >> .gitignore
```

The Recipe now detects a Test Database (a `.env.testing` exists, and `phpunit.xml` no longer forces SQLite). Every Worktree gets an empty `<database>_test`, and Portby patches `.env.testing` to point at it. New worktrees get a copy of the main Worktree's `.env.testing`.

### 5.3 Write `portby.toml` and fix the Vite Process for Vite+

Write the Recipe's resolved config to a file you can edit and commit:

```sh
portby init
```

The Recipe found a `web` Process (`php artisan serve`, the Primary Process, with `public/` served directly by Portby), a `queue` worker (`QUEUE_CONNECTION=database`), a `vite` Process (`laravel-vite-plugin` is in `package.json`), Postgres 18 and Mailpit as Services, and its setup steps.

**The `vite` command needs a fix.** The current starter kit uses Vite+: `vite` is an alias of `@voidzero-dev/vite-plus-core`, and there is no `node_modules/.bin/vite`, only `vp`. The Recipe's command runs `node_modules/.bin/vite`, so the Process would exit with "not found". `vp dev` takes the same flags, so change the command in `portby.toml`:

```toml
[processes.vite]
command = "rm -f public/hot; LARAVEL_BYPASS_ENV_CHECK=1 node_modules/.bin/vp dev --config {vite_config} --host 127.0.0.1 --port {port} --strictPort"
http = true
```

`{vite_config}` is a config Portby generates around your `vite.config.ts`. It moves Vite's live-reload socket to `wss://vite.<domain>` behind Portby's proxy, so HMR works over HTTPS without changing your project.

Then add migrations to the setup steps, so each new Worktree's database is migrated for its own branch. A cloned database is already migrated, so this is a no-op until the branch adds a migration:

```toml
on_create = ["composer install", "grep -Eq \"^APP_KEY=[\\\"']?[^\\\"'[:space:]#]\" .env || php artisan key:generate", "npm install", "php artisan storage:link", "php artisan migrate --force"]
```

The full file should now look like this:

```toml
inherit_setup = false
on_create = ["composer install", "grep -Eq \"^APP_KEY=[\\\"']?[^\\\"'[:space:]#]\" .env || php artisan key:generate", "npm install", "php artisan storage:link", "php artisan migrate --force"]

[processes.queue]
command = "php artisan queue:work"

[processes.vite]
command = "rm -f public/hot; LARAVEL_BYPASS_ENV_CHECK=1 node_modules/.bin/vp dev --config {vite_config} --host 127.0.0.1 --port {port} --strictPort"
http = true

[processes.web]
command = "PHP_INI_SCAN_DIR=$PHP_INI_SCAN_DIR:{php_ini_dir} PHP_CLI_SERVER_WORKERS=8 php artisan serve --host=127.0.0.1 --port={port} --no-reload"
primary = true
static_dir = "public"

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

For settings only you want, use a gitignored `portby.local.toml`. It overrides `portby.toml` key by key.

### 5.4 Make it a git repository

Portby Links git repositories. `create-project` doesn't run `git init`, so do it now:

```sh
git init -b main
git add -A
git commit -m "chore: new Laravel React starter kit on Portby"
```

---

## 6. Link it and open it

```sh
portby link
```

Portby shows each env file it will create or change, as a diff of only the keys it owns, and asks before writing. Use `--yes` in scripts. Then it prepares the main Worktree, in this order:

1. **database**: the first connection to port 5432 starts the shared `postgres:18` container (the image is pulled the first time, so this one is slow), and Portby creates the database `shop`
2. **test database**: an empty `shop_test`
3. **env**: `.env` is created from `.env.example` with the Env Patch applied (`APP_URL=https://shop.localhost`, `DB_HOST=127.0.0.1`, `DB_PORT=5432`, `DB_DATABASE=shop`, `DB_USERNAME=portby`, `DB_PASSWORD=portby`, `MAIL_MAILER=smtp`, `MAIL_HOST=127.0.0.1`, `MAIL_PORT=1025`), and `.env.testing` gets the same keys with `DB_DATABASE=shop_test`. Before Portby first changes an existing file, it saves the original as `<file>.portby.bak`.
4. **setup steps** (`on_create`): `composer install`, `php artisan key:generate` (only if `APP_KEY` is empty), `npm install`, `storage:link`, `migrate --force`

Wait for it, then open the app:

```sh
portby worktree ready     # exits 0 when Ready; 1 if a step failed, with that step's output
portby open               # https://shop.localhost
```

The first request starts `web`, `queue` and `vite` together and waits for `web` to accept connections. Register a user to check that Postgres, sessions and Inertia all work.

---

## 7. Day-to-day

### 7.1 Where things are

| What                    | Where                                                                    |
| ----------------------- | ------------------------------------------------------------------------ |
| The app                 | `https://shop.localhost`                                                 |
| Vite dev server and HMR | `https://vite.shop.localhost` (laravel-vite-plugin finds it via `public/hot`) |
| Caught mail (Mailpit)   | `https://mail.portby.localhost`                                          |
| Portby Dashboard        | `https://portby.localhost`: Projects, Worktrees, Processes, Services, Orphans |
| Postgres                | `127.0.0.1:5432`, user `portby`, password `portby`, database `shop`      |
| Logs on disk            | `~/.local/state/portby/logs/<domain>/`                                   |

Mail: the starter kit sends password-reset and email-verification mail. With `MAIL_MAILER=smtp` pointed at Mailpit, all of it lands in the inbox at `mail.portby.localhost`, and none of it leaves your machine.

Database GUIs (TablePlus, DBeaver, `psql`) connect to `127.0.0.1:5432` as `portby`/`portby`. Portby itself listens on 5432, so the first connection also starts the Postgres container. Each Worktree's database is listed by `portby info`.

### 7.2 Commands

```sh
portby status                 # every Project, its Worktrees and their Processes
portby info                   # this Worktree: URL, Processes, databases, Services
portby info --json            # the same, in a stable format for scripts and agents

portby logs                   # output of this Worktree's Processes
portby logs -f web            # follow one Process: web, queue, vite
portby up | down | restart    # the whole Worktree...
portby restart queue          # ...or one Process (do this after changing a Job class)

portby exec php artisan migrate         # run a command with this Worktree's Runtimes
portby exec php artisan tinker
portby exec npm run build

portby services               # shared Services and their state
portby services up postgres   # start or stop one by hand
```

Every command acts on the Worktree containing the current directory. `--worktree <name|domain|path>` picks another one.

Things to know:

- **Lazy and Idle.** After 15 minutes with no requests and no open connections, the Worktree's lazy Processes stop. The next request starts them again. Services stop too, 15 minutes after their last connection once no running Worktree uses them. Change these globally in `~/.config/portby/config.toml` (`idle_timeout = "30m"`, `service_idle_timeout = "1h"`) or per Project in `portby.toml`.
- **Changing `.env` yourself:** `artisan serve` runs with `--no-reload`, so run `portby restart` afterwards. When Portby changes `.env` itself, it restarts the Processes for you.
- **Edited Blade views and PHP** show on the next request: Portby's PHP ini turns off OPcache's revalidation delay.
- **Without the `vite` Process** (`portby down vite`), Laravel loads built assets, so run `portby exec npm run build` first, or pages fail with "Vite manifest not found".
- If the `web` Process crashes, the Domain shows Portby's error page with its last output instead of a bare 502.

### 7.3 Tests

```sh
portby exec php artisan test             # runs against shop_test, never shop
portby exec php artisan test --parallel  # each test worker uses its own copy of shop_test
```

`portby gc` cleans up the copies that parallel runs leave behind.

---

## 8. Worktrees: one environment per branch

This is where Portby saves the most work. Every git worktree is a complete, isolated copy of the app.

```sh
portby worktree add feature-x               # new branch feature-x
portby worktree add hotfix --branch fix/login   # or check out / create a named branch
```

The worktree goes in `~/code/shop.worktrees/feature-x`, next to the repository rather than inside it. Change the location with `worktree_root` in `portby.toml`. A plain `git worktree add`, or a worktree an AI tool creates, works exactly the same: Portby finds worktrees by watching git's own records.

Portby then prepares the new Worktree:

| Piece          | Main Worktree              | `feature-x`                                                                         |
| -------------- | -------------------------- | ----------------------------------------------------------------------------------- |
| Domain         | `shop.localhost`           | `feature-x.shop.localhost` (Vite at `vite.feature-x.shop.localhost`)                |
| Database       | `shop`                     | `shop__feature_x`, a **copy** of `shop` with its data                               |
| Test Database  | `shop_test`                | `shop__feature_x_test`, empty                                                       |
| `.env`         | yours                      | a copy of the main Worktree's `.env` (same `APP_KEY`), with its own URL and database |
| Cache/Redis keys | Laravel's defaults       | `CACHE_PREFIX` / `REDIS_PREFIX` = `shop_feature-x_`, so queues and caches never cross |
| Setup steps    | run at link                | run again: `composer install`, `npm install`, `migrate --force`                     |

```sh
cd ~/code/shop.worktrees/feature-x
portby worktree ready && portby open
```

The Domain is named after git's name for the worktree (its directory), not its branch.

**How a Worktree gets its database** (`[database] worktree` in `portby.toml`):

- `clone` (default): a copy of the main Worktree's database. Experiment freely; `main` is untouched.
- `fresh`: an empty database. Add `php artisan migrate --seed --force` to `on_create`.
- `shared`: uses the main Worktree's database.

**Aliases** give a Worktree an extra, nicer Domain, such as for a demo:

```sh
portby worktree alias demo           # https://demo.shop.localhost -> this Worktree
portby worktree alias demo --remove
```

**Removing a Worktree:**

```sh
git worktree remove ../shop.worktrees/feature-x
```

Its databases become **Orphans**: Portby keeps them for 24 hours in case the removal was a mistake (a worktree of the same name reclaims them), then drops them. To drop them now:

```sh
portby gc          # lists them and asks; --yes in scripts
```

Commands in `on_remove` in `portby.toml` run in the main Worktree when a Worktree is removed. They get `PORTBY_WORKTREE`, `PORTBY_DB`, `PORTBY_WORKTREE_PATH` and similar variables.

---

## 9. AI agents (Claude Code, T3 Code)

### 9.1 Make the Harness wait for a Ready Worktree

```sh
portby harness add claude   # SessionStart hook in .claude/settings.json + agent instructions
portby harness add t3       # a "Portby: wait until Ready" script in t3.json that runs when T3 Code creates a worktree, + agent instructions
php artisan boost:update    # puts the agent instructions into Boost's CLAUDE.md and AGENTS.md
```

The hook runs `portby worktree ready`, so an agent's session in a new worktree starts only once its database, `.env` and `composer`/`npm install` are done. The agent instructions tell the agent to use `portby info --json` for its URL and `portby exec` for commands. It's safe for teammates without Portby: the hook does nothing if `portby` isn't installed.

The starter kit comes with Laravel Boost, which writes `CLAUDE.md` and `AGENTS.md` itself. So Portby puts its instructions where Boost [takes custom guidelines](https://laravel.com/docs/boost#adding-custom-ai-guidelines), `.ai/guidelines/portby.md`, and doesn't touch those files. Boost adds the instructions to them when you run `boost:install` or `boost:update`. In a Project without Boost, Portby adds a section to `AGENTS.md` and imports it from `CLAUDE.md`.

### 9.2 Commit those files

**The starter kit's `.gitignore` ignores `/.claude`, `/AGENTS.md` and `/CLAUDE.md`**, because Laravel Boost generates them. A new worktree only contains committed files, so as long as they're ignored the hook and instructions never reach the worktrees agents work in. Remove those three lines from `.gitignore`, keep only personal files ignored, and commit:

```sh
sed -i '\#^/\.claude$#d; \#^/AGENTS\.md$#d; \#^/CLAUDE\.md$#d' .gitignore
echo '.claude/settings.local.json' >> .gitignore
git add -A && git commit -m "chore: Portby Harness config for agents"
```

### 9.3 What an agent sees

```sh
portby info --json   # url, Processes and their states, database, test database, Service hosts/ports/credentials
```

### 9.4 Optional: Redis for cache, queue or sessions

Set `CACHE_STORE=redis` (or `QUEUE_CONNECTION=redis`, `SESSION_DRIVER=redis`) in `.env.example` and `.env`. The Recipe then adds a shared `redis:7` Service and patches `REDIS_HOST`, `REDIS_PORT` and `REDIS_PASSWORD`. The conda PHP has no `redis` extension, so use Predis:

```sh
portby exec composer require predis/predis
# and in .env / .env.example: REDIS_CLIENT=predis
```

If you wrote `portby.toml` with `portby init` before this change, add `redis = "7"` under `[services]` and the three `REDIS_*` keys under `[env]` (`"{redis_host}"`, `"{redis_port}"`, `"{redis_password}"`), or run `portby init` again and re-apply your edits.

---

## 10. Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `portby worktree ready` exits 1 at `composer install`: "php: not found" | Portby's daemon can't get PHP from mise. Check `mise settings get trusted_config_paths` covers the worktree, and that `mise` was on your `PATH` when you ran `portby install` (if not, run it again; `systemctl --user cat portby` shows the `PATH` the daemon has). `portby doctor`, run in the worktree, shows the PHP mise resolves there. Fix it, then `portby worktree ready --retry`. |
| The `vite` Process fails right away: `node_modules/.bin/vite: not found` | The Vite+ starter kit has no `vite` binary. Apply the `vp dev` override from §5.3, then `portby restart vite`. |
| Pages load without CSS or JS, or with a CORS or mixed-content error | Check `portby logs vite`. Check `public/hot` holds `https://vite.<domain>…` and not `http://127.0.0.1…`. A stale file from running `npm run dev` by hand is the usual cause: delete it, then `portby restart vite`. |
| Tests touch `shop` or run on SQLite | `phpunit.xml` still sets `DB_CONNECTION` / `DB_DATABASE` (§5.2). Portby warns about `DB_DATABASE` in `portby doctor` and in the Worktree's setup warnings. |
| No Test Database even after adding `.env.testing` | The decision is made when the Worktree is prepared. Add `test = "on"` under `[database]` in `portby.toml`. Existing Worktrees pick it up on `portby unlink` + `portby link` (their databases come straight back from Orphans). New ones get it right away. |
| `portby doctor`: `port 5432` / `443` "in use by …" | Another Postgres (or Herd, or Valet) holds the port. Doctor names the program. Stop it. |
| Browser warns about the certificate | Re-run `portby install` and accept the CA. On WSL2, restart Windows browsers; Firefox may need `security.enterprise_roots.enabled`. On Debian/Ubuntu desktops, Chrome and Firefox use their own NSS store. |
| Generated links, redirects or Boost's browser logger use `http://`; console shows "Redirect is not allowed for a preflight request" | Your Portby is older than the fix that tells PHP's built-in server its requests came over HTTPS. Update Portby, run `portby install` again, then `portby restart web`. Don't add `trustProxies` to the app for this. |
| A setup step failed | The Domain shows the setup error page with the failed step's output, as does `portby worktree ready`. Fix the cause, then `portby worktree ready --retry`, which starts again from the first step. |

---

## 11. Undo everything

```sh
cd ~/code/shop
portby unlink        # stops it; its databases become Orphans (re-linking within 24h takes them back)
portby gc --yes      # drop them now
```

Portby's own state lives in `~/.local/state/portby`, global config in `~/.config/portby`, and the Service data in container volumes.
