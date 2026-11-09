# Object storage gives each Worktree its own Buckets, from a RustFS Engine

Laneway offers S3-compatible object storage through a first-party `rustfs` Plugin, whose Engine runs RustFS. MinIO, the usual choice, no longer publishes community images and its repository is archived; RustFS is Apache-2.0 and stable since 1.0. A Project opts in by declaring `rustfs` under `[services]`; nothing detects it, because Laneway would then own `AWS_*` keys a Project may point at real AWS.

Every Worktree gets its own **Bucket**, as it gets its own database, so Worktrees (and the agents in them) never see or delete each other's files. A Bucket is not a database: an Engine declares its Bucket commands (`exists`, `create`, `clone`, `configure`, `drop`, `list` and `size`) in a `[bucket]` table beside `[database]`, with its own naming rule, and a Worktree has a Bucket in each object storage Service its Project uses, beside its one database. A new Worktree's Bucket starts as a copy of the main Worktree's, because its cloned database points at the main Worktree's files; above a size limit it starts empty, with a warning naming the setting. A removed Worktree's Buckets become Orphans like its databases.

Each Worktree has one private Bucket. A Project whose production has a public bucket too declares more under `[buckets]`, each private or public (anonymous reads by bucket policy), and wires them into its env with placeholders. Public versus private is decided per Bucket, never per object, because new AWS buckets disable ACLs: a local environment that honoured `visibility: public` would accept what production refuses. Each Bucket allows browser requests (CORS) from its Worktree's Domain and Aliases, and follows them as Aliases change.

RustFS's S3 API and console are served at Service Domains (`s3.` and `rustfs.` under the Dashboard's Domain), so the app's endpoint, public URLs and presigned URLs are one HTTPS origin that both PHP and the browser reach. This generalises the mail Domain, which Mailpit's inbox had alone.

## Considered Options

- **One bucket per Project, shared by its Worktrees.** Rejected: the first Service where Worktrees step on each other's data, which is what Laneway exists to stop.
- **One bucket per Project with a key prefix per Worktree.** Rejected: Laravel's s3 disk takes no prefix from env, so every Project would edit its filesystem config.
- **Buckets as databases** (one per Engine with `[database]`). Rejected: bucket names follow different rules, Buckets have no Test Database, and a Worktree's database would stop being one thing.
- **An S3 client in Laneway's core.** Rejected: ADR-0007 took Postgres's and MySQL's clients out of core; Engines stay data.
- **A Laneway-owned RustFS image with a bucket CLI added.** Rejected: an image pipeline to own for a gap that closes when RustFS ships its CLI (`rc`) in its own image.
- **A manifest feature that mounts tools into a Service's container.** Rejected for now: new core surface and per-architecture binaries for one Engine and a temporary gap. It is the answer if a second Engine needs it.
- **Starting every Bucket empty.** Rejected: a Worktree whose first page shows broken images isn't usefully Ready.
- **A fixed public and private Bucket for every Worktree.** Rejected: most Projects follow AWS's own advice (one private bucket, CloudFront or presigned URLs) and would carry a public Bucket they never use.

## Consequences

- RustFS 1.0.4's image has no bucket CLI, so until one ships in it the `clone` and `size` commands fetch a pinned `rc` release, checked against its SHA-256 in the manifest (so the signed Catalog covers it), into the container on first use. The other Bucket commands are single S3 requests signed by the image's own `curl`: `rc` can't tell a missing bucket from a network error, and removes a bucket object by object. So only a new Worktree's first copy needs the network, as the Service's first start does to pull its image. A Plugin update drops this once RustFS bundles `rc`.
- RustFS tags only full versions (`1.0.4`), so an Engine manifest can map a Service version (`"1"`) to an image reference. The Service, its volume and its port are keyed by `"1"`; patch upgrades arrive with `lane plugin update` and keep their data.
- Cloning copies each object on the server (`rc cp --recursive`, S3 CopyObject, which keeps its metadata, unlike `rc mirror`), so a big main Worktree Bucket costs disk in each new Worktree, bounded by the size limit. Its time follows the object count more than the bytes (a thousand objects in seconds, tens of thousands of thumbnails in minutes), so the limit, which stays in bytes, bounds disk better than time; its documentation says so.
- PHP reaches the S3 Service Domain over HTTPS, so it relies on Laneway's CA being in the system trust store, as browsers already do. A request signature covers the host, so the Service Domain proxy must pass the client's `Host` through unchanged.
- RustFS accepts per-object ACLs and ignores them rather than refusing them as AWS does: `visibility: public` succeeds locally and the object stays private, while AWS refuses the upload. Refusing it would take Laneway's proxy inspecting S3 requests for one Engine, so the gap is documented instead: local storage never makes public what production wouldn't, but it doesn't refuse what production refuses.
- Service Domains are declared by Engine manifests; the mail Domain becomes one of them.
