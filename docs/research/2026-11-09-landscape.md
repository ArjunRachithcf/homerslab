# Self-hosted app managers, November 2026

- **Question**: does an existing project already do what HomersLab intends, without reinventing it? It would need
  per-app sign-in, private by default with no router ports, encrypted backups with restore tests, a polished UI,
  many apps, and open source on your own Linux.
- **Checked**: 2026-11-09, against primary sources: official docs, the projects' repositories, licence files.
- **App counts**: counted from the repositories or public APIs on that day.
- **"Not documented"** means the docs read don't mention it, not that it's proven absent.
- **Outcome**: [ADR 0001](../adr/0001-build-only-the-glue.md).

## Comparison

| | Format | Catalog | SSO | Auto-OIDC | Remote access | Per-app backup | Host | Licence |
|---|---|---|---|---|---|---|---|---|
| Umbrel | manifest + compose + app_proxy | official PRs + community URLs | login wall | no | Tor, Tailscale | whole device, encrypted | OS | PolyForm-NC |
| Runtipi | config.json + compose `x-runtipi` | official + custom URLs | forward-auth to its own login | no | LAN; VPN apps | yes, unencrypted | installer | GPL-3.0 |
| CasaOS / ZimaOS | compose `x-casaos` | official + third-party | no | no | Zima P2P; VPN apps | not documented | installer / OS | Apache-2.0 / freemium |
| Cosmos | cosmos-compose | several sources | yes, OIDC IdP | partial | Nebula + VPS tunnel | yes, restic | container | Apache + Commons Clause |
| YunoHost | manifest.toml + bash | PRs + CI levels | SSOwat + LDAP | planned | public 80/443 | yes | Debian | AGPL-3.0 |
| Cloudron | manifest + own image | curated + unreviewed | OIDC/LDAP/proxyAuth | yes | public; VPN option | yes, encrypted | fresh Ubuntu | proprietary |
| StartOS | s9pk (TS SDK, LXC) | signed registries | not documented | no | Tor, WireGuard, StartTunnel | yes, encrypted | OS | MIT |
| Coolify | compose + magic variables | in-repo PRs | no | no | proxy domains | databases only | installer, multi-server | Apache-2.0 |
| Dokploy | compose + template.toml | in-repo PRs | no | no | Traefik / Swarm | databases + volumes | installer, Swarm | Apache-2.0 + DSAL |
| Unraid CA | XML | 5.4k, scanned | no | no | Tailscale per container | no | OS (paid) | proprietary |
| TrueNAS | Jinja compose + questions | one official | no | no | – | not in UI | OS | LGPL-3.0 (apps) |
| Portainer / Dockge | JSON v3 / raw compose | template URL / none | no | no | – | no | container | zlib / MIT |
| Sandstorm | capnp + spk | App Market | platform-wide | n/a | – | – | installer | Apache-2.0 |
| Olares | Helm + manifest | Market | Authelia + LLDAP | yes | Tailscale VPN, FRP/Cloudflare | system-level | K3s / ISO | AGPL-3.0 |

## Per project

