---
status: accepted
date: 2026-11-11
---

# Only the edge reaches an app; apps reach only the internet

Accepted (#82, the maintainer, 2026-11-11).

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
- other apps' networks;
- the node's own addresses, except the DNS resolver the engine provides.

"Reaches the LAN" (for example a home automation app) and "reaches the mesh" are declared extras, approved with the
passkey like the others.

**The sign-in is the one exception, decided here, unlike app-to-app calls.** An app with native sign-in calls the
sign-in from its server: OIDC's token, userinfo and keys, or LDAP binds. `auth.<main domain>` resolves to the
primary's mesh address, or to the node's own address on the primary, which the rule above drops. The edge can't tell
apps apart by source address (rootless Podman, apps on other nodes, a desktop primary), so apps get a listener of
their own:
- the **sign-in listener** has its own address on each node, on the household's port (ADR 0021), and carries only the sign-in's route.
  SNI and `Host` must both equal `auth.<main domain>` (`Host` compared without its port);
- the agent makes `auth.<main domain>` resolve to that address inside app containers;
- on the primary it serves the sign-in; on other nodes, the node's own edge forwards it to the primary;
- an app whose sign-in method is LDAP also reaches the same address on port 636 (LDAPS), which leads to the identity
  provider's LDAP outpost: directly on the primary, through the node's own edge on other nodes;
- the firewall lets app networks reach that listener, and its LDAP port for LDAP apps, and nothing else on the node.

## Considered options

- **An edge sidecar per app**, inside the app's own network. Simpler to reason about, but a Caddy per app and a second
  path for the edge's configuration.
- **Letting apps reach the edge's ordinary listeners**, with the edge answering only the sign-in's name to apps. The
  edge would have to tell apps from people by source address, which it can't under rootless Podman, from other
  nodes, or on a primary that is also someone's desktop.
- **Trusting the bridge.** Classic's apps trusted a fixed address; on a node with other users and host apps, any local
  process can use it.
- **Only filtering what comes in.** "The LAN sees the machines, not the apps" covers incoming traffic only.

## Consequences

- The firewall matches on the connecting process's user for every app upstream, so the edge always runs as its own
  user.
- Apps that call another household app (say, a local AI model) need a path that isn't decided yet: the one open
  item.
- The firewall's namespace tests gain a container trying the LAN, the mesh, another app's network and the node's own
  addresses.
- Whether container DNS resolves `auth.<main domain>` to the sign-in listener on hosts running systemd-resolved is
  tested first.
