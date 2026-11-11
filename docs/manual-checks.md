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
   `sudo nft list table inet homerslab` on the primary shows the table, and it is there right after a reboot, before
   Docker starts (`systemd-analyze critical-chain docker.service` has `homerslab-agent.service` in it). After
   `sudo nft flush ruleset` the table is back within seconds.

## The domain, on the maintainer's own DNS provider (#16)

CI runs the DNS provider contract suite against a test CA (Pebble) and a test DNS server (BIND, through RFC 2136), and
the edge's request matrix against a real Caddy. A real provider and a real CA need an account and a real domain, so
they are checked here, on a domain kept for rehearsals. Run the manager with
`--certificate-authority letsencrypt-staging` for every check but the last: rehearsals never use up Let's Encrypt's
limits.

1. **Infomaniak's token, in the terminal.** Make a token with the domain scope (Your profile › Developer › API tokens).
   On the setup page give the domain and Infomaniak, then run `sudo homerslab setup secret dns`: the terminal says
   "This token is for <domain>, and goes to Infomaniak's API" before it reads the token. The page turns to the next
   step by itself. Setup's "Where do I find the token?" says the same; if Infomaniak's words moved, update
   `web/src/i18n/en.json` (`step.domain.helpInfomaniak*`).
2. **Only HomersLab's records.** During the issuance the zone holds `_acme-challenge.<domain>` TXT and
   `_homerslab.<domain>` TXT, and afterwards neither. MX, SPF, DKIM, DMARC and the apex are as they were (compare the
   zone export from before).
3. **Beside the public wildcard.** With a `*` CNAME in the zone (classic's), the certificate is issued all the same:
   the propagation check asks Infomaniak's name servers for the exact TXT record.
4. **The certificate.** `openssl s_client -connect <mesh address>:<port> -servername admin.<domain>` from a device on
   the mesh shows `*.<domain>` alone, issued by Let's Encrypt's staging CA. https://crt.sh/?q=<domain> lists only the
   wildcard, never an app's name.
5. **NetBird's cloud port.** On NetBird's cloud, setup's answers say `householdPort` 9443 (`netbird.CloudPort`), and
   Admin answers at `https://admin.<domain>:9443` from a phone on the mesh. Try a TLS passthrough service on 443 and
   8443 on the shared cluster too: if one is accepted now, the constant changes (ADR 0021).
6. **The LAN.** From another device on the LAN, `admin.<domain>` never resolves to a mesh address (the mesh's zone is the mesh's
   only) and the primary's port answers nothing.
7. **The console.** Revoke the token at Infomaniak, set the clock aside or wait for a renewal: Admin's warnings list
   (`sudo curl -s --unix-socket /run/homerslab/manager.sock http://manager/api/v1/warnings`) has one warning that names
   `sudo homerslab dns token`. Make a new token and give it there: the renewal runs at once, and the warning is resolved.
8. **CAA.** Add `CAA 0 issue "sectigo.com"` to the zone and give the token again: the terminal says the CAA records
   don't let the authority issue. Remove it.
9. **Production, once.** With `--certificate-authority letsencrypt`, issue the household's real certificate.
