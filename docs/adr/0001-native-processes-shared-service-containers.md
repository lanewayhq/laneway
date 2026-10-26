# Processes run natively; Services run as shared containers

Processes run directly on the host, with no container per Project or Worktree. Services such as Postgres, MySQL and Redis each run as one shared container per engine and major version (e.g. a single `postgres-16` serves every Project that needs it). Each Project and Worktree gets its own database inside that shared container.

We picked this to avoid DDEV's per-project container cost. Apps run natively because running them in containers is what makes DDEV heavy. Services run in containers because installing and running several database versions side by side on the host is fragile on every OS.

## Consequences

- Portby does not give a team reproducible system libraries. A Project that depends on host libraries (libvips, ImageMagick, etc.) relies on the developer's machine having them.
- A container engine (Podman or Docker) is required as soon as a Project uses a Service.
