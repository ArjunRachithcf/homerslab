# Spike: a self-hosted NetBird, driven only by its API

- **Question**: against a local, self-hosted NetBird v0.82.x running in containers, can HomersLab do its zero-click
  operations by API, with a token obtained without a browser (`POST /api/setup` with `NB_SETUP_PAT_ENABLED=true`)?
  This is spec "Open spikes" no. 1, extended to the whole mesh contract.
- **Checked**: 2026-11-09, NetBird v0.82.0 on the maintainer's Fedora 44 PC, in its own compose project
  (`hlspike-netbird`) on an internal Docker network: no internet, no published ports, no route to the host's real
  NetBird. Torn down afterwards with `docker compose -p hlspike-netbird down -v`. It took 19 minutes from the first
  `up` to the teardown, well inside the 90-minute budget.
- **Answer**: yes, for every operation tried.
  - **The token.** `POST /api/setup` returned a PAT without a browser. A service user's PAT then took over, and the
    service user rotated it by itself.
  - **Mesh operations.** Groups, a TCP 443 policy, setup keys, joining, a DNS zone with a wildcard, user invites,
    revocation and the complete teardown all work by API. The policy and the names were checked from inside the
    joined peers.
  - **The public path, end to end.** The reverse proxy took a token issued by API, a custom domain validated by its
    CNAME, and a `mode: tls` service with PROXY v2 to a peer. A request from "the internet" reached the edge, TLS ended
    there with the edge's own certificate, and the edge saw the client's address.
  - **The catch: the reverse-proxy API needs a connected proxy.** With none connected, adding a domain fails (500)
    and adding a service fails (412 "no proxy clusters available").
- **Evidence**:
  - every call, redacted: [`spikes/netbird-api/out/transcript.md`](../../spikes/netbird-api/out/transcript.md);
  - the setup: [`spikes/netbird-api/`](../../spikes/netbird-api/), whose test credentials stay in its git-ignored
    `secrets/`.

## Versions

