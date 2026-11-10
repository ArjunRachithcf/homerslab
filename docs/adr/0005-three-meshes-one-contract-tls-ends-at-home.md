---
status: accepted
date: 2026-11-09
---

# NetBird, Tailscale and Headscale behind one contract; TLS always ends at home

**Meshes.** HomersLab supports three meshes, all implementations of one mesh contract:
- **NetBird**, cloud or self-hosted.
- **Tailscale**, on Tailscale's cloud.
- **Headscale**: Tailscale's clients against the household's own control server.

A household may use NetBird together with Tailscale or Headscale. Every node joins every mesh in use, and devices join
either. The meshes are built in this order:
1. NetBird.
2. Headscale, which runs in CI.
3. Tailscale's cloud, which adds only its API adapter and Funnel.

**Never a router port.** No router port is ever opened, and TLS always ends on the household's own machines.
- **The clients' own port mapping is switched off on every node.** That means UPnP, NAT-PMP and PCP. It is on by
  default in NetBird since v0.68 and in Tailscale's clients.
- **Public apps with NetBird** go through its reverse proxy in TLS passthrough, on the household's own domain.
- **Public apps with Tailscale** go through Funnel, on `*.ts.net` names.
- **Headscale** has no public path.
- **A domain** is required with NetBird and Headscale, and optional with Tailscale.

**Names.**
- **NetBird**: wildcard records in its DNS zones, by API.
- **Tailscale**: split DNS to a resolver on the primary, by API.
- **Headscale**: its base domain is the household domain, set once in first-time setup. Every app is its own tsnet node
  named `<app>.<domain>`, so a device only learns the names of the apps its person may open.

**Zero clicks per app.** Installing an app never needs a click in a mesh's dashboard. Names, policies and public
services are created through the meshes' APIs, with tokens given once in first-time setup. Sources:
[APIs](../research/2026-11-09-apis.md), [Tailscale and Headscale](../research/2026-11-09-tailscale.md),
[NetBird spike](../research/2026-11-09-spike-netbird-api.md),
[Headscale spike](../research/2026-11-09-spike-headscale-tsnet.md).

## Considered options

- **NetBird only, behind a seam.** Recommended during the grilling, but not chosen.
- **Tailscale without Headscale.** That would leave Tailscale-based households dependent on Tailscale's cloud.
- **Split DNS for Headscale.** Every device would see every app's name, and changing it needs a Headscale restart.
- **A LAN-only mode.** Rejected: the LAN would see the apps.
- **Public paths that end TLS at a provider**, such as Cloudflare Tunnel. Rejected.
- **A relay VPS that passes TLS through.** Deferred.

## Consequences

- **Self-hosted control planes live off-site.** Self-hosted NetBird and Headscale run on a server reachable from the
  internet, outside the household's machines, and HomersLab reaches them by URL and API key.
- **Headscale's sign-in.** People's devices sign in to it with the household account through its OIDC login. This
  isn't tested yet.
- **What Headscale households give up.** They have no public apps unless they also use NetBird. They also need a
  domain: Headscale's `tailscale cert` support was merged on 2026-10-18 but wasn't in a release as of v0.29.7.
- **Tailscale without a domain.** Each app is its own tagged node under the tailnet's name. Adding a domain later, or
  switching meshes, is a guided migration.
- **NetBird's reverse proxy** was in beta on 2026-11-09.
  - On self-hosted NetBird, every change to the proxy needs a connected proxy.
  - On the cloud, the first token needs one visit to the dashboard.
