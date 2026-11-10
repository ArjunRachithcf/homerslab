# HomersLab: spec

- **Status**: the definition, 2026-11-09. No code yet.
- **Vocabulary**: [GLOSSARY.md](../GLOSSARY.md).
- **Decisions**: [adr/](adr/).
- **Facts**: [research/](research/).

## Problem Statement

A household admin is the one technical person who runs self-hosted apps for a family. Each app arrives as its own
island:
- **Its own login, or none.** Partners and kids juggle passwords, or an app sits open on the network.
- **Its own way to be reached.** Reaching it from outside the house means a port on the router, or a tunnel provider
  that can read the traffic.
- **Its own backups, if any.** Nobody knows whether they restore until the day they're needed.
- **Its own updates.** Usually by hand, sometimes broken, with no way back.

Existing app managers each fall short on one side:
- **Polish and hundreds of apps** (Umbrel, Runtipi, CasaOS): a login wall instead of real sign-in, one machine, often
  LAN-only.
- **Real per-app sign-in and backups** (Cloudron, Olares): proprietary, public by default, or Kubernetes with a
  provider account in the middle.

Once a household picks one, it is locked into that interface.

HomersLab's predecessor, classic, proved a stricter model for one household:
- one sign-in with passkeys for every app;
- nothing on the router;
- TLS ending at home;
- encrypted off-site backups.

But every app took a dozen hand-edited files, and nothing could install, remove, update or restore an app.

## Solution

**Every app arrives wired.** The household admin installs an app from a catalog, and it arrives with:
- **sign-in**: the household account signs people in, through the app's own sign-in where it has one, otherwise
  guarded at the edge;
- **a name** on the household's mesh;
- **optional public access**, with TLS still ending at home;
- **backups** every hour, and a restore test every week;
- **updates** to tested versions, with a snapshot first and an automatic rollback;
- **kids' safety**: kids see only what's for them.

Before anything happens, the admin sees an install plan in plain words. Risky changes ask for the passkey again.

**On the household's own machines.** HomersLab runs on its own Linux machines: one primary, or a primary plus nodes
such as a NAS or a GPU box. It is private by default and never needs a router port.

**Two surfaces.**
- **Admin**: polished, for the household admin, over the mesh only.
- **Home**: for everyone; installs on phones; a desktop app too.

**Nothing locks the household in.**
- Every app can be ejected as a plain compose project with its data.
- Every backup restores by hand with standard tools.

**Priorities**: security, then polish, then breadth.

## User Stories

### First-time setup

1. As a prospective household admin, I want to install HomersLab on a Linux machine I already have from one signed
   package, so that I don't need a dedicated appliance.
2. As a prospective household admin, I want the installer to print a one-time link and a QR code that work only from
   this machine or my LAN for 30 minutes, so that nobody else can claim the machine during setup.
3. As a prospective household admin, I want to create my admin account with a passkey at the household's own sign-in
   address once my device is on the mesh, so that the strongest sign-in is there from the start, tied to the name I'll
   use every day.
4. As a prospective household admin, I want to choose the household's identity provider (Authentik or Kanidm) from a
   plain-language comparison, so that I pick what fits my hardware.
5. As a prospective household admin, I want to choose the mesh and give its token once, so that HomersLab joins
   machines and devices without sending me to a dashboard again. The choice is NetBird, Tailscale or Headscale, or
   NetBird together with one of the other two.
6. As a prospective household admin, I want to give my domain and its DNS provider's token once, or with Tailscale
   skip the domain, so that every app gets a name and a certificate automatically.
7. As a prospective household admin, I want to give a mail account once, so that invites, passkey resets and alerts
   can be sent.
8. As a prospective household admin, I want a recovery kit at the end of first-time setup, printable and ready for my
   password manager, so that I can restore everything even without HomersLab.
9. As a prospective household admin, I want the LAN listener to close for good at my first sign-in over the mesh, so
   that the LAN never sees the household's apps.
10. As a prospective household admin, I want to install onto another machine over SSH from the setup app and then
    continue in the browser, so that I can set up a headless machine from my laptop.
11. As a prospective household admin, I want first-time setup to show what's left and resume where I stopped, so that
    an interruption doesn't mean starting over.

### The catalog and installing

12. As a household admin, I want to browse a catalog with icons, screenshots, descriptions and categories, so that I
    find apps for my family.
13. As a household admin, I want to search by name and filter by category and tier, so that a large catalog stays
    easy to use.
14. As a household admin, I want each app to show its tier (Verified, Community, Third-party) and what that means, so
    that I know how deeply it is integrated.