| Image or module | Version | Digest (multi-arch index) |
|---|---|---|
| `netbirdio/netbird-server` (management, signal, relay, STUN, embedded Dex) | 0.82.0 | `sha256:7b5b53b2dc8bc25639f791cf72d47cf86911894ff3f8a97cb79bab9be958109c` |
| `netbirdio/reverse-proxy` | 0.82.0 | `sha256:cc0deaed250ca1332e28f40caca780055cdda7871750cb020478f6fe96e1f6d7` |
| `netbirdio/netbird` (client) | 0.82.0 | `sha256:eca5b274c635bfbb04ec6724a0712d1146190dd450eea199eeb83916fdd94965` |
| `caddy` (the edge stand-in) | 2.11.9-alpine | `sha256:0bbb055bda6793c999bde78b1b5ce3e89f829fb031af2cf50056ea1246559fee` |
| `coredns/coredns` (stand-in for public DNS) | 1.13.1 | `sha256:9b9128672209474da07c91439bf15ed704ae05ad918dd6454e5b6ae14e35fee6` |
| `nicolaka/netshoot` (curl, dig, jq) | v0.15 | `sha256:47b907d662d139d1e2f22bfe14f4efca1e3f1feed283572f47c970c780c03b61` |
| `golang` (build of the Go check) | 1.26.4-alpine | `sha256:3ad57304ad93bbec8548a0437ad9e06a455660655d9af011d58b993f6f615648` |
| Go module `github.com/netbirdio/netbird` | v0.82.0 | commit `3c00a97878e6af537d28c3bdd4f42cb4286aa883` ([proxy.golang.org](https://proxy.golang.org/github.com/netbirdio/netbird/@v/v0.82.0.info)) |

The host ran Docker Engine 29.8.1, Compose v5.5.1 and kernel 7.2.11. v0.82.0 is the latest release
([releases](https://github.com/netbirdio/netbird/releases)); v0.83.0-rc.1 appeared the same day and wasn't used.
Neither the dashboard image nor Traefik was needed.

## The setup

**Topology.** One internal network, `10.231.0.0/24`, holds every container
([compose.yaml](../../spikes/netbird-api/compose.yaml)):

| Container | Address | Role |
|---|---|---|
| `server` | `10.231.0.10`, alias `nb.hlspike.test` | the combined server |
| `proxy` | `10.231.0.20` | NetBird's reverse proxy |
| `edge` | `10.231.0.30` | a node; its edge (Caddy) shares its network namespace |
| `device` | `10.231.0.31` | a household device, with a tools container in its namespace |
| `tools` | `10.231.0.5` | the API client, and later "the internet" |
| `dns` | `10.231.0.53` | CoreDNS, the stand-in for public DNS |

The account's mesh was set to `100.88.0.0/16` through the API, away from the range of the host's real NetBird.

**Server.** The [config](../../spikes/netbird-api/server-config.yaml.tmpl) is `getting-started.sh`'s
`render_combined_yaml` ([v0.82.0](https://github.com/netbirdio/netbird/releases/download/v0.82.0/getting-started.sh)),
reduced to what a sandbox needs:
- **Plain HTTP and h2c.** `listenAddress: ":80"`, with `exposedAddress: "http://nb.hlspike.test:80"`.
- **Everything on one port.** The server derived signal `http://nb.hlspike.test:80`, relay `rel://nb.hlspike.test:80`
  and STUN `stun:nb.hlspike.test:3478`. Management gRPC, signal, the relay's WebSocket (`/relay`), the REST API and the
  embedded IdP (`/oauth2`) share port 80. Beside them run STUN on UDP 3478, a legacy gRPC port (33073), metrics (9090)
  and health (9000).
- **The embedded IdP** has issuer `http://nb.hlspike.test/oauth2`, SQLite and generated secrets.
- **Offline.** `disableAnonymousMetrics` and `disableGeoliteUpdate` are both true.
- **Environment.** `NB_SETUP_PAT_ENABLED=true`, and `NB_DISABLE_GEOLOCATION=true`. Without the second, a fresh
  install still downloads GeoLite2 from `pkgs.netbird.io`, even with `disableGeoliteUpdate: true`, and exits when it
  can't (`FATL ... could not initialize geolocation service`;
  [modules.go](https://github.com/netbirdio/netbird/blob/v0.82.0/management/internals/server/modules.go)). The version
  check to `pkgs.netbird.io` only logs an error.

**Clients.** The `netbirdio/netbird` entrypoint runs `netbird service run`, then `netbird up`, configured by
environment:
- **Variables.** `NB_SETUP_KEY`, `NB_MANAGEMENT_URL=http://nb.hlspike.test:80` and `NB_HOSTNAME`.
- **Capabilities.** Only `NET_ADMIN`: the client used kernel WireGuard, and `wt0` and its iptables rules lived in the
  container's own namespace.
- **DNS.** The client rewrote the container's `/etc/resolv.conf` to its own resolver on its mesh address.

**Proxy.** `netbirdio/reverse-proxy` runs with these settings:
- **Connection.** `NB_PROXY_TOKEN` from the API, `NB_PROXY_MANAGEMENT_ADDRESS=http://nb.hlspike.test:80` and
  `NB_PROXY_ALLOW_INSECURE=true`.
- **Identity and listener.** `NB_PROXY_DOMAIN=proxy.hlspike.test` (the cluster address) and `NB_PROXY_ADDRESS=:8443`.
- **No ACME.** `NB_PROXY_ACME_CERTIFICATES=false`, with a self-signed certificate for `*.proxy.hlspike.test` in `/certs`.
- **Offline.** `NB_PROXY_DISABLE_GEOLOCATION=true`
  ([geolocation.go](https://github.com/netbirdio/netbird/blob/v0.82.0/proxy/internal/geolocation/geolocation.go)).
- **No Traefik.** Nothing sits in front: clients reach its listeners directly on the internal network.

**The stand-in for public DNS.** CoreDNS serves two zones, and nothing is forwarded
([Corefile](../../spikes/netbird-api/dns/Corefile)):
- `proxy.hlspike.test A 10.231.0.20`;
- `*.home.test CNAME proxy.hlspike.test`, the same single wildcard CNAME that classic's DNS zone carries, which also
  answers `validation.home.test`.

The server and the proxy get it through `dns:`. Docker's embedded resolver forwards to it, even on an internal
network.

**The edge.** Caddy runs in the `edge` peer's namespace
([Caddyfile](../../spikes/netbird-api/edge-web/Caddyfile)), with Caddy's local CA standing in for ACME:
- `:443`, the mesh listener;
- `:8444`, the public listener, with `proxy_protocol` (allowed from `100.88.0.0/16`) and then `tls`.

**Calls.** curl ran inside `tools`, with the token read from a mounted header file, through
[lib.sh](../../spikes/netbird-api/lib.sh). The official Go client ran as a static binary in the same network
([gocheck](../../spikes/netbird-api/gocheck/main.go)).

## Each operation

Identifiers are shortened. `→` gives the status and a short excerpt. Secrets are never shown.

### Bootstrap and tokens

```
GET  /api/instance                                   (no token) → 200 {"setup_required":true}
POST /api/setup                                      (no token)
     {"email":"owner@home.test","name":"Spike Owner","password":"…","create_pat":true,"pat_expire_in":1}
     → 200 {"user_id":"CiQx…","email":"owner@home.test","personal_access_token":"nbp_…"}   (40 characters)
     with pat_expire_in 366 → 422 "pat_expire_in must be between 1 and 365" (nothing created; setup still required)
     a second time          → 412 "setup already completed"
GET  /api/users/{owner}/tokens → 200 [{"name":"setup-token","expiration_date":"<created + 1 day>"}]
PUT  /api/accounts/{id}  {"settings":{…,"network_range":"100.88.0.0/16"}} → 200   (the default was a random /16, 100.74.0.0/16)
POST /api/users  {"name":"homerslab","role":"admin","auto_groups":[],"is_service_user":true} → 200 {"status":"active"}
POST /api/users/{svc}/tokens  {"name":"homerslab-1","expires_in":365} → 200 {"plain_token":"nbp_…","personal_access_token":{"expiration_date":"2027-11-09…"}}
     expires_in 0 or 366 → 422 "expiration has to be between 1 and 365"
```

**Rotation by the service user alone.** With its own token, the service user minted `homerslab-2`
(`POST /api/users/{svc}/tokens` → 200), switched to it, and revoked `homerslab-1`
(`DELETE /api/users/{svc}/tokens/{id}` → 200). The old token then got
`404 "invalid token: pat: <hash of the token> not found"`.
- `GET /api/users/current` answers 403 for a service user, so it can't serve as a "who am I".

### Groups, the policy, setup keys

```
POST   /api/groups {"name":"household"} → 200      {"name":"edges"} → 200      a duplicate name → 409 "group with name edges already exists"
GET    /api/policies → 200 [{"name":"Default","rules":[{"protocol":"all","bidirectional":true,"sources":["All"],"destinations":["All"]}]}]
DELETE /api/policies/{Default} → 200
POST   /api/policies {"name":"household-to-edges-https","enabled":true,"rules":[{"name":"https","enabled":true,"action":"accept",
       "bidirectional":false,"protocol":"tcp","ports":["443"],"sources":["<household>"],"destinations":["<edges>"]}]} → 200
POST   /api/setup-keys {"name":"homerslab-nodes","type":"reusable","expires_in":86400,"auto_groups":["<edges>"],"usage_limit":0,"ephemeral":false}
       → 200 {"state":"valid","key":"…"}   (36 characters, shown once; GET /api/setup-keys shows the first five, then "****")
POST   /api/setup-keys {"name":"anna-phone","type":"one-off","expires_in":86400,"auto_groups":["<household>"],"usage_limit":1} → 200
       expires_in 3600 → 200, expiring an hour later, although the spec's minimum is 86400
```

### Joining

The `edge` and `device` containers started with their keys. Within about a second, each client logged
`Netbird engine started, the IP is: 100.88.7.186/16` and `Connected`.

```
GET /api/peers → 200
  {"name":"edge","ip":"100.88.7.186","dns_label":"edge.netbird.selfhosted","connected":true,"version":"0.82.0",
   "groups":["All","edges"],"user_id":"","login_expiration_enabled":false}
  {"name":"device","ip":"100.88.26.210","groups":["All","household"],…}
GET /api/setup-keys → homerslab-nodes used_times 1, "valid";  anna-phone "overused", valid false
```

Peers that join with a setup key belong to no user and never expire.

### DNS

```
POST /api/dns/zones {"name":"household","domain":"home.test","enabled":true,"enable_search_domain":false,
                     "distribution_groups":["<household>","<edges>"]} → 200 {"records":[]}
     domain "*.home.test" → 422 "invalid zone domain format"
POST /api/dns/zones/{zone}/records {"name":"*.home.test","type":"A","content":"100.88.7.186","ttl":60}      → 200
                                   {"name":"app.home.test","type":"A","content":"100.88.7.186","ttl":60}    → 200
                                   {"name":"node2.home.test","type":"A","content":"100.88.26.210","ttl":60} → 200
                                   {"name":"www.home.test","type":"CNAME","content":"app.home.test","ttl":60} → 200
     "x.elsewhere.test" → 422 "record name does not belong to zone"
     a CNAME "*.home.test" beside the A → 422 "an a, aaaa, or cname record with name *.home.test already exists"
```

The device's resolver was `nameserver 100.88.26.210` (NetBird's own), with the search domain `netbird.selfhosted`.
From the device, with `dig` and `getent`:

| Name | Answer | What it shows |
|---|---|---|
| `app.home.test` | `100.88.7.186` | the per-app record |
| `anything.home.test`, `photos.home.test`, `zzz.home.test` | `100.88.7.186` | the wildcard |
| `node2.home.test` | `100.88.26.210` | a specific record beats the wildcard |
| `www.home.test` | `app.home.test.`, then `100.88.7.186` | a CNAME inside the zone |
| `home.test` | no answer | the wildcard doesn't cover the apex |

The edge resolved the same names.

### The policy, checked from the device

- **The allowed port.** `curl https://app.home.test/` (port 443) → 200
  `edge says hello: sni=app.home.test listener=443 client=100.88.26.210`, with issuer
  `CN=Caddy Local Authority - ECC Intermediate`.
- **Any other port.** `nc -z app.home.test 8444` timed out: dropped by the edge's NetBird firewall, not refused.

### The reverse proxy

**With no proxy connected:**

```
GET  /api/reverse-proxies/clusters → 200 []          GET /api/reverse-proxies/domains → 200 []
POST /api/reverse-proxies/domains {"domain":"home.test","target_cluster":"proxy.hlspike.test"} → 500 "internal server error"
POST /api/reverse-proxies/services {…app.home.test…} → 412 "could not derive cluster from domain app.home.test: no proxy clusters available"
```

The 500 means "target cluster not available": the error isn't typed
([domain manager](https://github.com/netbirdio/netbird/blob/v0.82.0/management/internals/modules/reverseproxy/domain/manager/manager.go)).

**The proxy's token, by API, then the proxy started:**

```
POST   /api/reverse-proxies/proxy-tokens {"name":"homerslab-proxy","expires_in":0} → 200 {"plain_token":"nbx_…","expires_at":null}   (40 characters)
POST   /api/reverse-proxies/proxy-tokens {"name":"short","expires_in":3600} → 200 {"expires_at":"<created + 1 h>"}
DELETE /api/reverse-proxies/proxy-tokens/{short} → 200; still listed, "revoked":true
GET    /api/reverse-proxies/clusters → 200 [{"address":"proxy.hlspike.test","type":"account","online":true,"connected_proxies":1,
       "supports_custom_ports":true,"require_subdomain":false,"supports_crowdsec":false,"private":false}]
GET    /api/reverse-proxies/domains  → 200 [{"domain":"proxy.hlspike.test","type":"free","validated":true,…}]
```

**Custom domains.** Validation is a CNAME lookup of `validation.<domain>` through the server's resolver, which must
equal the cluster address
([validator.go](https://github.com/netbirdio/netbird/blob/v0.82.0/management/internals/modules/reverseproxy/domain/validator.go)).
The stand-in DNS answered it, so validation passed locally:

```
POST /api/reverse-proxies/domains {"domain":"home.test","target_cluster":"proxy.hlspike.test"} → 200 {"type":"custom","validated":true}   (validated on creation)
POST /api/reverse-proxies/domains {"domain":"nocname.test",…} → 200 {"validated":false}
GET  /api/reverse-proxies/domains/{nocname}/validate → 412 "dns lookup for validation.nocname.test failed; retry the validation"
     "*.wild.test" → 422 "invalid domain format"      "home.test" again → 409 "domain home.test is already registered"
     target_cluster "eu.proxy.netbird.io" → 500
```

**The service.** These requests were refused, before any proxy call
([service.go](https://github.com/netbirdio/netbird/blob/v0.82.0/management/internals/modules/reverseproxy/service/service.go)):

```
mode tls without listen_port      → 422 "listen_port is required for tls services"
mode tls with two targets         → 422 "tls services must have exactly one target"
mode tls with bearer (SSO) auth   → 422 "auth is not supported for tls services"
mode http with proxy_protocol     → 422 "target 0: proxy_protocol is not supported for http services"
domain under unvalidated nocname  → 412 "… domain x.nocname.test is not validated"
domain nobody added               → 412 "… domain y.example.org does not match any available proxy cluster"
```

```
POST /api/reverse-proxies/services
     {"name":"app","domain":"app.home.test","mode":"tls","listen_port":9443,"enabled":true,
      "targets":[{"target_id":"<edge peer id>","target_type":"peer","protocol":"tcp","port":8444,"enabled":true,
                  "options":{"proxy_protocol":true}}]}
     → 200 {"proxy_cluster":"proxy.hlspike.test","port_auto_assigned":false,"meta":{"status":"pending"},
            "targets":[{"host":"100.88.7.186","port":8444,"options":{"proxy_protocol":true}}]}
GET  /api/reverse-proxies/services/{id} → "status":"active"  (about a second later)
```

The proxy's log showed each step:
- `custom TCP listener started [port 9443]`;
- `TLS passthrough mapping added [domain app.home.test, target 100.88.7.186:8444]`;
- `starting embedded netbird client … block_inbound: true, block_lan_access: true`;
- `notified management about tunnel connection`.

**The public path, from "the internet".** `tools` resolves `app.home.test` the way the public would, to
`proxy.hlspike.test`, `10.231.0.20`:
- **The service.** `curl -k https://app.home.test:9443/` → 200 `edge says hello: sni=app.home.test listener=9443
  client=10.231.0.5`, with issuer `CN=Caddy Local Authority - ECC Intermediate`.
- **TLS ended at home.** The certificate is the edge's own. The proxy holds only its self-signed one, so it never
  terminated TLS.
- **PROXY v2 worked.** The connection reached Caddy from the proxy's mesh address, and Caddy reported the internet
  client's address.
- **Unknown names are dropped.** A name with no service, on the same port, was dropped.

**More on the same proxy:**
- **Port sharing.** A second app, `photos.home.test`, on the same `listen_port 9443` → 200, and both served, routed
  by SNI. The same domain twice → 409 "domain already taken".
- **Port 443.** The apex `home.test` on `listen_port 443` → 200 and served. This is a self-hosted cluster with
  custom ports; on the shared cloud cluster, classic found 443 unavailable.
- **Changes.** `PUT /api/reverse-proxies/services/{photos}` with `"enabled":false` → 200, and its public requests
  failed from then on.
- **No visible policy, no visible proxy peer.** `/api/policies` still held only my policy, and the proxy's peer
  isn't in `/api/peers`. Even so, the edge's ACL went from 2 to 4 rules the moment the first service was created
  (`ACL rules processed … total rules count: 4`). Management opens the target port from the proxy to the target
  peer by itself.

### People and devices

```
POST /api/users/invites {"email":"anna@home.test","name":"Anna","role":"user","auto_groups":["<household>"],"expires_in":3600}
     → 200 {"invite_token":"nbi_…","expires_at":"<created + 1 h>","expired":false}   (40 characters)
GET  /api/users/invites/{token}          (no token) → 200 {"email":"anna@home.test","name":"Anna","valid":true,"invited_by":""}
POST /api/users/invites/{token}/accept   (no token) {"password":"…"} → 200 {"success":true}      again → 404 "user invite not found"
GET  /api/users → Anna: role "user", status "active", auto_groups ["<household>"]
PUT  /api/users/{anna} {"role":"user","auto_groups":["<household>"],"is_blocked":true} → 200 {"status":"blocked"}
DELETE /api/peers/{device} → 200
PUT  /api/setup-keys/{nodes} {"revoked":true,"auto_groups":["<edges>"]} → 200 {"state":"revoked","valid":false}
```

**After the device's peer was deleted:**
- its HTTPS request to the edge failed, and its names stopped resolving, within six seconds;
- its client logged `PermissionDenied … peer is not registered`, then
  `no peer auth method provided, please use a setup key or interactive SSO login`;
- it couldn't come back, because its one-off key was spent.

### Teardown

The rules on what has to go first:
- `DELETE /api/groups/{edges}` while a policy uses it → 400 "group has been linked to policy:
  household-to-edges-https".
- The same for a group in a user's `auto_groups` → 400 "group has been linked to user: …".
- `DELETE /api/reverse-proxies/domains/{home.test}` while services use it → 412 "custom domain has dependent services;
  delete or move them before deleting the domain".

Then, in order, every call succeeded:
1. The three services: each → 200, and the public path failed straight away.
2. Both domains: each → 204.
3. The proxy's token, the proxy and its cluster:
   - Revoking the live proxy token → 200, but the proxy stayed online: a token is checked only when the proxy
     connects.
   - `DELETE /api/reverse-proxies/clusters/proxy.hlspike.test` with the proxy connected → 200. The proxy then
     reconnected with its revoked token (`Unauthenticated … token expired or revoked`, then `ResourceExhausted … too
     many failed authentication attempts`, retrying every 1–2 s), and the cluster came back as offline.
   - With the proxy stopped first, the same `DELETE` → 200 and `[]`.
4. The zone, which took its four records with it → 200. Its names stopped resolving.
5. The edge's peer, both keys, the policy and both groups → 200.
6. Anna → 200; no invites were left.
7. The owner's token deleted the service user → 200, and the service user's token → 404.
8. The owner revoked its own setup PAT → 200, then got 404 itself.

The end state holds the owner and nothing else, with `setup_required: false` for good.

**No way back in without a browser.** The embedded Dex advertises `authorization_code`, `client_credentials`,
`refresh_token`, `device_code` and `token-exchange`. A password grant (`POST /oauth2/token`, `grant_type=password`,
for `netbird-cli` or `netbird-dashboard`) gets `unsupported_grant_type`. Once every PAT is gone, getting back in takes a
person at a browser.

### The official Go REST client

`github.com/netbirdio/netbird/shared/management/client/rest` v0.82.0 built and ran against the sandbox
([pkg](https://pkg.go.dev/github.com/netbirdio/netbird/shared/management/client/rest)):
- **The build.** It needs Go 1.26 (the module says `go 1.26.0`, and its toolchain at least 1.26.2), and gives a
  9.4 MB static binary (`CGO_ENABLED=0`). It adds only five indirect modules: go-jsonmerge, uuid, oapi-codegen runtime,
  logrus and x/sys.
- **What it read.** It listed the instance, peers, groups, policies, setup keys, DNS zones, clusters, domains, services
  and proxy tokens, all decoding without error.
- **What it wrote.** It created and deleted a group and a DNS record.
- **A refused request** came back as an error carrying the server's message ("at least one target is required").
- **What it covers.** It has every API used above: `Instance.Setup`, `Users.CreateInvite`/`AcceptInvite`, `Tokens`,
  `DNSZones`, `ReverseProxyServices`/`Domains`/`Clusters`/`Tokens`.

## The mesh contract, for NetBird self-hosted

| Contract row | Self-hosted (this spike) | Where the cloud differs (from docs, not tested) |
|---|---|---|
| **A key for joining a node** | **Proven.** A reusable setup key with `auto_groups`, by API. The node joins with `NB_SETUP_KEY` and `NB_MANAGEMENT_URL`, is listed within a second, and is in its groups. | The same API and client, without `NB_MANAGEMENT_URL` (the default is `api.netbird.io`). There is no `/api/setup`, so the first token needs one visit to the dashboard ("create one in the NetBird dashboard under User settings" ([auth](https://docs.netbird.io/api/guides/authentication))); after that, the service user and its PATs work by API. 120 requests a minute, bursts to 1,200. |
| **Device invites and revocation, per person and role** | **Proven for the API side.** One-off setup key per device, with the role as `auto_groups`. Invite created and accepted by API (`nbi_` token, single use), a person blocked, a device's peer deleted (cut off at once; it can't rejoin on a spent key), a key revoked. **Not run**: a person's own device signing in, which is interactive by design (browser or device code); phones. | `/api/users/invites` is "only available when embedded IdP is enabled" ([spec](https://github.com/netbirdio/netbird/blob/v0.82.0/shared/management/http/api/openapi.yml)). On the cloud, `POST /api/users` "sends an invite to a regular user", who accepts it by e-mail in a browser. Setup keys, peer deletion and blocking are the same. |
| **Per-app names on the right node** | **Proven.** A custom zone, a wildcard `*.home.test`, per-app A records, a CNAME. A specific record beats the wildcard, the zone is limited by `distribution_groups`, and the peers resolve through NetBird's resolver. The apex isn't covered by the wildcard. | The same management code, so expected to be identical; not tested on the cloud. Mesh names need no registered domain (any zone is served only to its groups); certificates and the public path still do. |
| **Policies per role** | **Proven.** Groups as roles, the Default policy deleted, TCP 443 household→edges enforced in the target peer's firewall (443 answered, 8444 dropped). Services add their own proxy→target rules, unseen in `/api/policies`. | The same. |
| **A public path** | **Proven end to end, through a local proxy.** Proxy token, cluster, custom domain validated by CNAME, a TLS-passthrough service with PROXY v2 to a peer, SNI sharing of one port, port 443, a change (disable) and removal. Every write needs a connected proxy. A real deployment adds a public address (below). | Shared clusters (`type: shared`, e.g. `eu.proxy.netbird.io`), with nothing to run. TLS listen ports are restricted there: classic found 443 and 8443 unavailable in 2026-10, and 9443 worked. Private services aren't offered on shared clusters, and plan availability is unverified ([apis.md](2026-11-09-apis.md)). An account can register its own proxy on the cloud too (`type: account`, the same proxy-token API), but that needs a public server. |
| **A report of what it can do** | **Proven.** `GET /api/instance` (`setup_required`), `/api/instance/version`, the cluster flags (`online`, `connected_proxies`, `type`, `supports_custom_ports`, `require_subdomain`, `supports_crowdsec`, `private`), the domain flags (`type`, `validated`, `supports_private`), and account settings (`embedded_idp_enabled`, `local_auth_disabled`, `network_range`). **Missing**: which TLS listen ports a cluster accepts (only a yes/no for custom ports), and anything about the clients' NAT mapper. | The same endpoints, describing NetBird's clusters. On a shared cluster, the allowed TLS ports have to be found by trying. |

## Tokens: lifetimes and rotation

| Credential | Lifetime | Rotation and revocation | Seen in the spike |
|---|---|---|---|
| Setup PAT (`/api/setup`) | `pat_expire_in` 1–365 days, default 1 | Once only: 412 afterwards | 422 outside the range; named `setup-token` |
| PAT, owner or service user | `expires_in` 1–365 days, required | `POST`/`DELETE /api/users/{id}/tokens[/{tokenId}]`, which a service user can do for itself | A revoked token gets 404 "invalid token", not 401. The message carries the token's hash, so don't log error bodies. |
| Proxy token (`/api/reverse-proxies/proxy-tokens`) | `expires_in` in seconds; 0 means never | `DELETE` revokes it, and it stays listed. A running proxy isn't disconnected. | Rotation: new token, restart the proxy, revoke the old one. A proxy with a revoked token is rate-limited. |
| Setup key | `expires_in` in seconds; the spec says 86400–31536000 | `PUT` with `revoked: true`, or `DELETE` | 3600 accepted; one-off keys become `overused` after one join |
| User invite | `expires_in` in seconds; default 72 hours | `DELETE`, or `…/regenerate` | `nbi_`, single use |
| Relay credentials | 12 hours (server configuration) | Automatic | Internal to the mesh |

The spec's scheduler line ("NetBird service-user tokens last at most 365 days") holds. The rotation job needs only the
service user's own token.

## What a real proxy would add

The spike's proxy sat on the same internal network as its "internet". A household's self-hosted proxy, on the
off-site server ADR 0005 calls for, adds:
- **A public address** for its listen ports.
  - TLS passthrough needs no certificate and no ACME: in the spike, TLS mode never touched the proxy's certificate.
  - Services in HTTP mode would need ACME (`tls-alpn-01` on 443, or `http-01`) or static certificates
    ([enable-reverse-proxy](https://docs.netbird.io/selfhosted/migration/enable-reverse-proxy)).
- **Ports.**
  - **In Docker,** every extra listen port is a `ports:` mapping, and changing one recreates the container, which
    briefly interrupts every service (same doc).
  - **The main port.** A TLS service whose `listen_port` equals the proxy's main port is routed by the main listener's
    SNI router (`routerForPort`, [server.go](https://github.com/netbirdio/netbird/blob/v0.82.0/proxy/server.go)). The
    main port is 8443 in Docker, behind `getting-started.sh`'s Traefik on 443. So one public port could serve every
    app with no port changes. This is read from the code, not tested.
- **In front of it.** `getting-started.sh` adds Traefik on 443:
  - a TCP router `HostSNI(*)` with `tls.passthrough` to `proxy:8443`;
  - a `pp-v2` servers transport;
  - `NB_PROXY_PROXY_PROTOCOL=true` and `NB_PROXY_TRUSTED_PROXIES=<Traefik's address>`.
- **WireGuard.** For direct connections to peers, the proxy's own client needs a fixed `NB_PROXY_WG_PORT`, that UDP
  port forwarded, and `NB_PROXY_EXTERNAL_IP_MAP`; without them its traffic is relayed (same doc).
- **DNS.** An A record for the cluster name, and the household's wildcard CNAME to it, which also answers
  `validation.<domain>`. A custom domain must be validated within 48 hours, or the registration is removed
  ([custom domains](https://docs.netbird.io/manage/reverse-proxy/custom-domains)).
- **The management server.** Set `reverseProxy.trustedPeers` (or `trustedHTTPProxies`). Without it the server
  warns: "TrustedPeers contains the default route 0.0.0.0/0, which trusts X-Forwarded-For from every client".

## What a CI job needs

The spike's compose file is already that job:
- **A runner.** Docker with Compose, and a kernel with WireGuard (mainline since 5.6). Without it, NetBird falls back
  to userspace and needs `/dev/net/tun`.
- **Eight long-running containers.** CoreDNS, the server, tools, the proxy, and two clients, each with a helper in its
  namespace (Caddy for the edge, netshoot for the device).
- **Privileges.** `NET_ADMIN` on the two clients only: no privileged containers, no host network, no published ports,
  no sudo.
- **No internet after the image pulls.** Set `NB_DISABLE_GEOLOCATION=true` and `NB_PROXY_DISABLE_GEOLOCATION=true`.
- **A DNS server playing public DNS**, handed to the server and the proxy through `dns:`, so that custom domains
  validate.
- **No outside secrets.** Every secret is generated on the spot: the config's keys, the owner's password, the tokens.
- **Seconds per step.** The server was up about two seconds after start, the peers connected one or two seconds after
  theirs, and services went active about a second after creation.
- **Pins.** The images by digest, as in the table above.

## Worth acting on

1. **Every node's NetBird client should get `NB_DISABLE_NAT_MAPPER=true`.**
   - **What it does.** Since v0.68.0 (2026-04-08,
     [#5202](https://github.com/netbirdio/netbird/pull/5202)), with PCP from v0.69.0
     ([#5219](https://github.com/netbirdio/netbird/pull/5219)), the client asks the router to map its WireGuard UDP
     port, through UPnP, NAT-PMP or PCP (on some gateways the mapping also opens an IPv6 pinhole), unless that variable
     is set
     ([env.go](https://github.com/netbirdio/netbird/blob/v0.82.0/client/internal/portforward/env.go)).
   - **Seen here.** The sandbox's clients logged `port forwarding setup: discover gateway: no NAT found`.
   - **Why it matters.** It clashes with "No router ports, ever". The API has no account-wide switch, so the agent has
     to set it on each node's service. Any client from v0.68.0 on does this wherever the router answers; the spike
     didn't check classic's host, whose NetBird it never touched.
2. **Plan public services only against a connected cluster.**
   - Before planning, check `GET /api/reverse-proxies/clusters` for `online`.
   - Treat the 500 from `POST /domains` as "cluster not available".
   - Self-hosted, HomersLab depends on the proxy's server being up for any public-path change.
3. **Bootstrap in one step.** Run `/api/setup`, create the service user and mint its 365-day PAT together.
   - The setup PAT lasts one day by default, `/api/setup` works only once, and there's no browserless way back in.
   - Keep the owner's password for a browser sign-in if all else fails.
4. **Rotation is self-service.** The service user mints its successor and revokes the old token.
   - Treat a 404 "invalid token" as an authentication failure.
   - Never log the bodies of error responses.
5. **Services carry hidden firewall rules.** Creating a service opens the target port from the proxy's hidden peer.
   - It isn't visible in policies or peers, so drift checks have to look at services, not just policies.
   - The proxy's own client runs with `block_inbound` and `block_lan_access`.
6. **Order of removal.**
   - services before domains;
   - policies, and users' `auto_groups`, before groups;
   - for a self-hosted proxy: stop it, then delete the cluster, or it registers again. Revoking its token alone doesn't
     disconnect it.
7. **Don't rely on server-side minimums.** A setup key with `expires_in` 3600 was accepted, though the spec says the
   minimum is 86400. HomersLab should enforce its own limits.

## Open questions

- **The cloud.** Is the reverse proxy on the free plan, and which TLS ports do the shared clusters accept today? Both
  are still unverified, and need a cloud account, which this spike kept away from.
- **One public port.** Does a TLS service on the proxy's main port, behind Traefik on 443, work end to end? It's read
  from the code only.
- **The proxy without a certificate.** Does the proxy start with no certificate at all when it serves only TLS
  passthrough? This spike gave it a self-signed one.
- **People's devices.** Can the embedded Dex use the household's Authentik or Kanidm as an upstream sign-in, set by API
  (`/api/identity-providers`)? Then people's devices would sign in with the household account (spec: "otherwise the
  mesh's sign-in").
- **DNS on hosts.** In containers the client rewrote `/etc/resolv.conf` (`file` mode). Its behaviour with
  systemd-resolved on a real node wasn't tested.
- **Auditing.** Is there an API that shows the implicit proxy→target rules, or the proxy's peer?

## Sources

- NetBird docs:
  - [self-hosted quickstart](https://docs.netbird.io/selfhosted/selfhosted-quickstart);
  - [automated setup (`/api/setup`, `NB_SETUP_PAT_ENABLED`)](https://docs.netbird.io/selfhosted/automated-setup);
  - [enable the reverse proxy](https://docs.netbird.io/selfhosted/migration/enable-reverse-proxy);
  - [custom domains](https://docs.netbird.io/manage/reverse-proxy/custom-domains);
  - [reverse proxy](https://docs.netbird.io/manage/reverse-proxy);
  - [API authentication](https://docs.netbird.io/api/guides/authentication).
- NetBird v0.82.0 release and files:
  - [the release](https://github.com/netbirdio/netbird/releases/tag/v0.82.0);
  - [getting-started.sh](https://github.com/netbirdio/netbird/releases/download/v0.82.0/getting-started.sh);
  - [combined/config.yaml.example](https://github.com/netbirdio/netbird/blob/v0.82.0/combined/config.yaml.example);
  - [the OpenAPI spec](https://github.com/netbirdio/netbird/blob/v0.82.0/shared/management/http/api/openapi.yml).
- NetBird v0.82.0 source:
  - [combined/cmd/config.go](https://github.com/netbirdio/netbird/blob/v0.82.0/combined/cmd/config.go) (signal,
    relay and STUN derived from `exposedAddress`);
  - [the domain validator](https://github.com/netbirdio/netbird/blob/v0.82.0/management/internals/modules/reverseproxy/domain/validator.go);
  - [the domain manager](https://github.com/netbirdio/netbird/blob/v0.82.0/management/internals/modules/reverseproxy/domain/manager/manager.go);
  - [service validation](https://github.com/netbirdio/netbird/blob/v0.82.0/management/internals/modules/reverseproxy/service/service.go);
  - [the service manager](https://github.com/netbirdio/netbird/blob/v0.82.0/management/internals/modules/reverseproxy/service/manager/manager.go)
    (ports, SNI sharing, cluster derivation);
  - [proxy/server.go](https://github.com/netbirdio/netbird/blob/v0.82.0/proxy/server.go);
  - [the proxy's flags](https://github.com/netbirdio/netbird/blob/v0.82.0/proxy/cmd/proxy/cmd/root.go);
  - [the client's port mapper](https://github.com/netbirdio/netbird/blob/v0.82.0/client/internal/portforward/env.go).
- Pull requests: [#5202 NAT-PMP/UPnP](https://github.com/netbirdio/netbird/pull/5202) (v0.68.0) and
  [#5219 PCP](https://github.com/netbirdio/netbird/pull/5219) (v0.69.0).
- [Go REST client](https://pkg.go.dev/github.com/netbirdio/netbird/shared/management/client/rest).
- [Earlier API research](2026-11-09-apis.md), which this spike tested.
