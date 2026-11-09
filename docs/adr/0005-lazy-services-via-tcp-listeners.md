# Services start lazily behind Laneway's own TCP listeners

Laneway itself listens on each Service's port (5432, 3306, 6379, …). On the first connection it starts the Service container and forwards the traffic. The Service stops once it has no connections and no running dependent Worktrees. This applies the same idle-cost rule to Services as to Processes. It also catches clients that never go through a Worktree, such as TablePlus or `psql`.

The cost is an extra TCP hop on every database connection, and a reader may be surprised that Laneway, not Postgres, owns port 5432. The alternative, starting Services only alongside Worktrees, would leave direct database clients failing with "connection refused".

Each engine's default version gets the standard port. Other versions get stable ports from a fixed rule (e.g. `postgres:14` → 54314); an Engine whose block of 100 would pass 65535 gets a block at 50000 + 100 × the first two digits of its port instead (e.g. `rustfs:2`, port 9000 → 59002). Services are reached through the Docker Engine API, which covers both Docker and Podman; rootless Podman is preferred when both are installed.