15. As a household admin, I want an install plan in plain words before anything happens, so that nothing happens I
    didn't expect. It covers:
    - the app's name and who will see it;
    - how people will sign in;
    - what gets backed up;
    - which node it runs on;
    - any extras it asks for.
16. As a household admin, I want to approve extras (the machine's network, a device, a capability) with my passkey,
    so that powerful permissions are always a deliberate decision.
17. As a household admin, I want installing an app to need no visit to any other dashboard, so that adding an app
    takes a minute.
18. As a household admin, I want a warning when a node lacks the memory, disk or GPU an app needs, so that installs
    don't fail halfway.
19. As a household admin, I want HomersLab to suggest a node from the app's needs, and to choose another if I like,
    so that apps land where they work best.
20. As a household admin, I want to set an app's audience at install and change it later, so that each person sees
    what's for them. The audience can be admins, adults, kids or named people.
21. As a household admin, I want to make an app public with my passkey, seeing exactly what changes, so that someone
    outside the mesh can reach it on purpose.
22. As a household admin, I want to be told when public access isn't possible with my mesh or control plane, and why,
    so that I'm never surprised.
23. As a household admin, I want to add a third-party catalog by URL and explicitly trust its publisher's key, so
    that I can install apps the official catalog doesn't have.
24. As a household admin, I want HomersLab to refuse any app, from any catalog, that fails the floor, so that no
    catalog can weaken my household's security.
25. As a household admin, I want apps that ship only as a vendor's package, such as a media server's own repository,
    installed and looked after like the others, so that I'm not forced into a worse version of them. "Looked after"
    means updates, backups, health, and the edge in front.
26. As a household admin, I want apps to use an NVIDIA GPU or an Intel iGPU when the node has one, so that transcoding
    and local AI work.

### Using the apps

27. As a person, I want one household account for every app, so that I remember one sign-in.
28. As a person, I want to sign in with a passkey, so that I don't need a password at all.
29. As a person, I want Home to show only the apps I may open, with whether each one is working, so that I find
    things at a glance.
30. As a person, I want to install Home on my phone, so that it feels like an app.
31. As a person on a computer, I want the desktop family app on macOS, Windows or Linux, so that Home is one click
    away and alerts reach me.
32. As an adult, I want apps that support it to open without a second login, so that the household account is enough.
33. As a kid, I want to see only kid-safe apps, in their kid views, so that the household is safe for me without
    anyone setting it up by hand.
34. As an invited person, I want the invite to walk me through signing up and getting my phone and laptop onto the
    mesh, so that I don't need technical help.
35. As a person, I want to add a passkey for a new device myself, so that I don't depend on the admin.
36. As a person who lost their passkey, I want the household admin to reset it from Admin, so that I can get back in.
37. As a person, I want the interface in my language once a translation exists, so that the whole family can use it.

### People and devices

38. As a household admin, I want to invite people by mail or link and give each a role (admin, adult, kid), so that
    the household's accounts stay under my control.
39. As a household admin, I want to limit self sign-up to my household's mail domains, or turn it off, so that
    strangers can't join.
40. As a household admin, I want to manage people only in Admin, whichever identity provider I chose, so that I
    learn one place.
41. As a household admin, I want to see each person's devices with their role and revoke any of them, so that a lost
    phone is cut off in seconds.
42. As a household admin, I want removing a person to revoke their account and their devices together, so that leaving
    the household is clean.
43. As a household admin, I want admins to optionally sign in to the machines over SSH with their household account
    and keys managed in Admin with the passkey, so that machine access follows the household account.

### Nodes and storage

44. As a household admin, I want to add a machine as a node with a one-time join code, so that a NAS or a GPU box
    becomes part of the household.
45. As a household admin, I want to see each node's health, resources and apps, so that I know what runs where.
46. As a household admin, I want to move an app to another node, so that I can rebalance. Moving means a backup, then
    a restore on the other node.
47. As a household admin, I want removing a node to move or remove its apps first, so that retiring a machine is safe.
48. As a household admin, I want to add storage locations once, so that media apps on any node can read the library.
    A storage location is a local folder or a NAS share over the mesh, read-only by default for media.
49. As a household admin, I want nodes to be ordinary Linux machines on x86_64 or arm64, a Raspberry Pi 5 included,
    so that I can use what I have.
50. As a household admin, I want to use NetBird together with Tailscale or Headscale, with every node on each mesh in
    use, so that people can use whichever app they already have.

### Updates

51. As a household admin, I want to see "update available" with notes per app, so that I decide when.
52. As a household admin, I want to turn on automatic updates per app, so that low-risk apps stay current without me.
53. As a household admin, I want every update to take a snapshot first, and to roll back by itself if the app's
    health check fails, so that an update never leaves an app broken.
54. As a household admin, I want versions to come only from the catalog's tested pins, so that an upstream break
    doesn't reach my household first.
55. As a household admin, I want HomersLab itself to update the same way, from signed releases, so that the platform
    stays current safely.

### Backups and restore

56. As a household admin, I want every app's own data backed up every hour, deduplicated and encrypted, to a target I
    choose (disk, SFTP, S3, B2, or Proton Drive), so that a dead disk or a fire doesn't lose the family's photos. Big
    shared folders (films, music) are backed up only where I opt in.
57. As a household admin, I want a weekly write-only archive of the small, sensitive things, encrypted to a key that
    isn't on the machines, so that even someone who takes over a machine can't read it.
58. As a household admin, I want a restore test of every app every week, with the result in Admin, so that I know a
    restore works before I need one.
59. As a household admin, I want to restore one app to an earlier point, so that a mistake can be undone.
60. As a household admin, I want to restore the whole household onto new hardware from the recovery kit, so that a
    lost machine is recoverable.
61. As a household admin, I want to be able to restore by hand with standard tools and a one-page how-to, so that my
    family's data never depends on HomersLab existing.
62. As a household admin, I want a mail and a push notification when a backup or a restore test fails, so that
    failures are never silent.

### Removing, ejecting, leaving

63. As a household admin, I want removing an app to keep its data and backups, so that reinstalling brings it back as
    it was. Removing still stops the app and takes away its sign-in, names, public exposure and place on Home.
64. As a household admin, I want "delete data" as a separate action, where I type the app's name and confirm with my
    passkey, so that losing data is never an accident. Its backups and archive copies still expire on their own
    schedule.
65. As a household admin, I want to eject an app, so that it keeps running without HomersLab. Eject writes it out as
    a plain compose project with its data, settings and secrets, plus a ready edge snippet for a guarded app. Its plan
    says plainly that nobody can reach the app until an edge is in front of it again.
66. As a household admin of classic, I want an importer that moves my household in one sitting with a one-command
    rollback, so that moving is safe.
    - It reads a classic backup: volumes, databases, people and their groups.
    - It can be rehearsed in a VM.

### Security

67. As a household admin, I want risky changes to ask for my passkey every time, so that a stolen session can't do
    damage. Risky changes are deleting data, nodes, roles, sign-in settings, anything public, and extras.
68. As a household admin, I want Admin reachable only over the mesh, so that it is never on the internet.
69. As a household admin, I want Home unable to reach anything privileged, so that a flaw in the public-facing part
    can't become control of the household.
70. As a household admin, I want no router port ever, and TLS always ending on my machines, so that no provider can
    read the household's traffic.
71. As a household admin, I want every app behind the household account with no ports of its own, so that a
    vulnerable app can't be reached by strangers.
72. As a household admin, I want secrets sealed to the machine and handed only to the app that needs them, so that a
    copied disk doesn't leak them.
73. As a household admin, I want the LAN to see the machines but not the household's apps, so that a compromised
    device at home can't reach them.
74. As a household admin, I want signed releases and signed catalogs, so that I can verify what I install.
75. As a household admin, I want no telemetry, so that HomersLab never reports on my household.

### Assistants

76. As an AI assistant helping a household admin, I want an MCP server on the same API with a scoped token, so that I
    can plan installs, explain install plans, check health and read logs.
77. As a household admin, I want an assistant unable to make risky changes (they need my passkey) and never shown a
    secret, so that delegating to it is safe.

### Contributors and maintainers

78. As a catalog contributor, I want to add an app as one folder plus a command that checks it against the floor, so
    that contributing is fast. The folder holds a manifest, a plain compose file and icons.
79. As a catalog contributor, I want to convert a Coolify, Dokploy or CasaOS template into a Community app definition
    automatically, then review it, so that the catalog grows quickly.
80. As a catalog contributor, I want CI to install my app, sign in to it, update it, restore it and eject it, so that I
    know it meets its tier.
81. As a catalog contributor, I want to promote a Community app to Verified, so that integration deepens over time.
    Promotion means adding native sign-in and passing the restore and update tests.
82. As a third-party catalog publisher, I want to publish a signed catalog as a git repository, so that households
    can install my apps by trusting my key.
83. As a maintainer, I want one contract test suite per adapter kind, run against the real software, so that a new
    implementation is checked the same way as the first. The kinds are identity provider, mesh, DNS provider,
    container engine and backup target.
84. As a maintainer, I want CI to run against self-hosted control planes and identity providers in containers, so
    that tests need no external accounts.
85. As a maintainer, I want a bot to propose version bumps that CI then verifies, so that the catalog's pins stay
    current.
86. As a maintainer, I want a manual release checklist for what CI can't run (GPUs, cloud control planes, Funnel, a
    real phone joining), so that every release is proven end to end on a real household.

