---
status: proposed
date: 2026-11-11
---

# Only the edge reaches an app; apps reach only the internet

Proposed (needs-decision #82).

"Apps publish no ports" (ADR 0006, ADR 0009) says nothing about who else on a node can reach an app, or what an app
can reach. Two holes follow: any local process could connect to an app's container and send a forged identity
header, which apps that trust the edge's headers would believe; and a compromised app could reach the LAN (the
router, printers, the NAS), the mesh as its node, and the host's own listeners.

**Only the edge reaches an app.** The agent, not the compose file, wires the one upstream the edge uses:
- on Docker and rootful Podman, a bridge address;
- on rootless Podman, whose containers the host can't route to, a loopback port published only for that purpose.

In both cases the agent's firewall table accepts new connections to that upstream only from the edge's user, and
from the agent's own, for its health probes. The floor's rule stays as it is: the compose file publishes no ports.

**Apps reach the internet, and nothing else, unless an extra says so.** The firewall table drops new connections from
app networks to:
- the LAN's prefixes;
- the mesh's ranges;
- the node's own addresses, except the DNS resolver the engine provides.

"Reaches the LAN" (for example a home automation app) and "reaches the mesh" are declared extras, approved with the
passkey like the others.

**The sign-in is the one exception, decided.** An app with native sign-in calls the sign-in from its server: OIDC's
token, userinfo and keys, or LDAP binds. `auth.<main domain>` resolves to the primary's mesh address, or to the node's
own address on the primary, which the rule above drops. So:
- every app reaches the sign-in's address through the edge only: the edge's ports on the node's own address and on
  the primary's mesh address. From an app, the edge answers only the sign-in's name, so this is no path to other apps;
- an app whose sign-in method is LDAP also reaches the identity provider's LDAP outpost.

## Considered options

- **An edge sidecar per app**, inside the app's own network. Simpler to reason about, but a Caddy per app and a second
  path for the edge's configuration.
- **Trusting the bridge.** Classic's apps trusted a fixed address; on a node with other users and host apps, any local
  process can use it.
- **Only filtering what comes in.** "The LAN sees the machines, not the apps" covers incoming traffic only.

## Consequences

- The firewall matches on the connecting process's user for every app upstream, so the edge always runs as its own
  user.
- Apps that call another household app (say, a local AI model) need a path that isn't decided yet: the one open
  item.
- The firewall's namespace tests gain a container trying the LAN, the mesh and the node's own addresses.
