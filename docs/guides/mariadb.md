# MariaDB

Laneway runs MariaDB as its own Engine, from the `mariadb` Plugin in the Catalog. Like MySQL, each Worktree gets its own database, by default a copy of the main Worktree's. Linking a Project that needs MariaDB fetches the Plugin, so there's nothing to install yourself.

## Laravel's `mariadb` connection

A Laravel app whose `.env` (or `.env.example`) says `DB_CONNECTION=mariadb` gets a MariaDB database when you Link it, with no `laneway.toml` needed. It runs MariaDB's current LTS, 11.8, unless you pin a version (see below).

## An app on the `mysql` connection, with MariaDB in production

Many apps say `DB_CONNECTION=mysql` but run MariaDB in production, and Laravel can't tell the two apart from `.env`. To develop against the MariaDB your production runs, add two settings to the Project's `laneway.toml`:

```toml
[database]
engine = "mariadb"

[services]
mariadb = "10.6"
```

Commit it, then Link the Project (or Link it again). Laneway puts the database on MariaDB 10.6 and fills in `DB_HOST`, `DB_PORT`, `DB_DATABASE`, `DB_USERNAME` and `DB_PASSWORD`. It leaves `DB_CONNECTION=mysql` as it is, since Laravel's mysql driver talks to MariaDB.

The version is MariaDB's image tag, so any tag from 10.6 on works: `10.6`, `10.11`, `11.4`, `11.8`, `12.3` and so on. Pin the one production runs.

## Patch releases

A tag such as `10.6` names a release series, not one release. When a Service of it first starts on a machine that doesn't have the image yet, Laneway pulls the tag, which gets that series' newest patch release. After that it keeps using that image and doesn't pull the tag again by itself.

To move a Service to a newer patch release:

```sh
lane services down mariadb:10.6
podman pull docker.io/library/mariadb:10.6      # or docker pull
podman rm laneway-mariadb-10.6                  # the container only: its volume, with your data, stays
```

The next time something needs the Service, Laneway creates its container again from the new image, on the same volume. MariaDB upgrades the data on that start if the release needs it (the Engine sets `MARIADB_AUTO_UPGRADE`), so you don't have to run `mariadb-upgrade` yourself.

## Changing the pin

A Service's port and data belong to its version string. Changing the pin, such as from `10.6` to `11.4`, or even to `10.6.27`, starts a new Service with its own port and empty databases. The new Service doesn't get the old one's data: copy across what you need yourself, with `mariadb-dump` and `mariadb`, then remove the old Service's container and volume once nothing uses them.

## Ports

MySQL and MariaDB both use port 3306. Whichever default version (MySQL 8.4 or MariaDB 11.8) Laneway registers first gets 3306, and the other gets a port in the 33000s, which it keeps from then on. A pinned version always gets a port in the 33000s. The Env Patch fills `DB_PORT` with the Service's port whichever one it is, and `lane services` shows each Service's port for GUI clients such as TablePlus.
