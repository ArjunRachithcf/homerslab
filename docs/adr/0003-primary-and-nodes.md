---
status: accepted
date: 2026-11-09
---

# A primary plus optional nodes, each app placed on one node

One machine is the primary:
- it runs the manager, the household's sign-in, the database and its own agent;
- on its own it is a complete household.

Other machines join as nodes, each running an agent over the mesh. Each app runs on exactly one node, chosen by its
needs (GPU, disk, a storage location) or by the household admin. Moving an app means a backup, then a restore on the
other node. This is placement, not scheduling.

## Considered options

- **Kubernetes (k3s)**: failover and storage classes, but a household admin would have to learn Kubernetes to fix
  anything.
- **Docker Swarm**: in maintenance mode, and compose files need changes to run under it.
- **Agentless over SSH**: no local watchdog, and root-equivalent SSH to every node.
- **Peers agreeing among themselves** (consensus): survives a dead machine, but much harder to build and debug.

## Consequences

- There is no high availability: while the primary is down, sign-in is down.
- An app's own data stays on its node. Shared folders are storage locations, a local path or a network share over the
  mesh, read-only by default for media.
