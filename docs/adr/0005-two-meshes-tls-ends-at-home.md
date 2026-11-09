---
status: accepted
date: 2026-11-09
---

# Two meshes, cloud or self-hosted, at once; TLS always ends at home

**Meshes.**
- NetBird and Tailscale are both supported, each with its cloud or a self-hosted control plane (self-hosted NetBird,
  Headscale).
- A household may use both at once. Every node joins every mesh in use, and devices join either.

**Never a router port.** No router port is ever opened, and TLS always ends on the household's own machines.
- With NetBird, public apps go through its reverse proxy in TLS passthrough, on the household's own domain.
- With Tailscale, they go through Funnel, on `*.ts.net` names.
- A domain is required with NetBird and optional with Tailscale.

**Zero clicks per app.** Installing an app never needs a click in a mesh's dashboard. Names, policies and public
services are created through the meshes' APIs, with tokens given once in first-time setup
([APIs](../research/2026-11-09-apis.md), [Tailscale](../research/2026-11-09-tailscale.md)).

## Considered options

- **NetBird only, behind a seam.** Recommended during the grilling, but not chosen.
- **A LAN-only mode.** Rejected: the LAN would see the apps.
- **Public paths that end TLS at a provider**, such as Cloudflare Tunnel. Rejected.
- **A relay VPS that passes TLS through.** Deferred.

## Consequences

- Headscale households have no public path, because Headscale has no Funnel.
- Without a domain, each app on Tailscale is its own tagged node under the tailnet's name. Adding a domain later, or
  switching meshes, is a guided migration.
- NetBird's reverse proxy was in beta on 2026-11-09. Its API is the first spike.
