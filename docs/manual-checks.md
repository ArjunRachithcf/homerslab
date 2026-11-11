# Manual checks

What CI can't run, checked by hand on the maintainer's household before a release (spec: Testing Decisions, "The
manual release checklist"). Each check says what it proves and how to see it. Secrets stay in the terminal: none is
pasted into an issue or a log.

## NetBird's cloud (#15)

CI runs the mesh contract suite against a self-hosted NetBird in containers. NetBird's cloud needs an account, so the
parts that only the cloud has are checked here.

1. **The guided dashboard visit.** At app.netbird.io, under Team › Service users, make a service user with the admin
   role and a token that expires in one day. Setup's "Where do I find the token?" says the same; if the dashboard's
   words moved, update `web/src/i18n/en.json` (`step.network.help*`).
2. **The token is given once, in the terminal.** On the setup page choose NetBird's cloud, then run
   `sudo homerslab setup secret mesh` and paste the token. The page turns to the next step by itself.
3. **HomersLab's own service user.** In the dashboard, the user `homerslab` exists, with one token named
   `homerslab-<date>` that expires in 365 days or less. Revoke the token from step 1: setup keeps working.
4. **Groups and policies.** The groups `homerslab-nodes`, `homerslab-admins`, `homerslab-adults` and `homerslab-kids`
   exist, and so do the four `homerslab-*` policies (TCP 80 and 443 to the nodes, 443 between nodes). On the cloud the
   Default policy is never deleted: setup lists it, with any policy of your own that reaches every peer or a node, and
   switches them off only after your yes. Each stays in the dashboard, disabled and otherwise unchanged: its ports,
   port ranges and posture checks. A policy made afterwards shows up in the manager's drift report within the hour,
   and stays on: `sudo curl -s --unix-socket /run/homerslab/manager.sock http://manager/api/v1/drift`.
5. **The primary joined, NAT mapper off.** The primary is a peer in `homerslab-nodes`, joined with a one-off setup key
   named `homerslab-node-<hostname>`. On the primary:
   `journalctl -u netbird | grep -E 'NAT port mapper disabled|port forwarding setup'` shows only the first, and
   `sudo curl -s --unix-socket /run/homerslab/manager.sock http://manager/api/v1/mesh` says `"portMappingOff":true`.
   Remove `/etc/systemd/system/netbird.service.d/10-homerslab.conf` and restart NetBird: within 30 seconds the agent
   puts it back, restarts the client, and reports it.
6. **The resolver.** On the primary, `resolvectl query api.netbird.io` (or `getent hosts api.netbird.io` without
   systemd-resolved) answers, and so does a mesh peer's name.
7. **The LAN is kept out.** From another device on the LAN: the machine answers ping; no app port and no SSH
   answers; the setup link's port answers until the admin's first sign-in over the mesh, and then never again.
   `sudo nft list table inet homerslab` on the primary shows the table. After a reboot it is there before the network
   and the engines: `systemd-analyze critical-chain docker.service` has `homerslab-firewall-restore.service` in it
   and not `homerslab-agent.service`, and `journalctl -b -u homerslab-agent` shows READY within seconds even with
   Docker socket-activated. After `sudo nft flush ruleset` the table is back within seconds.
8. **A self-hosted NetBird HomersLab set up.** `sudo homerslab mesh owner-password` prints the owner's password, with a
   warning, and it signs in at the NetBird dashboard. On an account HomersLab didn't set up, it says there is none.
