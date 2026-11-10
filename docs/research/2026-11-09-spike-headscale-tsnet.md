# Can HomersLab drive Headscale with no clicks, and run per-app tsnet nodes against it? (spike)

- **Question**: with Headscale v0.29.7 as the control plane, can HomersLab, with no clicks:
  - (a) create tagged pre-auth keys and set the ACL policy through Headscale's API, with `policy.mode: database`;
  - (b) run several tsnet nodes inside one Go process, one per app, each joining with such a key, getting its MagicDNS
    name under `base_domain` and accepting HTTP on the tailnet, while another tailnet client reaches each by name;
  - (c) make per-app names on a household domain (`photos.home.test`) resolve to the right node's 100.x address, with
    split DNS to a small resolver on the tailnet, and with `extra_records_path`?
- **Checked**: 2026-11-09 and 2026-11-10, in containers on the maintainer's Fedora machine (compose project
  `hlspike-headscale`, torn down afterwards). Code, scripts and raw results: [`spikes/headscale-tsnet/`](../../spikes/headscale-tsnet/).
- **Feeds**: [ADR 0005](../adr/0005-three-meshes-one-contract-tls-ends-at-home.md); [spec](../spec.md) "Open spikes" 5
  and 7; [Tailscale and Headscale](2026-11-09-tailscale.md).

