---
status: accepted
date: 2026-11-09
---

# Build only the glue, on maintained parts

HomersLab is a new codebase. It writes only what was missing on 2026-11-09:
- the catalog;
- the per-app wiring;
- the lifecycle;
- the UI.

Everything else is maintained software:
- Authentik and Kanidm for sign-in;
- Caddy for the edge;
- NetBird and Tailscale for the mesh;
- the standard `docker compose` command and Podman's Quadlet for running apps, with Docker's Compose SDK only for
  reading and planning them ([ADR 0014](0014-app-units-run-standard-tools.md));
- restic and age for backups;
- libdns for DNS providers.

No open-source project combined per-app sign-in, a private mesh with no router ports, encrypted backups with
restore tests, and containers on the household's own distro ([landscape](../research/2026-11-09-landscape.md)). So we
build, but we don't reinvent a sign-in, a proxy, a mesh or a backup tool.

## Considered options

- **Adopt Olares.** The closest: per-app OIDC, about 400 apps, polished. But it runs on Kubernetes, supports Ubuntu
  and Debian with NVIDIA only, its multi-node support is alpha, and custom domains and activation go through Olares
  Space.
- **Extend Runtipi.** Polished, with about 268 apps, but single-node, with no per-app sign-in. The changes we need are
  too large to land upstream.
- **Evolve classic in place.**
  - Its per-app wiring is spread over a dozen hand-edited files.
  - It has no install, remove, update or restore.
  - We reuse its ideas: the security model, secrets that only travel inward, golden tests, wizards for steps only a
    person can do.

## Amended (2026-11-11)

Proposed in #17, for the maintainer's acceptance: the line on running apps follows the decision of 2026-11-10 (ADR
0014). Apps run as systemd units that call the standard compose command or Quadlet; the Compose SDK only reads and
plans. HomersLab's binary is never in an app's start path.
