# Tailscale as a HomersLab mesh

- **Question**: with Tailscale, can a household get three things?
  - per-app names that resolve to the right node;
  - zero-click node joining and app publishing through APIs;
  - public access on its own domain with TLS ending on its machines.
- **Checked**: 2026-11-09.
- **Versions**: client [v1.104.5](https://github.com/tailscale/tailscale/releases/tag/v1.104.5) (2026-11-03),
  Headscale [v0.29.7](https://github.com/juanfont/headscale/releases/tag/v0.29.7) (2026-10-30).
- **"Spec"** below means the live [OpenAPI spec](https://api.tailscale.com/api/v2?outputOpenapiSchema=true).
- **Outcome**: [ADR 0005](../adr/0005-two-meshes-tls-ends-at-home.md).

**Verdict**:
- **Per-app names**: yes.
- **Zero-click**: yes.
- **Public access on your own domain with TLS at home**: no. Funnel serves only `*.ts.net`.

## API

- **Authentication**:
  - User tokens last 1–90 days.
  - OAuth clients don't expire and mint scoped one-hour tokens
    ([spec](https://api.tailscale.com/api/v2?outputOpenapiSchema=true), [OAuth](https://tailscale.com/kb/1215/oauth-clients)).
  - For a home machine the practical choice is an OAuth client. Workload federation needs a public OIDC issuer
    ([WIF](https://tailscale.com/docs/features/workload-identity-federation)).
  - The API is on all plans ([kb/1101](https://tailscale.com/kb/1101/api)).
- **Scopes** include `auth_keys`, `devices:core`, `policy_file` and `dns`
  ([trust credentials](https://tailscale.com/docs/reference/trust-credentials)).
- **Auth keys**: `POST /tailnet/{t}/keys` creates reusable, ephemeral, pre-authorized, tagged keys. Keys made through
  OAuth must carry the client's tags ([spec](https://api.tailscale.com/api/v2?outputOpenapiSchema=true)). Tagged
  devices' keys don't expire by default ([auth keys](https://tailscale.com/kb/1085/auth-keys)).
- **Untested**: creating the permanent OAuth client from a one-time admin token through the same endpoint
  (`keyType: "client"`).
- **Policy**: `GET/POST /tailnet/{t}/acl` with ETags, plus a validate endpoint
  ([policy syntax](https://tailscale.com/docs/reference/syntax/policy-file)).

## Names

- **MagicDNS** takes no custom records ([kb/1054](https://tailscale.com/kb/1054/dns)), and the request has been open
  since 2021 ([#1543](https://github.com/tailscale/tailscale/issues/1543)).
- **Split DNS by API**: `PUT/PATCH /tailnet/{t}/dns/split-dns` points a domain at a resolver on the primary's
  Tailscale address, on all plans. Tailscale documents this pattern itself
  ([BYOD guide](https://tailscale.com/docs/solutions/kubernetes-operator-byod-gateway-api)). Risk: an open Android
  split-DNS regression on 1.104.5 ([#22243](https://github.com/tailscale/tailscale/issues/22243)).
- **Wildcard MagicDNS** is merged in the client but not rolled out
  ([PR 18258](https://github.com/tailscale/tailscale/pull/18258), [#1196](https://github.com/tailscale/tailscale/issues/1196)).
- **Tailscale Services**:
  - a `svc:` name, a ts.net name and a stable virtual IP, TCP only, on tagged hosts
    ([kb/1552](https://tailscale.com/kb/1552/tailscale-services));
  - GA on 2026-01-27 ([changelog](https://tailscale.com/changelog));
  - at most 10 per user on all plans ([GA](https://tailscale.com/blog/services-ga)).
- **Certificates** for ts.net names appear in public Certificate Transparency logs
  ([kb/1153](https://tailscale.com/kb/1153/enabling-https)).

## Public access: Funnel

- **Limits**: ports 443, 8443 and 10000 only; only `tailnet-name.ts.net` names; bandwidth limits that aren't
  published; beta; all plans; needs the `funnel` node attribute ([kb/1223](https://tailscale.com/kb/1223/funnel)).
- **TLS ends on the node**: the relays don't decrypt, and read only the SNI
  ([blog](https://tailscale.com/blog/introducing-tailscale-funnel)).
- **Configuration**: the node attribute through the policy API. On the node, the CLI or the serve config
  ([kb/1242](https://tailscale.com/kb/1242/tailscale-serve)), or tsnet's `ListenFunnel`
  ([tsnet API](https://tailscale.com/docs/reference/tsnet-server-api)).
- **Custom domains**: no. The request is open ([#11563](https://github.com/tailscale/tailscale/issues/11563)), and a
  CNAME to ts.net fails ([#16478](https://github.com/tailscale/tailscale/issues/16478)). Custom domains for PAM are
  Premium only, and Tailscale provisions their certificates
  ([PAM](https://tailscale.com/docs/privileged-access-management/how-to/custom-dns-domain)).

## tsnet

- **What it is**: an embedded node without `tailscaled`, on a userspace network stack, BSD-3
  ([tsnet.go](https://github.com/tailscale/tailscale/blob/main/tsnet/tsnet.go)).
- **Features**: one process can run several nodes, with `ListenTLS`, `ListenFunnel`, `ListenService`, and `ControlURL`
  for a self-hosted control plane ([tsnet API](https://tailscale.com/docs/reference/tsnet-server-api)).
- **Limits**: other processes reach the tailnet only through its loopback proxy or a custom TUN.
- **For HomersLab**: the agent can accept on a per-app node and forward to the local edge.

## Headscale (BSD-3)

- **What it is**: one tailnet ([README](https://github.com/juanfont/headscale/blob/main/README.md)), with REST and gRPC
  ([api.md](https://github.com/juanfont/headscale/blob/v0.29.7/docs/ref/api.md)).
- **API**: pre-auth keys, tags, nodes and policy
  ([proto](https://github.com/juanfont/headscale/blob/v0.29.7/proto/headscale/v1/headscale.proto)). The policy can be
  set by API only in database mode
  ([grpcv1.go](https://github.com/juanfont/headscale/blob/v0.29.7/hscontrol/grpcv1.go)).
- **DNS**: `extra_records` (A and AAAA), through a watched file, not the API
  ([dns.md](https://github.com/juanfont/headscale/blob/v0.29.7/docs/ref/dns.md)).
- **Missing**:
  - Funnel: "We cannot really implement Tailscale's Funnel"
    ([#1040](https://github.com/juanfont/headscale/issues/1040));
  - Services ([#2845](https://github.com/juanfont/headscale/issues/2845));
  - wildcard MagicDNS ([#3322](https://github.com/juanfont/headscale/issues/3322)).

## Plans

- **The free Personal plan** ([pricing](https://tailscale.com/pricing), [v4](https://tailscale.com/blog/pricing-v4)):
  - 6 users and unlimited user devices;
  - 50 tagged resources, then $1 a month each;
  - 3 ACL groups;
  - limits not enforced yet.
- **Every node counts**: every node, and every tsnet node, is a tagged device
  ([tags](https://tailscale.com/docs/features/tags)).
- **Unverified**: which plan the OAuth clients are on.

## Devices

- **Phones**: auth keys are documented for `tailscale up --auth-key`; nothing documents entering one in the iOS app.
  Phones sign in to the tailnet ([auth keys](https://tailscale.com/kb/1085/auth-keys)).
- **Tags**: tagging a device ends its user's ownership ([tags](https://tailscale.com/docs/features/tags)).

## Licences

The client daemon is BSD-3 ([LICENSE](https://github.com/tailscale/tailscale/blob/main/LICENSE)); the GUIs on closed
operating systems and the control plane are closed ([open source](https://tailscale.com/opensource)). Headscale is
BSD-3 ([LICENSE](https://github.com/juanfont/headscale/blob/main/LICENSE)).

## What this means for HomersLab

**With a domain**:
- split DNS to a resolver on the primary;
- certificates from the primary's DNS challenge;
- names on the household's own domain, mesh only.

**Without a domain**: each app is its own tagged tsnet node, named `<name>.<tailnet>.ts.net`, with Tailscale's
certificate. That is chosen over Services because of the free plan's limits: 50 tagged devices against 10 Services.

**Public access**:
- Funnel on ts.net names, with TLS ending on the node.
- There is no own-domain public path with Tailscale.
- Headscale households have no public path at all.
