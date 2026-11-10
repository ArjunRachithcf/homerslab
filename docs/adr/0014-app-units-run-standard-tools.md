---
status: accepted
date: 2026-11-10
---

# App units run standard tools; the Compose SDK only reads and plans

Each app runs as one systemd unit:
- with Docker, the unit runs the standard `docker compose` CLI, pinned by the agent's package;
- with Podman, the app runs as Quadlet units.

The agent uses Docker's Compose SDK only for status, health, logs and planning. The SDK is pinned exactly, starting at
v5.7.0 ([Compose SDK spike](../research/2026-11-09-spike-compose-sdk.md)).

The result: an ejected app runs exactly as it did before, and HomersLab's binary is never in an app's start path.

## Considered options

- **The unit runs HomersLab's binary with the SDK.** One code path, with behaviour fully under our control. But
  HomersLab would sit in every app's start path, and eject would have to translate.

## Consequences

- **Reserved project names.** Compose project names are reserved for HomersLab, because Compose claims a container by
  its labels alone.
- **The dry run.** The SDK's dry run is never combined with output or error streams: in v5.5–v5.7 that combination
  applies the changes for real.
- **Upgrades.** Every SDK upgrade passes three checks:
  - an API diff;
  - the engine contract suite;
  - a golden test of config hashes.
