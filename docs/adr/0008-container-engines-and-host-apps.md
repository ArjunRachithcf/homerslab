---
status: accepted
date: 2026-11-09
---

# Docker and Podman, rootful and rootless; host apps only as vendor packages

Apps run on one of three engines:
- Docker;
- rootful Podman, through its Docker-compatible API;
- rootless Podman, where each app is its own unprivileged user.

An app may instead be a host app, but only from its vendor's signed package repository. The agent:
- installs it;
- binds it to localhost and the mesh;
- puts the edge in front of it;
- treats it like any other app for backups, health and updates.

## Considered options

- **Docker only, or Podman rootful only.** Both recommended during the grilling, but not chosen.
- **Host apps from any binary with a service unit.** The agent would install arbitrary code as root.
- **Flatpak or Snap.** Few server apps ship that way.

## Consequences

- Rootless Podman is a second code path: networking, ports below 1024 and file ownership all differ.
- The engine test matrix triples.