## Implementation Decisions

### Modules

- **Manager** (on the primary; unprivileged).
  - **One API**, used by Admin, the CLI and the MCP server. Its resources:
    - apps and install plans;
    - nodes, people and devices;
    - catalogs and storage locations;
    - backups, restore tests and jobs;
    - settings.
  - **The install planner**: turns an app definition and the household's state into an install plan.
  - **Desired state and history** in its database. The database holds no secrets.
  - **The scheduler** runs:
    - backups, the archive and restore tests;
    - update checks;
    - certificate renewals;
    - token rotation (NetBird service-user tokens last at most 365 days);
    - drift reports.
  - **The node authority**: join codes and agent certificates, for mTLS over the mesh.
  - **Certificates**: one wildcard certificate per household domain, from an ACME DNS challenge through libdns, handed
    to the edges. The DNS token never leaves the primary, and the household's app names never reach public
    Certificate Transparency logs.
- **Agent** (on every node; root). It owns:
  - **The container engine**: Docker, or rootful Podman through its Docker-compatible API, or rootless Podman with one
    unprivileged user per app.
  - **App units**: one systemd unit per app, running standard tools: the `docker compose` CLI pinned by the agent's
    package, or Quadlet units with Podman ([ADR 0014](adr/0014-app-units-run-standard-tools.md)). The agent uses the
    Compose SDK, pinned exactly, for status, health, logs and planning, in a project-name namespace reserved for
    HomersLab.
  - **Host apps**: the vendor's repository and package, per distro.
  - **The firewall table**: no published ports, mesh-only listeners, the LAN kept out, drift corrected.
  - **The mesh clients**: NetBird, and Tailscale's client, which also serves Headscale. Per-app tsnet nodes serve
    Headscale households, Tailscale households without a domain, and Funnel.
    - Router port mapping (UPnP, NAT-PMP, PCP) is switched off in every client: `NB_DISABLE_NAT_MAPPER=true`,
      `TS_DISABLE_PORTMAPPER=1`.
    - tsnet nodes upload no logs, reach their control plane over port 443 only, and are closed before they are
      deleted.
  - **The edge's configuration**: through Caddy's admin API on a Unix socket.
  - **Secrets**: systemd credentials, sealed by the agent with an explicit `host+tpm2` key, decrypted by the app's unit
    at start, and handed to its containers as files under the app's own SELinux MCS level
    ([ADR 0011](adr/0011-secrets-as-tpm-sealed-systemd-credentials.md)).
  - **Storage locations**: local folders, and network shares over the mesh.
  - **Health probes, backup and restore runs, and eject.**
