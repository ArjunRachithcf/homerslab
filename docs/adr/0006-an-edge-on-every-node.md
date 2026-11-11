---
status: accepted
date: 2026-11-09
---

# An edge on every node, one wildcard certificate from the primary

Every node runs its own edge: Caddy 2.11.2 or later, unprivileged, configured by the agent through Caddy's admin API.
- An app's name resolves to the node that runs it, so traffic goes straight from a device to that node.
- Apps publish no ports.
- A guard hands the sign-in's refreshed session cookie back to the browser. The forward-auth is written out, with
  `Set-Cookie` copied from the response. Without it, Kanidm households would be sent back to sign-in at every refresh
  ([Kanidm spike](../research/2026-11-09-spike-kanidm-forward-auth.md)).

**Certificates.** Each household domain gets one wildcard certificate (`*.<domain>`):
- issued on the primary, through a DNS challenge via libdns;
- handed to the edges over the agents' authenticated channel.

The DNS provider's token never leaves the primary, and the household's app names never appear in public Certificate
Transparency logs. Only ts.net names (Tailscale) carry per-node certificates.

## Considered options

- **One edge on the primary for everything.** Simpler, but every byte would pass through the primary (a film from the
  NAS to a phone included), and every node would need a port open for it.
- **A certificate per app.** Each node would hold only its own apps' keys, but every app's name would be public in the
  Certificate Transparency logs.

## Amended (2026-11-11)

Proposed (needs-decision #82).

"Apps publish no ports" still means that no app's compose file publishes one. On rootless Podman, whose containers
the host can't route to, the agent itself publishes one loopback port per app for the edge, and the firewall accepts
connections to it only from the edge's user and the agent's ([ADR 0020](0020-only-the-edge-reaches-an-app.md)).