| Software | Version | Pinned as | Source |
|---|---|---|---|
| Headscale | v0.29.7 (commit `c9cf210`, built with go1.27.3) | `headscale/headscale@sha256:11f81665e842809db9fe3006a38c56a66e968baa5abd4bac498e86445572b970` | [release](https://github.com/juanfont/headscale/releases/tag/v0.29.7) |
| tsnet (`tailscale.com`) | v1.104.5 | `h1:TLemRww20+bcQ0FEVTTZ7yMtVDZ4pOpSCL86Oo9g3mA=` | [release](https://github.com/tailscale/tailscale/releases/tag/v1.104.5) |
| Tailscale client (people's devices, resolver) | v1.104.4: no v1.104.5 image exists on Docker Hub or GHCR | `tailscale/tailscale@sha256:ed6480b33c6eb67c9cbd203ed42b9fb57e361986725b6856def71b7a3d9e276c` | [Docker Hub](https://hub.docker.com/r/tailscale/tailscale/tags) |
| Go | 1.27.3 | `golang@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e` | [go.dev/dl](https://go.dev/dl/) |
| CoreDNS | 1.15.0 | `coredns/coredns@sha256:0a9d8c8ddf34347eb59aad9a8784f0710de88d6d112726d11d271f9bc6daca26` | [Docker Hub](https://hub.docker.com/r/coredns/coredns/tags) |
| Host | Fedora 44, kernel 7.2.11, SELinux enforcing, Docker 29.8.1, Compose v5.5.1 | – | – |

**Verdict**:
- **(a) Yes.** Users, tagged and personal pre-auth keys, the policy, node expiry and deletion all work through the REST
  API with one API key. The API key can rotate itself over the same API. Policy changes reach devices in about 1 s, and
  a bad policy is refused.
- **(b) Yes.** Up to 100 tsnet nodes ran in one process. Each joined with a tagged key in 1.4–1.7 s, got
  `<app>.<base_domain>` and served HTTP, and a separate Tailscale client reached each by name. Each node costs about
  1.5 MB of memory and almost no CPU when the policy keeps netmaps small.
- **(c) Yes, both ways**, and a third way works too:
  - split DNS for `home.test` to CoreDNS behind a tailnet node;
  - `extra_records_path`;
  - `base_domain` set to the household domain itself, with one tsnet node per app.
- **Four things the agent must do itself**, or it phones Tailscale Inc. or opens router ports: turn off tsnet's log
  uploader, turn off port mapping, force Noise over 443 and accept a remaining bootstrap-DNS leak during outages, and
  close a node before deleting it. Details are in [What didn't work](#what-didnt-work-and-what-the-agent-must-handle).

## Setup

```
 host 127.0.0.1:18443 ─┐   network 10.77.29.0/24 (compose project hlspike-headscale)
                       ▼
  headscale  10.77.29.10:8443  TLS (throwaway CA): API + Noise + embedded DERP region 999, STUN :3478
     ▲   policy.mode: database; base_domain ts.hlspike.test; logtail off; DERP urls: [] (embedded only)
     │
  apps       10.77.29.20  one Go process (golang:1.27.3, uid 1000): N tsnet nodes + a tsnet resolver node
  ts-dns     10.77.29.30  official client, userspace networking  ─┬─ CoreDNS (same network namespace)
  client-adult/-kid .41/.42  official client, TUN in its own netns, accept-dns, nftables mode
```

- **Embedded DERP over TLS works.** Headscale requires an https `server_url` for it
  ([config-example](https://github.com/juanfont/headscale/blob/v0.29.7/config-example.yaml)). Its DERP map entry is
  `HostName:headscale.hlspike.test IPv4:10.77.29.10 DERPPort:8443 STUNPort:3478`, and all nodes chose `derp-999`. Direct
  paths formed between containers (`pong from photos (100.64.0.3) via 10.77.29.20:57747`). Clients trusted the CA
  through `SSL_CERT_DIR`.
- **Nothing on the host.**
  - No Tailscale daemon, no sudo.
  - Ports published only on 127.0.0.1.
  - The tsnet process ran in a container, not on the host, so its UDP sockets stayed out of the LAN.
  - People's devices are official clients with a TUN device inside their own network namespace.
- **To rerun** (the folder keeps only sources and results; secrets, state and caches were deleted):
  1. `scripts/certs.sh` and `scripts/hs-config.sh`;
  2. `docker compose -p hlspike-headscale -f compose.yaml up -d headscale`;
  3. an API key from the CLI into `.secrets/auth-header`;
  4. users, the policy and keys with `scripts/api.sh` and `scripts/mkkey.sh`;
  5. `scripts/build.sh`, then `up -d ts-dns coredns apps client-adult client-kid`;
  6. `scripts/probe.sh` and `scripts/scale.sh`;
  7. `… down -v` at the end.
- **The API key** came from the CLI in Headscale's own container:
  - `headscale apikeys create --expiration 90d`. 90 days is also the default
    ([api.md](https://github.com/juanfont/headscale/blob/v0.29.7/docs/ref/api.md),
    [cli](https://github.com/juanfont/headscale/blob/v0.29.7/cmd/headscale/cli/api_key.go)).
  - It prints `hskey-api-<12-char prefix>-<secret>` once; listings show `hskey-api-O1ofRQ9BKLHA-***`.
  - It is sent as `Authorization: Bearer`.
- **Rotation works without a shell on the server**, through the API itself, as the off-site model needs:

  ```
  key 2 created with key 1 (prefix hskey-api-KPi6VZrcJknI, expires 2027-02-07T19:09:16Z)   POST /api/v1/apikey {"expiration": …}
  - key 2 expires key 1:  {}HTTP 200                                                      POST /api/v1/apikey/expire {"prefix": …}
  - key 1 now:            UnauthorizedHTTP 401
  - key 2 deletes keys 1 and 3: {}HTTP 200                                                DELETE /api/v1/apikey/{prefix}
  ```

  - Headscale never fills an API key's `lastSeen`.
  - The manager's scheduler must rotate before day 90, as it already must for NetBird's tokens.

## What worked

### (a) Keys and policy through the API

| Step | REST call (all `/api/v1/…`) | Result |
|---|---|---|
| Person | `POST user {"name":"adult"}` | `{"id":"1","name":"adult"}` |
| Validate a policy | `POST policy/check {"policy":"<HuJSON>"}` | `{}` 200, or 400 with the errors |
| Set the policy | `PUT policy {"policy":"<HuJSON>"}` | 200 with `updatedAt`; refused with 500 if invalid or a test fails |
| Read it back | `GET policy` | The HuJSON as written, comments included |
| Key for an app node | `POST preauthkey {"aclTags":["tag:app"],"expiration":"…"}` | `{"aclTags":["tag:app"],"user":null,"reusable":false}` |
| Key for a person's device | `POST preauthkey {"user":"1","expiration":"…"}` | Owned by `adult` |
| Revoke a device | `POST node/9/expire` | The kid's device is cut off in 1.5 s |
| Remove a node | `DELETE node/6` | Its name is gone from peers in 1.6 s |

- **The policy** ([`policy/policy.hujson`](../../spikes/headscale-tsnet/policy/policy.hujson)) is written with grants,
  which Headscale recommends over ACLs ([policy.md](https://github.com/juanfont/headscale/blob/v0.29.7/docs/ref/policy.md)):
  - adults reach `tag:app` on tcp/80;
  - kids reach `tag:kids` on tcp/80;
  - both reach `tag:dns` on 53.
- **Grants may only name tags defined in `tagOwners`.** An empty owner list is accepted. The error otherwise is
  `tag not found: "tag:app"`.
- **Live changes.** Granting kids `tag:app` let the kid's device fetch `photos.home.test` 1.02 s after the `PUT`.
  Removing the grant took it away again in 1.11 s.
- **Policy `tests` (beta in 0.29) guard the policy server-side**
  ([CHANGELOG](https://github.com/juanfont/headscale/blob/v0.29.7/CHANGELOG.md)):

  ```
  {"code":3, "message":"test(s) failed:\nkid@ -> tag:dns:22: expected ALLOWED, got DENIED"} HTTP 400   (check)
  {"code":2, "message":"setting policy: test(s) failed: …"} HTTP 500                                   (put; old policy kept)
  ```

- **Tagged keys make tag-owned nodes**: `user: tagged-devices`, and node key expiry off
  ([registration.md](https://github.com/juanfont/headscale/blob/v0.29.7/docs/ref/registration.md),
  [state.go](https://github.com/juanfont/headscale/blob/v0.29.7/hscontrol/state/state.go)). After 12 hours offline the
  app nodes rejoined from their state in 1.5 s.
- **Re-inviting a revoked device.** A new user-owned key brought the kid's device back as the same node (id 9, same
  address) in 4.6 s.

### (b) Per-app tsnet nodes in one process

[`go/cmd/apps/main.go`](../../spikes/headscale-tsnet/go/cmd/apps/main.go) is about 400 lines:
- one `tsnet.Server` per app, each with its own `Dir`, `Hostname`, `ControlURL` and `AuthKey`;
- `Listen("tcp", ":80")` and `http.Serve` per node;
- `LocalClient().WhoIs` to name the caller.

```
node photos: up in 1.736s: photos.ts.hlspike.test 100.64.0.3 tags=[tag:app]
node chores: up in 2.545s: chores.ts.hlspike.test 100.64.0.4 tags=[tag:app tag:kids]
all nodes started: up=6 failed=0 in 2.691s
client-adult photos.ts.hlspike.test   A=100.64.0.3   app=photos … caller=adult-laptop.ts.hlspike.test (user adult)
client-kid   photos.ts.hlspike.test   A=none         wget: bad address 'photos.ts.hlspike.test'
client-kid   chores.ts.hlspike.test   A=100.64.0.4   app=chores … caller=kid-tablet.ts.hlspike.test (user kid)
```

- **MagicDNS follows peer visibility.** The kid's device has only the resolvers and `chores` in its netmap, so other
  apps' names don't even resolve there.
- **WhoIs gives each app node the caller's person and device** (`user adult`). The agent could use it for identity
  without a forward-auth hop. Untested beyond this.
- **Expired and reused keys fail fast**, which the agent can act on:

  ```
  node noexp: FAILED: tsnet.Up: backend: authkey expired
  node photos-again: FAILED: tsnet.Up: backend: authkey already used
  ```

### (c) Per-app names on the household domain

| Variant | How | Result | Change takes |
|---|---|---|---|
| B1 split DNS → official client | `dns.nameservers.split: {home.test: [100.64.0.1]}`; CoreDNS shares the userspace client's network namespace; the client relays UDP/TCP 53 from its tailnet address to 127.0.0.1:53 | All five names resolve and load from the adult's device | Records: 0.3–1.4 s (CoreDNS `hosts` with `reload 1s`). The split entry: a Headscale restart, about 7 s |
| B2 split DNS → tsnet node | Same, pointed at `100.64.0.5`: a tsnet node in the apps process (`ListenPacket` on its tailnet IP:53 and `Listen(":53")`) relaying to CoreDNS | Works over UDP and TCP (`A 100.64.0.3`) | As B1 |
| C `extra_records_path` | `/etc/headscale/extra/records.json`, A records generated from `GET /api/v1/node` | Works on both devices | Add by atomic rename: 0.55 s (adult), 0.74 s (kid). Remove by in-place write: 1.04 s. No restart |
| D `base_domain: home.test` | Each app is its own tsnet node, so its MagicDNS name *is* `photos.home.test` | Works; the kid gets `bad address` for apps it can't reach | A Headscale restart |

- **Split DNS needs the resolver's tailnet address before it is configured.** The order is:
  1. join the resolver;
  2. read its address from `GET /api/v1/node`;
  3. write the config;
  4. restart Headscale once.

  The split entry isn't in the API ([headscale.proto](https://github.com/juanfont/headscale/blob/v0.29.7/proto/headscale/v1/headscale.proto)).
  Headscale turns it into `DNSConfig.Routes`
  ([config.go](https://github.com/juanfont/headscale/blob/v0.29.7/hscontrol/types/config.go)).
- **Split DNS to a resolver with no tun device is supported upstream.** Netstack UDP to a tailnet resolver was fixed and
  is tested in tsnet ([dns_split_test.go](https://github.com/tailscale/tailscale/blob/v1.104.5/tsnet/dns_split_test.go),
  tailscale#20314).
- **The extra-records watcher** uses fsnotify on the file and re-adds the watch after a rename. It skips an empty file,
  and `[]` clears all records ([extrarecords.go](https://github.com/juanfont/headscale/blob/v0.29.7/hscontrol/dns/extrarecords.go)).
  - `extra_records` and `extra_records_path` are mutually exclusive.
  - The client gives each extra record its own resolver-less DNS route (`Routes:{books.home.test.:[] …}`), so
    100.100.100.100 doesn't become the device's main resolver
    ([node_backend.go](https://github.com/tailscale/tailscale/blob/v1.104.5/ipn/ipnlocal/node_backend.go#L1579)).
- **Who learns which names.**
  - **B and C: everyone.** The kid's device resolves `photos.home.test → 100.64.0.3` even though it can't connect.
    Extra records are tailnet-wide.
  - **The resolver can't tell who asked.** The userspace client relays every query from `127.0.0.1`
    (`[INFO] 127.0.0.1:51836 - "A IN photos.home.test."`), so CoreDNS can't give per-person answers.
  - **D: names follow the policy**, so a kid's device only learns kids' apps. Device names land in the household domain
    too (`adult-laptop.home.test`), and could collide with an app name.

### Resource use of N tsnet nodes in one process

The process held N nodes, each serving HTTP. The policy made each app node's netmap hold only the people's devices that
may reach it. Memory figures are `/proc/self/status` after 90–120 s idle. The process is a 32.5 MB static binary, so
about 20 MB of RSS is its own mapped file.

| N | All joined in | Mean / max join | RSS | of it anon | Goroutines | Threads | FDs | Idle CPU | Container (cgroup) | Headscale |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | 1.5 s | 1.53 s | 28.9 MB | 8.1 MB | 110 | 13 | 16 | 0.01 s/min | – | – |
| 10 | 2.7 s | 1.42 / 1.71 s | 45.1 MB | 24.0 MB | 1,064 | 21 | 97 | 0.04 s/min | – | – |
| 25 | 6.1 s | 1.53 / 1.64 s | 66.0 MB | 45.0 MB | 2,654 | 25 | 232 | 0.08 s/min | – | – |
| 50 (new) | 11.2 s | 1.70 / 2.21 s | 103.9 MB | 82.9 MB | 5,305 | 25 | 457 | 0.12 s/min | 83.5 MiB | 54.7 MiB |
| 100 (50 new) | 21.3 s | 1.61 / 2.17 s | 168.0 MB | 146.9 MB | 10,611 | 30 | 907 | 0.06 s/min | 149.5 MiB | 102 MiB |
| 50, allow-all `{}` | 10.5 s | 1.50 / 1.57 s | 265.5 MB | 244.4 MB | 5,304 | 28 | 457 | 0.08 s/min | 202.4 MiB | 89 MiB |

- **The marginal cost per node** is about 1.4–1.6 MB of anonymous memory, about 106 goroutines and about 9 file
  descriptors (its own control stream, DERP connection and UDP sockets). OS threads level off near 30.
- **Joins are parallel.** 8 at a time took about 1.6 s each, so the total is about N/8 × 1.7 s.
- **The policy matters more than N.** Under allow-all every node keeps every peer (about 108) in its netmap: 4.7 MB per
  node instead of 1.5 MB.
- **Headscale idles at 21.8 MiB and 0.1 % CPU** with 108 nodes registered and 3 online. It logged no warning or error in
  12 hours.

## What didn't work, and what the agent must handle

1. **A missing `expiration` creates a dead key.** Over REST, a pre-auth key or API key created without one is stored
   with 0001-01-01 and is expired at once. That gives `authkey expired` for the pre-auth key, and 401 for the API key.
   The CLI defaults (1 h, 90 d) aren't applied ([grpcv1.go](https://github.com/juanfont/headscale/blob/v0.29.7/hscontrol/grpcv1.go)).
   **Always send an expiration.**
2. **A deleted node storms.** After `DELETE /api/v1/node/{id}`, the still-running tsnet node gets a
   `machine-unauthorized` netmap back and polls again at once. Backoff resets to about 5 ms:
   - 4,822 map responses in a minute, up to 80 per second;
   - about 1,200 log lines and 0.2 of a core per second (5.2 MB of log a minute);
   - Headscale logs none of it.

   **The agent must `Close()` the node before deleting it,** and close any node that reports `machine-unauthorized`.
3. **Bootstrap DNS tells Tailscale Inc. the control server's name.**
   - With an https `ControlURL` on a non-default port, the Noise dialer still tries `http://host:80` first
     ([ts2021/client.go](https://github.com/tailscale/tailscale/blob/v1.104.5/control/ts2021/client.go),
     [controlhttp/client.go](https://github.com/tailscale/tailscale/blob/v1.104.5/control/controlhttp/client.go)).
   - When a dial fails, the dialer asks DERP servers to resolve the name. Their list is Tailscale's built-in map, always
     merged with Headscale's, and there is no switch
     ([dnscache.go](https://github.com/tailscale/tailscale/blob/v1.104.5/net/dnscache/dnscache.go),
     [dnsfallback.go](https://github.com/tailscale/tailscale/blob/v1.104.5/net/dnsfallback/dnsfallback.go)).
   - Observed: `GET https://derp4d.tailscale.com/bootstrap-dns?q=headscale.hlspike.test`. That was about 264 requests
     to 16 different `derp*.tailscale.com` hosts from 8 nodes, at registration and at every Headscale restart. The
     official clients did it too.
   - `TS_FORCE_NOISE_443=1` removed the registration case: 0 requests in 101 node logs.
   - Every dial starts with no memory of past successes (a new `dnscache.Dialer` each time), so **a control-plane
     outage still leaks the name**. This wasn't re-tested.
   - Mitigations:
     - have Headscale, or its proxy, accept TCP on 80 as well as 443;
     - filter egress to the built-in DERP addresses;
     - or ask upstream for an opt-out.
4. **tsnet's log uploader ignores `TS_NO_LOGS_NO_SUPPORT`.** tsnet builds its own uploader to log.tailscale.com
   ([tsnet.go `startLogger`](https://github.com/tailscale/tailscale/blob/v1.104.5/tsnet/tsnet.go)).
   - Headscale switches it off in every full map response (`Debug.DisableLogTail`, with `logtail.enabled: false`, the
     default) ([mapper.go](https://github.com/juanfont/headscale/blob/v0.29.7/hscontrol/mapper/mapper.go)).
   - That only happens after the first map. **Call `logtail.Disable()` before starting any node**, as the spike does
     ([logtail.go](https://github.com/tailscale/tailscale/blob/v1.104.5/logtail/logtail.go)).
5. **Port mapping is on by default**, which conflicts with "never a router port". tsnet compiles in UPnP, NAT-PMP and
   PCP, and the official client probed the gateway (`portmap: monitor: gateway and self IP changed: gw=10.77.29.1`).
   **Set `TS_DISABLE_PORTMAPPER=1`, or build with `-tags ts_omit_portmapper`**
   ([portmapper.go](https://github.com/tailscale/tailscale/blob/v1.104.5/net/portmapper/portmapper.go),
   [condregister](https://github.com/tailscale/tailscale/blob/v1.104.5/feature/condregister/portmapper/maybe_portmapper.go)).
   Headscale can only turn off UPnP, through the node attribute `debug-disable-upnp`.
6. **No HTTPS and no Funnel**, as expected:

   ```
   probe photos: CertDomains=[]
   probe photos: ListenTLS: tsnet: you must enable HTTPS in the admin panel to proceed. See https://tailscale.com/s/https
   probe photos: ListenFunnel: Funnel not available; HTTPS must be enabled. See https://tailscale.com/s/https.
   probe photos: CertPair(photos.ts.hlspike.test): 500 Internal Server Error: your Tailscale account does not support getting TLS certs
   ```

7. **Smaller things:**
   - **`GET /api/v1/policy` before any policy is stored** answers 500 `acl policy not found`.
   - **A tagged key plus advertised tags is refused.** A tagged-key registration that also sends `AdvertiseTags` fails,
     so tsnet must leave `AdvertiseTags` empty ([state.go](https://github.com/juanfont/headscale/blob/v0.29.7/hscontrol/state/state.go)).
   - **tsnet keeps its startup name.** It printed the old DNS name after `base_domain` changed; the agent must re-read
     status.
   - **containerboot exits on NeedsLogin.** The official image quits when its device is expired: `tailscaled left
     running state (now in state "NeedsLogin"), exiting`
     ([containerboot](https://github.com/tailscale/tailscale/blob/v1.104.5/cmd/containerboot/main.go)).
   - **No iptables-legacy.** The client image needed `TS_DEBUG_FIREWALL_MODE=nftables` on this kernel.
   - **Non-peer addresses leave by the default route.** A Linux client routes only its peers' /32s (table 52), so a
     connect to a non-peer 100.64.0.0/10 address goes out the default route. The contract suite adds
     `ip route add blackhole 100.64.0.0/10` inside each client container.
8. **I caused 64,887 SELinux denials in 78 s.**
   - A `docker compose run` of the same service relabelled the shared `:Z` bind mount, even though its networking then
     failed. That cut the running `apps` container off from its own log files.
   - Fixed in the spike's [`compose.yaml`](../../spikes/headscale-tsnet/compose.yaml): each `:Z` folder belongs to one
     service, and one-off runs use a fixed container name so two never overlap. The fix was re-checked with 0 denials.
   - The lesson for the agent: **never mount one `:Z` folder into two containers.** Docker relabels on every start.

## The mesh contract, row by row, for Headscale

| Row | Headscale | Proven here? |
|---|---|---|
| **Joining**: a key for a node | Tagged pre-auth key by `POST /api/v1/preauthkey`, single-use, with an explicit expiry | **Yes**: 100+ nodes, tsnet and the official client |
| A person's device: invites, revocation | User-owned key by API; `POST node/{id}/expire`, `DELETE node/{id}` | **Partly**: keys, revocation and re-invite yes. Sign-in through OIDC not tested |
| **Names** | Split DNS to a tailnet resolver, set once in the config (restart). Also `extra_records_path` (a file on the server) and `base_domain` = the household domain (with per-app nodes) | **Yes**, all three |
| **Policies** per role | `policy.mode: database`, `PUT /api/v1/policy`, `POST /api/v1/policy/check`, policy `tests` | **Yes**: enforced in about 1 s, bad policies refused |
| **Public path** | None | **Yes, none**: no cert domains; TLS, Funnel and certificates refused |
| **Domain** | Required: Headscale issues no certificates in v0.29.7 ([PR #3554](https://github.com/juanfont/headscale/pull/3554) is unreleased), so the primary's DNS challenge is the only HTTPS | **Yes** for names: any domain works, even `.test`. HTTPS on those names was not part of the spike |
| A report of what it can do | – | Not exercised |

**What this means for HomersLab**
- **Split DNS to the primary's resolver is the right default for Headscale**, as ADR 0005 says:
  - its records stay at home;
  - `extra_records_path` needs write access to a file on the off-site server, which "URL and API key" doesn't give.
- **`base_domain` = household domain is worth considering when every app is its own tsnet node.** Names then follow the
  policy, which split DNS and extra records can't do. The cost is device names sharing the domain.
- **The agent's tsnet settings, all verified here:**
  - `logtail.Disable()`;
  - `TS_DISABLE_PORTMAPPER=1`;
  - `TS_FORCE_NOISE_443=1`;
  - no `AdvertiseTags`;
  - `Close()` before `DELETE`;
  - explicit expirations;
  - API-key rotation through the API;
  - a policy that keeps each app node's netmap to its audience.

## Open questions

1. **Sign-in for people's devices** through Headscale's OIDC login against Authentik and against Kanidm (spike 7). Not
   tried.
2. **Phones.** How iOS and Android handle split DNS to a tailnet resolver, and per-name extra-record routes. Compare the
   Android split-DNS regression [#22243](https://github.com/tailscale/tailscale/issues/22243).
3. **Bootstrap DNS during outages.** Measure it with `TS_FORCE_NOISE_443=1` and an outage, then decide between Headscale
   on 80+443, egress filtering and an upstream request.
4. **The deleted-node storm.** Is the fix in Headscale (stop answering) or in the client's backoff? Report it upstream
   with the spike's numbers.
5. **Per-person DNS answers.** A resolver inside the agent (tsnet `ListenPacket`) sees the real tailnet source and could
   answer per person with `WhoIs`. Untested.
6. **NetBird alongside Headscale on one node.** Both default to 100.64.0.0/10 (a node may already run NetBird in that range).
   Headscale allows a subset prefix; test both clients on one node.
7. **tsnet nodes on a real host.** They bind their UDP sockets on all interfaces, and `Server.Port` sets only the port,
   so the agent's firewall must keep the LAN out.
8. **HTTPS on MagicDNS names** once PR #3554 ships in a release: rerun the TLS probe.

## Sources

- **Headscale v0.29.7**:
  - [config-example.yaml](https://github.com/juanfont/headscale/blob/v0.29.7/config-example.yaml),
    [api.md](https://github.com/juanfont/headscale/blob/v0.29.7/docs/ref/api.md),
    [dns.md](https://github.com/juanfont/headscale/blob/v0.29.7/docs/ref/dns.md),
    [policy.md](https://github.com/juanfont/headscale/blob/v0.29.7/docs/ref/policy.md),
    [tags.md](https://github.com/juanfont/headscale/blob/v0.29.7/docs/ref/tags.md),
    [registration.md](https://github.com/juanfont/headscale/blob/v0.29.7/docs/ref/registration.md),
    [container.md](https://github.com/juanfont/headscale/blob/v0.29.7/docs/setup/install/container.md),
    [CHANGELOG](https://github.com/juanfont/headscale/blob/v0.29.7/CHANGELOG.md);
  - [headscale.proto](https://github.com/juanfont/headscale/blob/v0.29.7/proto/headscale/v1/headscale.proto),
    [grpcv1.go](https://github.com/juanfont/headscale/blob/v0.29.7/hscontrol/grpcv1.go),
    [db/api_key.go](https://github.com/juanfont/headscale/blob/v0.29.7/hscontrol/db/api_key.go),
    [types/preauth_key.go](https://github.com/juanfont/headscale/blob/v0.29.7/hscontrol/types/preauth_key.go),
    [state.go](https://github.com/juanfont/headscale/blob/v0.29.7/hscontrol/state/state.go),
    [types/config.go](https://github.com/juanfont/headscale/blob/v0.29.7/hscontrol/types/config.go),
    [dns/extrarecords.go](https://github.com/juanfont/headscale/blob/v0.29.7/hscontrol/dns/extrarecords.go),
    [derp_server.go](https://github.com/juanfont/headscale/blob/v0.29.7/hscontrol/derp/server/derp_server.go),
    [mapper.go](https://github.com/juanfont/headscale/blob/v0.29.7/hscontrol/mapper/mapper.go).
- **Tailscale v1.104.5**:
  - [tsnet.go](https://github.com/tailscale/tailscale/blob/v1.104.5/tsnet/tsnet.go),
    [tsnet/dns_split_test.go](https://github.com/tailscale/tailscale/blob/v1.104.5/tsnet/dns_split_test.go),
    [ipnlocal/node_backend.go](https://github.com/tailscale/tailscale/blob/v1.104.5/ipn/ipnlocal/node_backend.go);
  - [control/ts2021/client.go](https://github.com/tailscale/tailscale/blob/v1.104.5/control/ts2021/client.go),
    [control/controlhttp/client.go](https://github.com/tailscale/tailscale/blob/v1.104.5/control/controlhttp/client.go),
    [net/dnscache/dnscache.go](https://github.com/tailscale/tailscale/blob/v1.104.5/net/dnscache/dnscache.go),
    [net/dnsfallback/dnsfallback.go](https://github.com/tailscale/tailscale/blob/v1.104.5/net/dnsfallback/dnsfallback.go);
  - [net/portmapper/portmapper.go](https://github.com/tailscale/tailscale/blob/v1.104.5/net/portmapper/portmapper.go),
    [logtail/logtail.go](https://github.com/tailscale/tailscale/blob/v1.104.5/logtail/logtail.go),
    [control/controlclient/direct.go](https://github.com/tailscale/tailscale/blob/v1.104.5/control/controlclient/direct.go),
    [cmd/containerboot/main.go](https://github.com/tailscale/tailscale/blob/v1.104.5/cmd/containerboot/main.go).
- **Images**:
  [headscale](https://hub.docker.com/r/headscale/headscale/tags),
  [tailscale](https://hub.docker.com/r/tailscale/tailscale/tags),
  [golang](https://hub.docker.com/_/golang/tags),
  [coredns](https://hub.docker.com/r/coredns/coredns/tags).