- **Edge** (on every node; unprivileged Caddy 2.11.2 or later, with only the capability to bind low ports).
  - Per-app sites on the node's mesh addresses.
  - Forward-auth to the identity provider for guarded apps, after stripping identity headers that a client could
    forge, passing the sign-in's refreshed session cookie back to the browser.
  - A public listener that requires PROXY protocol v2, for NetBird's TLS passthrough.
  - Funnel traffic arrives through the agent's tsnet listeners.
- **Catalog**.
  - The app definition schema, and the loader.
  - Signature checks against the publisher keys the household trusts.
  - The floor checker, and tier labels.
  - The converter from Coolify, Dokploy and CasaOS templates to Community definitions.
- **Classic importer**.
  - It reads a classic backup and maps classic's components to catalog apps.
  - It restores volumes and database dumps, and brings over people and their groups.
  - It runs as a rehearsal or for real. Rolling back means starting classic again: classic stays installed, but
    stopped.
- **Surfaces**.
  - **Admin**: React with TypeScript, served by the manager, mesh only.
  - **Home**: a separate unprivileged process that reads only a projection of apps, audiences and health; a PWA.
  - **The desktop family app**: Tauri, on macOS, Windows and Linux; it signs in through the browser.
  - **The setup app**: Tauri; it installs over SSH, then hands over to first-time setup's link.
  - All strings come from translation files. English ships at 1.0. WCAG 2.2 AA.
- **CLI and MCP server**.
  - Both use the manager's API.
  - MCP tokens are scoped, and their use is audited.
  - No result ever carries a secret.
  - Risky changes need a person's step-up, so no token or assistant can make one.

### Adapter contracts