**Umbrel** (umbrelOS 2.0.1)
- **Format**: `umbrel-app.yml` plus compose, with a settings-only `app_proxy` service. Optional `exports.sh`,
  `hooks/` and templates. Per-install secrets come from `APP_SEED`/`APP_PASSWORD`. 2.0 lets users edit
  `environment`/`folderAccess`, and an app can declare `backupIgnore`
  ([skill](https://github.com/getumbrel/umbrel-apps/blob/master/.agents/skills/umbrel-package-app/SKILL.md)).
- **Sign-in**: the proxy puts the Umbrel login (with 2FA) in front of each app, with path whitelists and a per-app
  off switch. No OIDC is documented.
- **Catalog**: about 396 official apps, and contributors are told to point a coding agent at `AGENTS.md`
  ([repo](https://github.com/getumbrel/umbrel-apps)). A community store is a repo with `umbrel-app-store.yml`, added
  by URL, with no review documented ([template](https://github.com/getumbrel/umbrel-community-app-store)).
- **Lifecycle**:
  - updates are human PRs that bump `version`
    ([update skill](https://github.com/getumbrel/umbrel-apps/blob/master/.agents/skills/umbrel-update-app/SKILL.md));
  - uninstalling deletes the app's data
    ([doc](https://umbrel.com/support/apps/how-to-uninstall-an-app));
  - backups are hourly, encrypted and of the whole device
    ([doc](https://umbrel.com/support/backups-and-recovery/backing-up-your-data)).
- **Access**: Tor built in, Tailscale as an app ([doc](https://umbrel.com/support/basics/remote-access)). 2.0 added
  automatic HTTPS, multiple users and an MCP server with per-agent revocable tokens
  ([release](https://github.com/getumbrel/umbrel/releases/tag/2.0.0)).
- **Licence**: PolyForm Noncommercial ([FAQ](https://github.com/getumbrel/umbrel/wiki/License-FAQ)); umbrel-apps has
  no licence file.
- **Complaint**: a report of backups failing silently
  ([thread](https://community.umbrel.com/t/backups-die-silently-when-kopias-cache-exhausts-sd-card-inodes-umbrelos-1-7-4/25377)).

**Runtipi**
- **Format**: `config.json` plus compose with an `x-runtipi` block. `form_fields` include a `random` type that
  generates secrets ([config](https://runtipi.io/docs/reference/config-json)). Traefik labels are generated
  ([compose](https://runtipi.io/docs/reference/dynamic-compose)), and user overrides live in `user-config/`
  ([doc](https://runtipi.io/docs/guides/customize-app-config)).
- **Catalog**: about 268 apps, GPL-3.0. Renovate and an update bot open most bumps
  ([store](https://github.com/runtipi/runtipi-appstore/pulls?q=is%3Apr+is%3Aclosed)). Custom stores are supported
  ([guide](https://runtipi.io/docs/guides/create-your-own-app-store)).
- **Sign-in**: a per-app flag adds forward-auth against Runtipi's own login
  ([code](https://github.com/runtipi/runtipi/blob/develop/packages/backend/src/modules/docker/builders/traefik-labels.builder.ts),
  [v4.9.0](https://github.com/runtipi/runtipi/releases/tag/v4.9.0)). OIDC login is an open PR
  ([#2571](https://github.com/runtipi/runtipi/pull/2571)).
- **Access**: LAN by default; VPN through the Tailscale or NetBird apps
  ([doc](https://runtipi.io/docs/learn/networking)). Local names use its own CA
  ([doc](https://runtipi.io/docs/learn/reverse-proxy)).
- **Lifecycle**:
  - backups and updates run as tracked jobs ([v4.11](https://github.com/runtipi/runtipi/releases/tag/v4.11.0));
  - failed updates roll back ([v4.10.2](https://github.com/runtipi/runtipi/releases/tag/v4.10.2));
  - archives are plain tar.gz ([doc](https://runtipi.io/docs/guides/auto-backup-apps)).
- **Complaint**: an authenticated remote code execution through backup file names, fixed in 4.7.0
  ([advisory](https://github.com/runtipi/runtipi/security/advisories/GHSA-vrgf-rcj5-6gv9)).

**CasaOS / ZimaOS**
- **Format**: compose with an `x-casaos` block, and no way to generate secrets
  ([spec](https://github.com/IceWhaleTech/CasaOS-AppStore/blob/main/docs/specs/compose-and-x-casaos.md)).
- **Catalog**: about 182 apps, Apache-2.0. Third-party stores work the same way
  ([guide](https://github.com/IceWhaleTech/CasaOS-AppStore/blob/main/docs/guides/third-party-store-guide.md)).
- **Access**: apps publish host ports. ZimaOS adds peer-to-peer remote access, and mentions neither SSO nor per-app
  backup ([features](https://www.zimaspace.com/docs/zimaos/features)).
- **Status and pricing**: CasaOS's last release was v0.4.15, Dec 2024
  ([releases](https://github.com/IceWhaleTech/CasaOS/releases)). ZimaOS is an appliance with no licence in its repo
  ([repo](https://github.com/IceWhaleTech/ZimaOS)) and a paid tier
  ([pricing](https://www.zimaspace.com/zimaos/pricing)).

**Cosmos Cloud**
- **Format**: cosmos-compose: compose plus `routes` and templating ([doc](https://cosmos-cloud.io/docs/cosmos-compose/)).
- **Catalog**: about 151 apps, AGPL-3.0, plus configurable sources
  ([types](https://github.com/azukaar/Cosmos-Server/blob/master/src/utils/types.go)).
- **Built in**: a reverse proxy with HTTPS, an auth server with MFA and OpenID, and anti-bot protection
  ([README](https://github.com/azukaar/Cosmos-Server)).
- **Auto-OIDC**: partial, through per-route PKCE clients
  ([changelog](https://github.com/azukaar/Cosmos-Server/blob/master/changelog.md),
  [Paperless template](https://github.com/azukaar/cosmos-servapps-official/blob/master/servapps/Paperless-ngx/cosmos-compose.json));
  other apps need a client set up by hand ([doc](https://cosmos-cloud.io/docs/openid/)).
- **VPN and backups**: a Nebula-based VPN ([doc](https://cosmos-cloud.io/docs/constellation-vpn/)), and restic
  backups with restore in the UI ([doc](https://cosmos-cloud.io/docs/backups/)).
- **Licence**: Apache-2.0 plus Commons Clause plus an anti-tampering clause, which is not OSI open source
  ([LICENCE](https://github.com/azukaar/Cosmos-Server/blob/master/LICENCE)).

**YunoHost**
- **Format**: `manifest.toml` with resources (sources pinned by sha256, generated database passwords), and bash
  scripts on the host ([manifest](https://doc.yunohost.org/en/dev/packaging/manifest),
  [resources](https://doc.yunohost.org/en/dev/packaging/resources/)).
- **Catalog**: about 728 apps, with quality levels set by CI ([catalog](https://github.com/YunoHost/apps)).
- **Sign-in**: SSOwat, LDAP and identity headers
  ([doc](https://doc.yunohost.org/en/dev/packaging/advanced/sso_ldap_integration/)); OIDC via Authelia is a
  work-in-progress PR ([#2149](https://github.com/YunoHost/yunohost/pull/2149)).
- **Access**: ports 80/443 forwarded on the router
  ([doc](https://doc.yunohost.org/en/admin/get_started/post_install/port_forwarding/)).
- **Licence**: AGPL-3.0.

**Cloudron** (the reference for per-app sign-in)
- **Format**: `CloudronManifest.json` plus an image ([doc](https://docs.cloudron.io/packaging/manifest/)).
- **Sign-in**: the `oidc` addon registers a client at install and injects its id, secret and issuer. `ldap` and
  `proxyAuth` exist but can't be added after install ([addons](https://docs.cloudron.io/packaging/addons/)).
- **Backups**: per app, optionally encrypted, with automatic backups before updates
  ([doc](https://docs.cloudron.io/backups/)) and integrity checks ([9.1](https://blog.cloudron.io/cloudron-9-1/)).
- **Host**: needs ports 80/443, a fresh Ubuntu, and no ARM ([doc](https://docs.cloudron.io/installation/)).
- **Licence**: proprietary ([licence](https://www.cloudron.io/legal/license.html)).

**StartOS 0.4**
- **Format**: signed `.s9pk` packages, built with a TypeScript SDK, run in LXC
  ([release](https://github.com/Start9Labs/start-technologies/releases/tag/start-os/v0.4.0),
  [packaging](https://docs.start9.com/packaging/)).
- **Catalog**: registries, where users choose which signing keys to trust
  ([doc](https://docs.start9.com/packaging/publishing.html)).
- **Access**: Tor, WireGuard and a VPS tunnel
  ([doc](https://docs.start9.com/start-os/0.4.0.x/networking-strategy.html)).
- **Licence**: MIT ([repo](https://github.com/Start9Labs/start-technologies)).

**Olares** (the closest overall)
- **Format**: a Helm chart plus `OlaresManifest.yaml`. Each entrance has an `authLevel`, and `oidc.enabled` injects
  a client id, secret and issuer ([manifest](https://www.olares.com/docs/developer/develop/package/manifest.html)).
- **Sign-in**: Authelia plus LLDAP in front of every request
  ([doc](https://www.olares.com/docs/developer/concepts/account)).
- **Access**: a Tailscale-based VPN, a tunnel, or Cloudflare
  ([blog](https://www.olares.com/blog/how-olares-access-works/)).
- **Host**:
  - K3s on Ubuntu 22.04–25.04 or Debian 12/13, with 8 GB RAM, 150 GB of SSD, and NVIDIA-only GPUs
    ([install](https://docs.olares.com/manual/get-started/install-linux-script.html)).
  - Worker nodes are Alpha and not recommended for production
    ([multi-node](https://docs.olares.com/manual/best-practices/install-olares-multi-node.html)).
  - A custom domain and device activation go through Olares Space and the LarePass app
    ([custom domain](https://docs.olares.com/manual/best-practices/set-custom-domain.html)).
- **Licence**: AGPL-3.0, with about 402 apps under Apache-2.0 ([README](https://github.com/beclab/Olares),
  [apps](https://github.com/beclab/apps)).

**Coolify**
- **Format**: compose with magic variables (`SERVICE_URL_*`, `SERVICE_PASSWORD_*`), generated once and kept
  ([doc](https://coolify.io/docs/knowledge-base/docker/compose)).
- **Catalog**: about 388 templates, Apache-2.0.
- **Backups**: databases only ([doc](https://coolify.io/docs/databases/backups)).
- **MCP**: a server scoped by API token ([doc](https://coolify.io/docs/integrations/mcp)).
- **Advisories**: 70 security advisories ([GHSA](https://github.com/coollabsio/coolify/security/advisories)).

**Dokploy**
- **Format**: compose plus `template.toml`, with helpers such as `${password:32}`
  ([templates](https://github.com/Dokploy/templates)).
- **Catalog**: about 602 templates, MIT.
- **Hosts**: multi-node through Swarm ([doc](https://docs.dokploy.com/docs/core/cluster)).
- **MCP**: a server ([v0.29](https://dokploy.com/blog/v0-29-0-ai-powered-debugging-mcp-server-cli-shared-git-providers)).

**Others**
- **Unraid**: Community Applications, 5,424 apps, on a paid OS ([CA](https://ca.unraid.net/)).
- **TrueNAS**: one official catalog ([repo](https://github.com/truenas/apps)); app data backup isn't in the UI
  ([doc](https://apps.truenas.com/getting-started/app-storage/)).
- **Portainer**: templates ([format](https://docs.portainer.io/advanced/app-templates/format)).
- **Dockge**: no catalog ([repo](https://github.com/louislam/dockge)).
- **Sandstorm**: sign-in for the whole platform ([doc](https://docs.sandstorm.io/en/latest/developing/auth/)).
- **Homarr**: a dashboard only ([docs](https://homarr.dev/docs/getting-started/)).
- **Pangolin**: a tunnelled reverse proxy, not an app manager ([repo](https://github.com/fosrl/pangolin)).

## What nobody combines

1. A compose-native, Apache-2.0 manager on an existing distro.
2. A real identity provider with each app's sign-in declared in the app definition, and still changeable after
   install.
3. Private by default on a mesh, with no router ports, and public exposure opt-in per app.
4. Encrypted off-site backups with automated restore tests. None of the docs read automates restore testing.
5. Assistant-driven setup with guardrails. MCP itself is no longer unique: Umbrel, Coolify and Dokploy have one.

## Formats and importing

- **Native format**: compose with extension fields is the common direction
  ([compose extensions](https://docs.docker.com/reference/compose-file/extension/)).
- **Import sources by licence** (a reading of the licence files, not legal advice):
  - **Coolify** (Apache-2.0) and **Dokploy** (MIT): plain compose with variables, converted mechanically.
  - **CasaOS** (Apache-2.0): close in format, but publishes host ports and generates no secrets.
  - **Runtipi** (GPL-3.0): a separate GPL catalog at most.
  - **umbrel-apps**: no licence file, so not redistributable.
