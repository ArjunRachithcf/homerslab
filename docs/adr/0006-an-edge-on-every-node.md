---
status: accepted
date: 2026-11-09
---

# An edge on every node, certificates from the primary

Every node runs its own edge: Caddy, unprivileged, configured by the agent through Caddy's admin API.
- An app's name resolves to the node that runs it, so traffic goes straight from a device to that node.
- Apps publish no ports.

Certificates are issued on the primary, through a DNS challenge via libdns, and handed to the edges over the agents'
authenticated channel. The DNS provider's token never leaves the primary.

## Considered options

- **One edge on the primary for everything.** Simpler, but every byte would pass through the primary (a film from the
  NAS to a phone included), and every node would need a port open for it.