| Kind | Implementations | Must provide |
|---|---|---|
| Identity provider | Authentik; Kanidm with oauth2-proxy | See below |
| Mesh | NetBird (cloud or self-hosted); Tailscale (cloud); Headscale (self-hosted, with Tailscale's clients) | See below |
| DNS provider | libdns providers | Create and clean up ACME DNS challenge records |
| Container engine | Docker; Podman rootful; Podman rootless | Run, stop and inspect a compose project; logs and health; device and capability extras; GPUs (NVIDIA through CDI, Intel through its render devices) |
| Backup target | Disk, SFTP, S3, B2 (restic); Proton Drive (official CLI, as a copy target) | Store, list and prune; immutable where the target supports it |

**Identity providers** must provide:
- per-app OIDC clients, created, changed and deleted;
- a forward-auth guard per app and audience;
- LDAP access for apps that need it (with Kanidm, a person creates an app's LDAP password themselves);
- people: invite, role, passkey reset, removal;
- groups for roles and audiences;
- step-up, as a fresh passkey assertion bound to a plan;
- revocation that reaches the guard: removing a person, a role or an app also ends that app's guard sessions;
- admins who hold passkeys only, enforced by HomersLab where the provider doesn't;
- optional SSH sign-in on nodes: SSSD with Authentik, kanidm-unixd with Kanidm;
- a report of what it can do.

**Meshes** must provide:
- a key for joining a node;
- device invites and revocation, per person and role;
- per-app names that resolve to the right node: wildcard records, split DNS, or a per-app node;
- policies per role;
- router port mapping switched off in its clients;
- a public path, where the mesh has one: NetBird's reverse proxy in TLS passthrough, or Funnel;
- a report of what it can do, for example "no public path" on Headscale.

**The mesh implementations**, against that contract:

| | NetBird | Tailscale | Headscale |
|---|---|---|---|
| Control plane | Cloud, or self-hosted | Tailscale's cloud | The household's own server |
| A node joins with | A setup key, by API | A tagged, pre-approved auth key from an OAuth client | A pre-auth key with tags, by API |
| A person's device | A setup key where the app takes one, otherwise the mesh's sign-in | Signs in to the tailnet | Signs in with the household account (Headscale's OIDC login, against the household's identity provider) |
| Per-app names | Wildcard records in its DNS zones, by API | Split DNS to the primary's resolver, by API; without a domain, one tsnet node per app | Each app is its own tsnet node, `<app>.<domain>`, with Headscale's base domain set to the household domain once in first-time setup; a device learns only the names its person may open |
| Policies | By API | By API (the policy file) | By API (the policy kept in its database) |
| Public path | Its reverse proxy, TLS passthrough, own domain | Funnel, on `*.ts.net` | None |
| Domain | Required | Optional | Required |

- **Where self-hosted control planes run.** Self-hosted NetBird and Headscale run on a server reachable from the
  internet, outside the household's machines, because HomersLab never opens a router port. HomersLab reaches them by
  URL and API key.
- **Combining meshes.** A household may use NetBird together with Tailscale or Headscale, but only one of Tailscale
  and Headscale at a time.
- **Headscale is held to the contract.** It passes the same mesh contract suite as the others, and CI runs it in a
  container.

**The sign-in method.** An app lists its sign-in methods in order of preference, and the manager uses the first one
the chosen identity provider provides. Every provider provides all three (OIDC, LDAP, guarded), so no app depends on
the provider.

### The app definition

One folder per app: a manifest, a plain compose file (none for a host app), icons and screenshots. The manifest holds:
- **identity**: id, name, description, category;
- **versions**: image digests per engine and architecture;
- **sign-in methods**, in order of preference, with their settings;
- **names and exposure**: the default exposure, and whether a public path is allowed;
- **audience**: the default audience, and the kid view;
- **data to back up**: paths, and database dumps through declared commands;
- **secrets to generate**, and the UID that reads them;
- **health checks**;
- **resources**: memory, disk, GPU;
- **extras**;
- **the host-app recipe**: the vendor's repository and package, per distro;
- **update notes**;
- **tier**.

### The floor

The manager refuses an app definition unless:
- it publishes no ports;
- it is not privileged;
- the machine's network, devices and capabilities appear only as declared extras;
- images are pinned by digest;
- every persistent path is declared as data;
- every secret is declared and generated, with no default values;
- a sign-in method and a health check are declared;
- its CI install test passes.

### The install plan

- A plan is one object, shown the same way in Admin, the CLI and the MCP server. It holds:
  - what gets created: the names, the sign-in client, the audience, a public service;
  - the placement;
  - each extra, in plain words;
  - what is backed up, and when;
  - the resources check.
- A plan is immutable.
- Applying takes the plan's id, plus a step-up when the plan holds a risky change.
- Updates, removals, ejects, moves and deleting data are plans too.

### Updates, removal, eject

- **Updates.**
  - Each catalog release pins versions; a bot proposes bumps.
  - CI installs, signs in, updates and restores every verified app.
  - The admin applies an update, or opts in per app.
  - An update runs snapshot → update → health check → rollback if unhealthy.
  - HomersLab itself and the platform's own parts (identity provider, edge, mesh clients) update the same way, from
    signed releases.
- **Remove.** Keeps the data and backups.
- **Delete data.** Typing the app's name, plus a step-up. Its backups and archive copies expire on their own schedule.
- **Eject.** Hands over the compose project, its secrets as `0600` files, its data and settings, and for a guarded app
  a ready edge snippet. HomersLab's edge stops serving the app, and the plan says nobody can reach it until an edge is
  in front of it again. The app's sign-in client stays while the household keeps its identity provider
  ([ADR 0012](adr/0012-every-app-can-be-ejected.md)).
- **Restore to an earlier point.** A plan that takes a snapshot first.

### Names and certificates

- **With a domain**, every app is `<name>.<domain>` on the mesh, resolving to its node.
  - NetBird: wildcard records in its DNS zones, by API.
  - Tailscale: split DNS to a resolver on the primary, by API.
  - Headscale: its base domain is the household domain, set once in first-time setup. Every app is its own tsnet node,
    so a device learns only the names its person may open
    ([Headscale spike](research/2026-11-09-spike-headscale-tsnet.md)).
- **Certificates**: one wildcard certificate per household domain, issued on the primary and handed to the edges over
  the agents' mTLS. The household's app names never appear in public Certificate Transparency logs.
- **Headscale always needs a domain.** Its support for `tailscale cert` was merged into its main branch on 2026-10-18
  but isn't in a release yet (v0.29.7). Even then it would need a DNS provider for the household's domain.
- **On Tailscale without a domain**, every app is its own tagged tsnet node, named `<name>.<tailnet>.ts.net`, with
  Tailscale's per-node certificate, which is public in the CT logs. The free plan allows 50 tagged devices, so this is
  chosen over Tailscale Services, which allows 10.

### First-time setup in two halves

- **The one-time link** works on the machine or its LAN for 30 minutes. It covers everything up to "mesh connected,
  certificate issued": the household, the identity provider, the mesh and its token, the domain and its DNS token,
  mail, and the recovery kit.
- **Then the admin joins this device to the mesh** (a QR code) and finishes at the sign-in's own address, where the
  admin's passkey is created. The passkey is bound to the name the household will use every day.
- **The LAN listener closes for good** at the admin's first sign-in over the mesh.
- **Headscale households** also make one guided change in Headscale's configuration: its base domain.

### Security defaults

- No router ports, ever, and no router port mapping by the mesh clients.
- TLS ends on the household's machines.
- The LAN sees the machines, not the apps.
- Admin is reachable over the mesh only.
- Secrets travel only inward: a front end may send one, and no response carries one.
- Signed releases, an SBOM, image digests pinned in the catalog, signed catalogs.
- No telemetry.
- Notifications by mail always, by ntfy where the household has one (an external server or the catalog's ntfy app),
  and in the desktop app.

### Packaging

- Signed packages per distro (deb, rpm, pacman), for x86_64 and arm64.
- One binary. On the primary it runs as two processes: the manager, unprivileged, and the agent, as root.

## Testing Decisions

- **What makes a good test**: it tests external behaviour through the highest seam and nothing internal. The seams
  are:
  - the manager's API (an install plan in, the household's state out);
  - the agent's API;
  - the adapter contracts.
- **Contract suites**, one per adapter kind, run against the real software in containers:
  - Authentik, and Kanidm with oauth2-proxy;
  - Headscale and self-hosted NetBird;
  - Docker, rootful Podman and rootless Podman;
  - a test ACME CA with a test DNS server;
  - restic targets: local, SFTP and S3-compatible.
- **The floor checker**: table tests over app definitions, one passing and one failing case per rule.
- **The converter**: golden tests, a template in and a definition out.
- **Every verified app in CI**: install → sign in through a headless browser → update → restore test → eject → run the
  ejected project.
- **The edge**: a request matrix for every app's names: who asks what, through which listener, and which answer they
  get. Prior art: classic's request matrix, a real Caddy with stub upstreams and a stub identity provider.
- **Golden renders**: what HomersLab writes for an app (compose project, edge configuration, policies) is compared
  byte for byte with a committed baseline. A deliberate change updates the baseline and explains the diff. Prior art:
  classic's golden tests.
- **Restore by hand**: CI follows the recovery kit's how-to, using only plain restic and age.
- **Compose SDK upgrades** pass an API diff, the engine contract suite and a golden test of config hashes.
- **Distros and architectures**: package install tests in containers and VMs.
- **Surfaces**:
  - flow tests on the key flows: first-time setup, install with a plan, invite, add a node, restore;
  - accessibility checks to WCAG 2.2 AA;
  - the prototypes' flows become the tests' scripts.
- **Test isolation** (from classic):
  - a test that stubs a container engine points it at nowhere;
  - tests get their own home directory;
  - nothing touches a live household.
- **The manual release checklist**, on the maintainer's household: GPUs, cloud control planes, Funnel, and a real phone
  joining the mesh.

## Out of Scope

- An appliance OS image.
- A PaaS for deploying your own code.
- Kubernetes or Swarm.
- macOS or Windows nodes.
- Several households on one HomersLab.
- Native mobile apps: phones use Home as a PWA.
- Public paths that end TLS anywhere but the household's machines, such as Cloudflare Tunnel.
- An identity provider of our own.
- Router port forwarding or UPnP.
- Telemetry.
- High availability or failover of the primary.
- Ejecting the whole platform at once. Eject is per app.

## Further Notes

### Releases and the move

- **Releases**: there is no release before 1.0. 1.0 is the whole matrix, tested in CI, plus the manual checklist.
- **Dogfooding**: the maintainer's household moves before 1.0, as soon as its own path works: one distro, one engine,
  one identity provider, its meshes, its GPUs and its DNS provider.
- **The move**: the importer is rehearsed in a VM until clean, then run in one sitting. Classic stays installed but
  stopped.
- **Classic** takes fixes only, and is archived after the move.
- **The maintainer's private apps** go into a private signed catalog, the first third-party catalog.
- **The repository** is public from its first commit. The README says "not ready" until 1.0.
- **The meshes are built in this order:** NetBird, then Headscale (which runs in CI), then Tailscale's cloud (its API
  adapter and Funnel, tested against a recorded stand-in and the manual release checklist).
- **1.0's catalog:** every app the maintainer's household runs today is Verified before the move. Converter output
  that passes review joins as Community.

### Design first

- First, clickable throwaway prototypes of the key flows, tried with a family member:
  - first-time setup;
  - the catalog;
  - an install plan;
  - an app's page;
  - people and invites;
  - nodes;
  - backups and restore.
- Then a design system: tokens, components, light and dark, mobile.
- Then the backend, built against it.

### Spikes

Run on 2026-11-09, each in throwaway containers on the maintainer's machine. The findings and code are in
[research/](research/) and `spikes/`.

| Spike | Answer | Still open |
|---|---|---|
| [NetBird API](research/2026-11-09-spike-netbird-api.md) | Every operation works by API on self-hosted NetBird, including the TLS passthrough public path with PROXY v2 | The cloud (the first token needs one dashboard visit; shared clusters' TLS ports) |
| [systemd credentials](research/2026-11-09-spike-systemd-creds.md) | Works for Docker, Compose and rootless Podman, with per-app SELinux MCS levels and explicit `host+tpm2` sealing | The root tests, in a VM |
| [Kanidm + oauth2-proxy](research/2026-11-09-spike-kanidm-forward-auth.md) | Meets the contract by API; guards need the session cookie passed back | Revocation that reaches the guard; LDAP is partial |
| [Compose SDK](research/2026-11-09-spike-compose-sdk.md) | Works on Docker and rootless Podman, v5.5.1 and v5.7.0 | Rootful Podman; the GPU on rootless Podman (SELinux) |
| [Headscale + tsnet](research/2026-11-09-spike-headscale-tsnet.md) | Keys and policy by API, 100 tsnet nodes in one process, names that follow the policy | Headscale's OIDC login; `tailscale cert` in a release |
| A Tailscale OAuth client from a one-time token | Not run | Needs a real tailnet |

### Name

**HomersLab**, after Homer's Odyssey, the long journey home. The logo draws on it: a ship or a lyre.

Checked on 2026-11-09:
- **GitHub and npm**: no repository or package named homerslab.
- **Domains**: no DNS for .dev, .org, .io or .app; .com is taken.
- **Collision**: Homer, a popular self-hosted dashboard, shares the word.
- **Trademarks** (TMview, Swissreg, USPTO): no HomersLab mark anywhere, live or dead. Similar HOMER software marks
  exist; the maintainer plans no registration and no commercial use.

### Traceability

Every answer of the grilling (2026-11-09) and where it lives:

| Q | Decision | Where |
|---|---|---|
| 1 | The household admin is the audience | Problem Statement; stories 1–11 |
| 2 | The full lifecycle, wired | Solution |
| 3 | A rewrite: new code, classic's ideas | ADR 0001 |
| 4 | Multi-node | ADR 0003 |
| 5 | A new repository; classic takes fixes only | Further Notes |
| 6 | Go | ADR 0002 |
| 7 | A primary plus optional nodes, placement | ADR 0003; stories 44–49 |
| 8 | Pluggable identity provider from day one | ADR 0004 |
| 9 | NetBird and Tailscale at launch | ADR 0005 |
| 10 | A web app on the primary; CLI and MCP on the same API | ADR 0013; stories 76–77 |
| 11 | Zero clicks per app | ADR 0005; story 17 |
| 12 | An edge on every node | ADR 0006 |
| 13 | Data on its node; storage locations | ADR 0003; story 48 |
| 14 | The agent owns the host; the manager's scheduler | ADR 0007 |
| 15 | First-time setup by a one-time local link | Stories 2–9 |
| 16 | restic plus a weekly write-only archive | ADR 0010 |
| 17 | Catalog pins; the admin applies; snapshot and rollback | Stories 51–55 |
| 18 | Remove keeps data | Stories 63–64 |
| 19 | Admin, adult, kid; audiences | Stories 20, 38–39; glossary |
| 20 | DNS providers, backup targets, NVIDIA and Intel, Docker and Podman | ADRs 0005, 0008, 0010; Adapter contracts |
| 21 | Two surfaces | ADR 0013 |
| 22 | React | ADR 0002 |
| 23 | Host apps allowed | ADR 0008; story 25 |
| 24 | Restore by hand with standard tools | ADR 0010; story 61 |
| 25 | Secrets in the manager's database | Replaced by Q62 (ADR 0011) |
| 26 | Step-up for risky changes | Story 67; glossary |
| 27 | Podman rootless too | ADR 0008 |
| 28 | Host apps only as vendor packages | ADR 0008 |
| 29 | Everything in 1.0 | Further Notes |
| 30 | The move: rehearsed, one sitting, rollback | Story 66; Further Notes |
| 31 | Linux only | ADR 0007; story 49 |
| 32 | Public from day one | Further Notes |
| 33 | Dogfooding before 1.0 | Further Notes |
| 34 | A new name | Further Notes |
| 35 | Apache-2.0 | LICENSE |
| 36 | Build the glue | ADR 0001 |
| 37 | Security > polish > breadth | Solution |
| 38 | Tailscale's public path: Funnel on ts.net | ADR 0005; story 22 |
| 39 | Kanidm as the second identity provider | ADR 0004 |
| 40 | People managed in Admin | Story 40 |
| 41 | Contract: OIDC, forward-auth, LDAP | ADR 0004; Adapter contracts |
| 42 | One floor, two tiers | ADR 0009 |
| 43 | Signed third-party catalogs | ADR 0009; stories 23, 82 |
| 44 | A manifest plus plain compose | ADR 0009; The app definition |
| 45 | HomersLab; the Odyssey | Further Notes |
| 46 | This repository, local first | Further Notes |
| 47 | Prototypes before code | Further Notes |
| 48 | English, i18n-ready | Modules (Surfaces); story 37 |
| 49 | Extras declared and approved | ADR 0009; story 16 |
| 50 | Cloud and self-hosted control planes | ADR 0005 |
| 51 | Both meshes at once | ADR 0005; story 50 |
| 52 | Keep a desktop family app | Modules (Surfaces); story 31 |
| 53 | Every node joins every mesh in use | ADR 0005 |
| 54 | Keep the setup app for SSH installs | Story 10 |
| 55 | Desktop app plus the PWA on phones | Stories 30–31 |
| 56 | A domain is optional with Tailscale | ADR 0005; Names and certificates |
| 57 | A guided invite per device | Stories 34, 41 |
| 58 | Eject per app | ADR 0012; story 65 |
| 59 | SSH sign-in, optional, off by default | Story 43; Adapter contracts |
| 60 | A private catalog for the maintainer's own apps | Further Notes |
| 61 | Eject is the way out; the platform stays HomersLab's | ADR 0012 |
| 62 | Secrets as TPM-sealed systemd credentials | ADR 0011 |
| 63 | Headscale named in the mesh contract (added after the grilling) | ADR 0005; Adapter contracts |
| 64 | First-time setup in two halves; the passkey at the sign-in's own address | First-time setup in two halves; stories 3, 9 |
| 65 | One wildcard certificate per household domain | ADR 0006; Names and certificates |
| 66 | App units run standard tools; the SDK reads and plans | ADR 0014; Modules (Agent) |
| 67 | Eject hands over everything, an edge snippet included | ADR 0012; story 65 |
| 68 | Headscale's base domain is the household domain | ADR 0005; Names and certificates |
| 69 | Hourly backups; storage locations opt-in | ADR 0010; story 56 |
| 70 | Meshes built NetBird, then Headscale, then Tailscale | ADR 0005; Further Notes |
| 71 | 1.0's catalog: the maintainer's apps, Verified | Further Notes |
