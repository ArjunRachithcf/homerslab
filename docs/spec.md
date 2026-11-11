# HomersLab: spec

- **Status**: the definition, 2026-11-09. Code since 2026-11-11, from the first slice (#14). Nothing is released before 1.0.
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
26a. As a household admin, I want to choose which app serves a purpose, or install several apps of the same kind (two
    photo apps, or Jellyfin and Plex side by side), so that each person can use the one they prefer.

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
57. As a household admin, I want a write-only archive of the small, sensitive things, made on change and at least
    weekly, encrypted to a key that isn't on the machines, so that even someone who takes over a machine can't read it.
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
    damage. The glossary's **Risky change** lists them. The archive's key, backup targets, domains, DNS tokens, the
    mail account and API or MCP tokens joined that list, and sign-in settings were defined, as decided in #85.
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
87. As a person, I want to ask for an app the household doesn't have, or describe what I need in my own words, so that
    the household admin knows what the family wants.
88. As a household admin, I want app requests in the Store, merged per app, to approve through the normal install plan
    or turn down with a reason, so that requests never install anything by themselves.
89. As a household admin, I want Admin in its dark look by default, with Light and System as choices, so that it
    matches Home's dark look.
90. As a person, I want to set how see-through the glass is (Clear, Balanced, Frosted, Solid), so that the look suits
    my eyes and my screen.
91. As a catalog contributor, I want a feedstock for an app whose sign-in can only be fixed by a patch, built and
    signed by CI, so that the app can still reach the Verified tier.
92. As a household admin, I want an upstream security release offered at once, guarded at the edge, when HomersLab's
    sign-in patch for that app isn't ready yet, so that security fixes never wait on a patch.
93. As a household admin, I want HomersLab to create the public DNS records it needs and touch nothing else in my zone,
    so that making an app public needs no visit to my DNS provider and my mail keeps working.
94. As a household admin, I want to add more domains that every app answers on, so that family members can use the
    address they know.
95. As a person, I want to sign in once with one passkey whichever domain I used, so that more domains never mean
    more passkeys.
96. As a household admin, I want to rename the household's main domain through a guided migration, so that a new
    domain doesn't mean starting over.
97. As a household admin, I want to see every disk's health, temperature and use (per app and per storage location),
    with "full in N days", so that I replace or extend a disk before it fails or fills.
98. As a household admin, I want to use an existing filesystem as app-data space or a storage location without
    touching what's on it, so that a disk I already have just works.
99. As a household admin, I want to prepare a new disk from Admin, encrypted and unlocked by the TPM, as btrfs, so
    that a stolen disk is unreadable and nobody types a passphrase at boot.
100. As a household admin, I want disks holding app data mirrored by default, and to choose for media disks, so that
     one dead disk doesn't stop the household.
101. As a household admin, I want a guided replacement when a mirrored disk fails, showing which disk to pull, so that
     the apps stay up while I fix it.
102. As a household admin, I want to move an app's data to another disk or node with a verified switch and a
     7-day way back, so that rebalancing is safe.
103. As a household admin, I want apps that can wait to pause when a disk is nearly full, while backups and sign-in
     keep running, so that one app can't take the household down.
104. As a household admin, I want to encrypt the system disk in place through a rehearsed, guided migration, so that
     the whole machine is protected without reinstalling.
105. As a household admin, I want a disk analyzer (a space map, lists sorted by size and age, the biggest and oldest
     files, duplicates, space per person), so that I find what fills a disk.
106. As a household admin, I want to delete what I find in storage locations and HomersLab's leftovers, with my passkey
     for every delete and an app's own cleanup inside its data, so that freeing space never breaks an app.
107. As a household admin, I want deleted items kept for 7 days, so that a wrong delete can be undone.
108. As a household admin, I want to be told when the primary is down, even when it can't tell me itself, so that a
     power cut or a crash never goes unnoticed.
109. As a household admin, I want a year of uptime, response times and incidents per app, node and the public path,
     so that I see what keeps failing.
110. As a household admin, I want alerts for nodes, the mesh, the public path, expiring certificates and tokens,
     security events and resource pressure, so that I act before the family notices.
111. As a household admin, I want one alert per incident, a "resolved", quiet hours with only critical alerts, and a
     morning digest, so that alerts stay worth reading.
112. As a household admin, I want to choose my own alert channels per level, with mail always on for critical, so
     that nothing depends on one channel.
113. As a household admin, I want planned work to run in a nightly maintenance window and show "updating" on Home, so
     that the family isn't surprised.
114. As a person, I want Home to tell me plainly when an app isn't working and that the admin knows, so that I don't
     have to ask.

### People leaving and visiting

Stories 115–119 are decided in #86.

115. As a household admin, I want removing a person to end their sessions in every app that can, and the plan to name
     each app that can't, so that someone who has left can't keep opening a public app.
116. As a household admin, I want removing a person to list what they own in each app, keep it under their name, and
     offer them a download first, so that nothing of theirs is lost by accident.
117. As a household admin, I want to invite a guest until an end date, to named apps only, so that a visitor leaves on
     their own.
118. As an adult or a kid, I want to add a new device of mine to the mesh from another of my devices, with my
     passkey, so that a broken phone doesn't wait for the admin.
119. As a household admin, I want a role change to reach every app's kid view by the person's next sign-in, so that a
     kid growing up needs no per-app settings.

### Getting back in, and losing a node

Stories 120–121 are decided in #87.

120. As the only household admin, I want two passkeys from the start and a way back in from the machine's terminal,
     so that losing my passkey doesn't lock the household out.
121. As a household admin, I want to restore the sign-in from its backups at the machine's terminal, so that a corrupt
     sign-in doesn't need the sign-in to fix it.
122. As a household admin, I want to replace the DNS token at the machine's terminal, so that an expired certificate
     doesn't lock me out of the place that fixes it.
123. As a household admin, I want one plan that cuts off a lost or stolen node and replaces everything it held, so that
     a stolen machine opens nothing in the household.

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
  - **The firewall table**: no published ports, mesh-only listeners, the LAN kept out, drift corrected. Also
    ([ADR 0020](adr/0020-only-the-edge-reaches-an-app.md); decided in #82):
    - **only the edge reaches an app**. The agent, not the compose file, wires each app's one upstream: a bridge
      address on Docker and rootful Podman, a loopback port on rootless Podman. New connections to it are accepted
      only from the edge's user, and from the agent's, for its health probes;
    - **apps reach the internet, and nothing else**. New connections from apps to the LAN, the mesh, other apps'
      networks and the node's own addresses (except the engine's DNS resolver) are dropped, unless an extra ("reaches
      the LAN", "reaches the mesh") says so;
    - **the sign-in is the one exception, decided here, unlike app-to-app calls**. Apps with native sign-in call it
      from their server (OIDC's token, userinfo and keys; LDAP binds). The edge can't tell apps apart by source
      address (rootless Podman, apps on other nodes, a desktop primary), so apps reach it through a listener of their
      own:
      - the **sign-in listener** has its own address on each node, on the household's port (ADR 0021), and carries
        only the sign-in's route. SNI and `Host` must both equal `auth.<main domain>` (`Host` compared without its port);
      - the agent makes `auth.<main domain>` resolve to that address inside app containers;
      - on the primary it serves the sign-in; on other nodes, the node's own edge forwards it to the primary;
      - an app whose sign-in method is LDAP also reaches the same address on port 636 (LDAPS), which leads to the
        identity provider's LDAP outpost: directly on the primary, through the node's own edge on other nodes;
      - the firewall lets app networks reach that listener, and its LDAP port for LDAP apps, and nothing else on the
        node;
    - open: how one app uses another household app's API, such as a local AI model. The implementing ticket asks the
      maintainer before building it.
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
  - Per-app sites on the node's mesh addresses, at the household's port
    ([ADR 0021](adr/0021-one-url-per-name.md)). A name that isn't on this node gets 421 Misdirected Request, so a
    browser that reuses a connection for two apps opens a new one (#81).
  - Forward-auth to the identity provider for guarded apps, after stripping identity headers that a client could
    forge, passing the sign-in's refreshed session cookie back to the browser.
  - **Apps are same-site with Admin and the sign-in**, so the edge keeps their cookies apart (#83):
    - it strips the guard's own cookie from the request before proxying to the app;
    - it drops any `Set-Cookie` in an app's response whose `Domain` is a household domain, unless the app's manifest
      declares that cookie and the plan says so;
    - with Authentik, every guarded app has its own provider (`forward_single`), never one for the whole domain.
  - A public listener that requires PROXY protocol v2, for NetBird's TLS passthrough (#81):
    - it holds only the routes of this node's public apps, and Home's when Home is public. Admin and mesh-only apps
      are absent from it, not merely refused;
    - while anything is public, the primary's public listener also holds `auth.<main domain>`, because public
      visitors sign in there too. The public-path plan lists the sign-in's service;
    - a `Host` that differs from the connection's SNI gets 421, and so does a name this node doesn't serve publicly.
  - Funnel traffic arrives through the agent's tsnet listeners.
  - **No private key is readable through the edge** (#78):
    - it runs with Caddy's autosave off (`admin.config.persist: false`), and the agent pushes the whole
      configuration again whenever the edge starts;
    - certificates load from files that the edge's unit receives as systemd credentials sealed by the agent, never
      inline, so the admin API never shows a key;
    - the admin socket sits in a directory only the edge's user and root can enter;
    - on a node without a TPM, Admin says that the node's copy of the certificate is only as safe as its disk.
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
  - **Admin and Home refuse cross-site changes**, because every app is same-site with them (#83):
    - a state-changing request needs `Sec-Fetch-Site: same-origin`, or the header absent, for token-authenticated CLI
      calls;
    - a cookie-authenticated one also needs an exact `Origin`;
    - session cookies are `__Host-` prefixed.
  - **The desktop family app**: Tauri, on macOS, Windows and Linux; it signs in through the browser.
  - **The setup app**: Tauri; it installs over SSH, then hands over to first-time setup's link.
  - All strings come from translation files. English ships at 1.0. WCAG 2.2 AA.
- **CLI and MCP server**.
  - Both use the manager's API.
  - MCP tokens are scoped, and their use is audited.
  - No result ever carries a secret.
  - Risky changes need a person's step-up, so no token or assistant can make one.
  - **No token applies an invite, an audience change or a removal** (decided in #85).
    They stay one click for an admin signed in to Admin. An MCP token's apply scope covers updates, and installs
    without extras, and nothing else.

### Adapter contracts

| Kind | Implementations | Must provide |
|---|---|---|
| Identity provider | Authentik; Kanidm with oauth2-proxy | See below |
| Mesh | NetBird (cloud or self-hosted); Tailscale (cloud); Headscale (self-hosted, with Tailscale's clients) | See below |
| DNS provider | libdns providers | Create, update and delete only records HomersLab owns (ACME challenges, the public path's CNAMEs and validation), as the manager's database and the inventory record list them; check through the provider's API listing that a name is free; never touch other records |
| Container engine | Docker; Podman rootful; Podman rootless | Run, stop and inspect a compose project; logs and health; device and capability extras; GPUs (NVIDIA through CDI, Intel through its render devices) |
| Backup target | Disk, SFTP, S3, B2 (restic); Proton Drive (official CLI, as a copy target) | Store, list and prune; immutable where the target supports it |

**Identity providers** must provide:
- per-app OIDC clients, created, changed and deleted;
- a forward-auth guard per app and audience;
- LDAP access for apps that need it (with Kanidm, a person creates an app's LDAP password themselves);
- people: invite (with an optional end date, for guests), role, passkey reset, removal;
- groups for roles and audiences;
- step-up, as a fresh passkey assertion bound to a plan;
- passkeys with the RP ID `auth.<main domain>`, never the bare domain, and an exact origin check (#83);
- revocation that reaches the guard: removing a person, a role or an app also ends that app's guard sessions;
- passkeys that require user verification, at enrolment and at every assertion (#84);
- no first-comer admin (#84):
  - the provider's own first-admin flow can't apply to anyone before the sign-in is served. For Authentik, `akadmin`
    gets a random password and is deactivated, because blocking the flow's page at the edge isn't enough: the flow
    executor API runs it too;
  - the first admin's passkey comes only from the manager's one-time enrolment link;
- enrolment and recovery links that run a HomersLab-owned flow with only a passkey-enrolment stage, never the
  provider's own recovery flow (#84, #87);
- admins who hold passkeys only, enforced by HomersLab with either provider (#84):
  - admins sign in, step-up included, only through a passkey-only authentication flow, so a password or second
    factor that a stolen session adds can't sign them in;
  - the provider's own password, second-factor and recovery flows refuse admins;
  - before accepting a step-up, HomersLab also asks the provider for the admin's credentials. Anything but passkeys
    refuses the step-up, raises a security event, and says the way out (`homerslab admin recover`, or another
    admin);
  - an admin's app-scoped LDAP credentials (Kanidm's per-app passwords; the LDAP bind's own credentials with
    Authentik) are exempt from that check. They are never accepted by the sign-in flow, and Admin lists them among
    the admin's credentials (#104);
- optional SSH sign-in on nodes: SSSD with Authentik, kanidm-unixd with Kanidm;
- a report of what it can do.

**Meshes** must provide:
- a key for joining a node, and removing one: its peers deleted and its keys revoked (#91);
- device invites and revocation, per person and role, including a one-off key a person makes for their own device
  (#86);
- per-app names that resolve to the right node: wildcard records, split DNS, or a per-app node;
- policies per role, and a group of their own for guests (#86);
- router port mapping switched off in its clients;
- a public path, where the mesh has one: NetBird's reverse proxy in TLS passthrough, or Funnel;
- a report of what it can do, for example "no public path" on Headscale, or the TLS ports NetBird's cloud cluster
  accepts, found by trying during first-time setup (#92).

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
- **names and exposure**: the default exposure, whether a public path is allowed, and a cookie for the whole domain
  where the app truly needs one (#83);
- **audience**: the default audience, and the kid view;
- **data to back up**: paths, and database dumps through declared commands;
- **secrets to generate**, and the UID that reads them;
- **health checks**;
- **resources**: memory, disk, GPU, and whether the app can pause when a disk is nearly full;
- **extras**;
- **the host-app recipe**: the vendor's repository and package, per distro;
- **update notes**;
- **tier**;
- **people**, for a Verified app: how to end one person's sessions and disable their account in it, and how they
  export what they own, where the app can (decided in #86).

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

### Catalogs

Decided in #89. Details are in the amended
[ADR 0009](adr/0009-catalog-floor-and-tiers.md).
- **No rollback, no freeze.** A catalog's signed metadata carries a version that only goes up, and an expiry 30 days
  out.
  - The publisher signs it again on a schedule (weekly), even when nothing changed, as TUF's timestamp role does,
    with a separate timestamp key. That key may only sign the version, the expiry and the hash of content the
    publisher's key signed.
  - The manager refuses a version lower than the last one it accepted.
  - Admin warns as the expiry nears. After it, Admin says "This catalog hasn't been updated since …"; installed apps
    keep running, and new installs from that catalog wait.
- **Each catalog acts only on its own apps.**
  - App ids are scoped to their catalog.
  - An installed app updates only from the catalog and publisher key it came from.
  - A catalog's declared image signers apply to its own apps only.

### The install plan

- A plan is one object, shown the same way in Admin, the CLI and the MCP server. It holds:
  - what gets created: the names, the sign-in client, the audience, a public service;
  - the catalog and publisher, beside the tier (decided in #89);
  - the placement;
  - each extra, in plain words;
  - what is backed up, and when;
  - the resources check.
- A plan is immutable.
- A plan records who made it (a person, the CLI or a named token), and its page shows that before the passkey:
  "Prepared by the assistant token *Claude*" (#85).
- Applying takes the plan's id, plus a step-up when the plan holds a risky change.
- Updates, removals, ejects, moves and deleting data are plans too.

### Updates, removal, eject

- **Updates.**
  - Each catalog release pins versions; a bot proposes bumps.
  - **Upstream releases wait, unless they are security releases** (decided in #89):
    - in the official catalog, a pin bump goes through CI and the independent review like any change, and the loop
      may merge it (the maintainer, 2026-11-11, #89: pins are not security core);
    - a new upstream version is offered three days after its release, unless the catalog marks it a security release;
    - where an upstream signs its images, the catalog pins the expected signer, and the agent checks it as it does a
      feedstock's.
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

### Backups and the archive

Details are in [ADR 0010](adr/0010-backups-restic-and-a-write-only-archive.md), amended for #88 and #90.
- **The platform's own state is backed up hourly**, with the apps: the identity provider's database and the manager's
  (#88).
- **The archive also runs on change** (#88). Every secret created or rotated, every change to the sign-in's people or
  clients, and every change to the manager's desired state starts an archive run, once things have been quiet for 10
  minutes, and at most 30 minutes later. The weekly run stays as the floor.
- **Overlapping rotation**: a rotated token's predecessor is revoked only after an archive that holds its successor
  has been written (#88).
- **The restore plan** compares each app's last backup with the archive's secrets. When an app's secrets are older than
  its data, it says so before anything happens (#88).
- **A TPM that no longer opens the secrets** (#88): at boot, a credential that fails to decrypt is reported once, as
  "this machine's TPM no longer opens HomersLab's secrets", in the machine's terminal and in an alert when one can
  still be sent, with the restore-from-archive command.
- **One restic repository per node** (decided in #90), at the same target, each with
  its own key and, where the target allows it, credentials scoped to its own path or bucket.
  - A node holds only its own repository's key. The primary holds them all, for restore tests and moves.
  - The recovery kit lists each repository and key, so adding a node asks for a new kit.
  - Moving an app between nodes restores from the source node's repository, then backs up into the target node's.

### Names and certificates

- **With a domain**, every app is `<name>.<domain>` on the mesh, resolving to its node.
  - NetBird: wildcard records in its DNS zones, by API.
  - Tailscale: split DNS to a resolver on the primary, by API.
  - Headscale: its base domain is the household domain, set once in first-time setup. Every app is its own tsnet node,
    so a device learns only the names its person may open
    ([Headscale spike](research/2026-11-09-spike-headscale-tsnet.md)).
- **One URL per name**: `https://<name>.<domain>[:<port>]`, with the household's port chosen once in first-time
  setup's mesh step ([ADR 0021](adr/0021-one-url-per-name.md); #92).
  - 443 when the mesh's public path can use it (self-hosted NetBird), or when the household's meshes have no public
    path (Headscale alone).
  - NetBird's cloud: the TLS port its cluster accepts, even before anything is public, so the first public app changes
    no URL. The mesh step finds it (#103):
    - a throwaway TLS passthrough service with a random, recognisable name (`homerslab-probe-<random>`), under the
      cluster's own domain, points at a probe listener on the primary that serves nothing;
    - it tries the candidate ports in order (443, 8443, 9443), and the first that reaches the probe listener wins;
    - the service is removed at once, and leftovers are swept by name at every run and in drift checks;
    - when the probe can't run (the household's plan has no reverse proxy), or no candidate port works, the port is 443, and Admin says that
      going public later is a guided migration.
  - Tailscale's Funnel serves only `*.ts.net` names: it is the public path only for Tailscale households without a
    domain. A household with a domain goes public through NetBird, so the sign-in and the passkeys' RP ID stay at
    `auth.<main domain>` (#102).
  - The edge serves every site on the mesh at that port too. Every URL HomersLab writes uses it: base URLs, redirect
    addresses, the sign-in's origin, mails, invites and Home.
  - Changing it later is a guided migration, like renaming the main domain.
- **Certificates**: one wildcard certificate per household domain, issued on the primary and handed to the edges over
  the agents' mTLS. The household's app names never appear in public Certificate Transparency logs.
- **Renewal** starts when a third of the certificate's lifetime is left, read from the certificate itself. The first
  failed renewal raises a warning, before the 14-day alert (#80).
- **The DNS challenge** (#79):
  - the solver asks public resolvers, never the system's, which on the primary may answer from the mesh's own zone;
  - its propagation check asks the zone's authoritative servers for the exact TXT record, and never follows a CNAME,
    because the public wildcard also answers for `_acme-challenge.<domain>`.
- **CAA**: the domain step reads the zone's CAA records, and says plainly when the chosen CA isn't allowed (#80).
- **Restores and rehearsals** (#80):
  - the certificate, its key and the ACME account key are in the archive;
  - a restore puts back a certificate that is still valid, and issues a new one only when it must, or when the old
    machine was lost or stolen (see A lost or stolen node);
  - importer rehearsals and CI use the CA's staging endpoint, never production, so they never use up its limits.
- **Headscale always needs a domain.** Its support for `tailscale cert` was merged into its main branch on 2026-10-18
  but isn't in a release yet (v0.29.7). Even then it would need a DNS provider for the household's domain.
- **On Tailscale without a domain**, every app is its own tagged tsnet node, named `<name>.<tailnet>.ts.net`, with
  Tailscale's per-node certificate, which is public in the CT logs. The free plan allows 50 tagged devices, so this is
  chosen over Tailscale Services, which allows 10.

### Domains

- **Records HomersLab writes**, through the DNS token ([ADR 0016](adr/0016-domains-own-records-equal-domains-one-sign-in.md)):
  - the certificate challenges;
  - when the first app goes public through NetBird's proxy, its wildcard CNAME and validation record, shown in the
    plan and removed when nothing is public any more;
  - where the zone already holds a `*` record HomersLab didn't create, a CNAME per public app instead. The plan says
    why in plain words ("your domain already sends every name to …") (#79).
- **Only its own records** (#79):
  - the manager's database is the source of truth for the records HomersLab created: provider ID, name, type and
    value;
  - one inventory record, a `_homerslab.<domain>` TXT, lists them by name and type, so a restored primary finds them
    again. Nothing is ever written beside a CNAME;
  - other records (MX, SPF, DKIM, DMARC, the apex) are never edited or deleted;
  - a name already taken by someone else's record is refused with a reason. Whether a name is free is read from the
    provider's API listing, never from DNS, where the public wildcard answers every name.
- **More domains**: Admin's settings add them, each with its own DNS token and wildcard certificate. Every app answers
  on every domain, with no redirect.
- **One sign-in domain**: the identity provider lives on the main domain only, so one passkey works everywhere. Each
  app's OIDC client lists one address per domain, and each guard sets its cookie on its own app's name, never on the
  whole domain (#83).
- **Renaming the main domain**: a guided migration (a plan with step-up).
  - A new certificate; every app re-pointed, and every sign-in client updated.
  - The old domain keeps answering for 90 days.
  - Everyone adds a passkey on the new address.
  - It can be rehearsed and rolled back.

### Disks

Details are in [ADR 0017](adr/0017-disks-btrfs-encrypted-mirrored.md).

- **Watching**: SMART health and temperature, use per app and per storage location, growth, "full in N days", and
  scrub results, with alerts by mail, ntfy and on Home.
- **Adopting**: an existing filesystem or folder becomes app-data space or a storage location, untouched.
- **Preparing**: an empty disk can be prepared with the passkey.
  - It's encrypted with LUKS2, unlocked by the TPM, with its recovery key in the recovery kit.
  - It's formatted as btrfs, which gives snapshots, reflinks, checksums with scheduled scrubs, compression and
    mirrors.
  - A disk with data needs an "Erase this disk" plan: the passkey plus its serial, typed.
  - The system disk and disks in use are never offered.
- **Redundancy**:
  - app data is mirrored by default when there are two or more disks;
  - media disks ask: mirror or single;
  - a failed disk is replaced in a guided flow, with the apps kept up.
- **Moving an app's data**: a plan that snapshots, copies, verifies and switches, keeping the old copy for 7 days with
  rollback.
- **Low space**: warnings at 80% and 90%. At 95%, apps declared pausable pause, and backups and sign-in keep running.
  Per-app size limits are optional.
- **The system disk**: an in-place encryption migration, offered and rehearsable. It needs a VM dry run and a
  verified full copy first.
- **The disk analyzer** (admins only):
  - **Views**: a space map; lists sorted by size, age, type and owner; the biggest and oldest files; duplicates, which
    can be freed or shared as reflinks; space per person.
  - **The passkey**: to open it (a session of up to 10 minutes), and again for every delete, bound to the exact items.
  - **Where it may delete**: storage locations and HomersLab's leftovers. Inside an app's data, that app's own cleanup.
  - **Undo**: "Recently deleted" keeps items for 7 days, through a btrfs snapshot.
- **The agent's contract** gains disks, LUKS with TPM enrolment, btrfs operations and SMART. Every write is a plan,
  and the destructive ones need step-up.

### Uptime and notifications

Details are in [ADR 0018](adr/0018-uptime-and-notifications.md).

- **The primary down**:
  - an outside check (healthchecks.io or any compatible URL) is pinged every 5 minutes and alerts when the pings stop;
  - nodes also watch the primary over the mesh.
- **History**: a year of uptime, response times and an incident log per app, per node and for the public path.
- **What alerts admins**, besides backups, restore tests, disks, rolled-back updates and requests:
  - nodes, mesh clients and the public path;
  - expiries, 14 and 3 days ahead, when renewal hasn't already worked;
  - security events;
  - resource pressure.
- **Behaviour**:
  - one alert per incident, then "resolved", with a reminder every 4 hours;
  - quiet hours per admin (22:00–07:00): only critical alerts push, the rest waits for a morning digest.
- **Channels**: chosen per admin and per level: mail, ntfy, the desktop app, or a webhook. Mail is always on for
  critical.
- **The maintenance window**: nightly, 02:00–05:00 by default. Updates, scrubs, restore tests and moves run in it,
  silencing their own alerts and showing "updating" on Home.
- **The family** hears on Home only. There is no status page.

### Getting back in

When the usual way in is what broke, the household admin gets back in from the primary's own terminal.
- **Terminal commands** run as root on the primary, over the manager's control socket. Root on the primary can already
  do anything, so they grant nothing new.
  - `homerslab dns token` replaces the DNS token, read from stdin as in first-time setup, for when an expired
    certificate keeps everyone out of Admin (#80). From Admin, the same change is a risky change.
  - `homerslab admin recover <person>` prints a one-time link that enrols a new passkey for that admin, at the
    sign-in's own address over the mesh. The admin may have no device left on the mesh, so it also prints a one-off
    mesh key and QR code for one device of theirs, as first-time setup's second half does. Both last 30 minutes and
    work once, and the command raises a security event (decided in #87).
  - `homerslab idp restore --list` and `--to <time>` restore the identity provider from the hourly backups, with no
    browser. The command says whose passkeys, added since that point, are lost (recommended in #87; the maintainer may
    overrule).
- **Two ways in for the first admin**: first-time setup finishes only when the first admin has two passkeys (say, the
  phone plus a laptop or a security key), or a second admin has joined with a passkey (decided in #87).
- **The mesh's control plane down** (decided in #87):
  - there is no LAN fallback for apps ([ADR 0005](adr/0005-three-meshes-one-contract-tls-ends-at-home.md));
  - alerts name the cause ("NetBird's control plane hasn't answered for 20 minutes"), and a node that loses the
    primary says when the mesh may be the cause, rather than reporting the primary down;
  - for a self-hosted control plane, the recovery kit holds what's needed to reach the server again: its address, the
    owner's sign-in, and how to restore it.
- **Mail down as well**: nothing more; each admin's choice of channels already covers it (ADR 0018).

### People leaving and visiting

Decided in #86.
- **Removing a person ends their sessions in every app.** The app definition says how, for each Verified app, and the
  removal runs it in every app. For an app that can't, the plan says so in plain words: "Their Immich session on
  their phone keeps working until they sign out; Immich can't end it".
- **What they leave behind.** The removal plan lists, per app, what the person owns.
  - It is kept by default, under their name, marked "left the household".
  - Before the account goes, they're offered a download from each app that can export.
  - Deleting it is a separate plan, with step-up.
- **Guests.** An invite may carry an end date. On that date the guest is removed as above.
  - A guest is in no role's group, and has their own group on the mesh.
  - Their audience is named apps only, never a role's, and the invite chooses whether they get the kid views.
  - A guest can't be an admin, and can't add devices.
- **A new device, for adults and kids.** A person adds one of their own devices, with another device of theirs.
  - Home links to the manager's device page: its own origin, mesh only, reachable by people's own devices, with a
    step-up at the sign-in. The request never passes through Home's process
    ([ADR 0013](adr/0013-two-surfaces.md), amended).
  - The mesh gives a one-off key for their own role's groups, and the admins are told.
  - At most five devices per person; admins can turn this off.
- **Role changes** reach every app's kid view by the person's next sign-in at the latest.

### A lost or stolen node

A node that is lost, stolen or dead can't be removed in the usual way, because nothing can be moved off it first
(#91). Admin offers "This node is lost or stolen" for an offline node: a plan with step-up that needs nothing from
the node. It:
- revokes the node's agent certificate, which the manager refuses from then on;
- deletes its peers on every mesh in use, and revokes its keys;
- revokes every household domain's wildcard certificate at the CA, and issues it again with a new key, which reaches
  the remaining edges before the plan finishes;
- rotates every credential the node held that lives elsewhere: its storage locations' share credentials, its apps'
  OIDC client secrets, its backup repository's key and target credentials;
- restores each of its apps onto another node from its latest backup, or marks it "lost with the node", as chosen in
  the plan;
- says what a thief could read: whether its disks were encrypted and TPM-sealed, and which apps' data it held;
- raises a security event, and the incident log records what was rotated.

**A lost or stolen primary.** Restoring onto new hardware asks "was the old primary lost or stolen?". If it was, the
restore plan also revokes and reissues the certificates, and replaces everything the primary held:
- the node authority: a new one, with every node enrolled again;
- the old primary's mesh peers, deleted, and every outstanding setup key revoked. The recovery kit says how to delete
  the peer from the mesh's dashboard at once, before new hardware exists;
- the DNS token and the mesh tokens;
- the mail account's password;
- every node's restic key, the backup and archive targets' credentials, and Proton Drive's session;
- the sign-in: every app's OIDC client secret, the identity provider's keys (for Authentik, its secret key and outpost
  tokens), and every session ended;
- every API and MCP token, and the manager's own API token to the identity provider;
- the guards' cookie secrets (Authentik's per-provider proxy cookie secret; oauth2-proxy's for Kanidm), so no one can
  forge a guard session;
- every outstanding invite, enrolment link and recovery link held in the restored sign-in database;
- the ACME account: deactivated and registered anew, because a valid authorization can outlive the old DNS token for
  up to 30 days;
- the storage locations' share credentials, the notification channels' credentials (ntfy, webhooks) and the outside
  heartbeat's URL, so a thief can't keep the heartbeat green.

The plan also lists the people, credentials and tokens created after a chosen time, and offers an earlier restore
point. Nothing needs replacing for:
- the archive's age key, because only its public key was on the machine;
- the edge's admin socket, which exists only on the machine;
- passkeys, because the identity provider holds only their public keys.

### First-time setup in two halves

- **The installer** prints the command `sudo homerslab setup link`, never the link itself: package managers keep a
  post-install script's output in readable logs (#74).
- **The LAN** is the private networks and the on-link global IPv6 prefixes of the interfaces that carry the default
  route, plus IPv6 link-local on those interfaces; never container bridges, VPNs or the mesh (#14 Q5, #73).
- **The one-time link** works on the machine or its LAN for 30 minutes. It covers everything up to "mesh connected,
  certificate issued": the household, the identity provider, the mesh and its token, the domain and its DNS token,
  mail, and the recovery kit.
- **Then the admin joins this device to the mesh** (a QR code) and finishes at the sign-in's own address, where the
  admin's passkey is created. The passkey is bound to the name the household will use every day.
  - It comes from a one-time enrolment link for the admin's account, which the manager mints (30 minutes, once) and
    the claimed setup session hands over, as a QR code beside the mesh one. No other path creates the first admin
    (#84).
- **The LAN listener closes for good** at the admin's first sign-in over the mesh.
- **Headscale households** also make one guided change in Headscale's configuration: its base domain.
- **The first half travels over plain HTTP on the LAN**, including the mesh token, the DNS token and the mail
  password. The threat model accepts this, because the window is narrow:
  - the link works only from this machine or the LAN, for 30 minutes, and once;
  - one claimer holds it, and a new link ends every earlier link and session;
  - a claim whose answer was lost is answered again only for the tab that sent it: the page sends a random nonce with
    the key, the manager keeps its hash, and for 60 seconds the same key, nonce and address get the same session back,
    with a new token, until that session is first used (#101);
  - the listener answers only the machine's own names and addresses;
  - the listener closes for good after the hand-off to the mesh.

  A self-signed certificate was considered and refused: browser warnings teach the admin to click through them.

  This is temporary. **HTTPS is enforced eventually** (the maintainer, 2026-11-11; #64):
  - setup's secrets are typed in the machine's terminal, so plain HTTP carries only non-secret answers;
  - once the certificate exists, every name answers HTTPS only, with HSTS and `Secure` cookies.
- **The setup session is a bearer token**, not a cookie:
  - the page keeps it in `sessionStorage`, so one tab holds it;
  - it is bound to the address that claimed the link, ends after 30 minutes idle and 2 hours at most, and is stored
    only as a hash;
  - browsers send a cookie to every port on the same host, so another local service could replay one.

### Security defaults

- No router ports, ever, and no router port mapping by the mesh clients.
- TLS ends on the household's machines.
- HTTPS only once the certificate exists, with HSTS; no secret ever crosses plain HTTP (#64).
- The LAN sees the machines, not the apps.
- Only the edge reaches an app, and apps reach only the internet unless an extra says so (decided in #82).
- Admin is reachable over the mesh only.
- Secrets travel only inward: a front end may send one, and no response carries one.
- Signed releases, an SBOM, image digests pinned in the catalog, signed catalogs.
- No telemetry.
- Notifications by mail always, by ntfy where the household has one (an external server or the catalog's ntfy app),
  and in the desktop app.

### Packaging

- Signed packages per distro (deb, rpm, pacman), for x86_64 and arm64.
- One binary. On the primary it runs as two processes: the manager, unprivileged, and the agent, as root.

### Feedstocks

- **When**: a feedstock exists only when an app can't reach Verified sign-in through its own settings, OIDC, LDAP or
  the edge's guard ([ADR 0015](adr/0015-feedstocks-for-sign-in-patches.md)).
- **Upstream first**: every patch is also offered upstream, and is dropped once merged.
- **Where**: one public repository, with a folder per app holding the recipe, patches and tests.
- **What it builds**:
  - images built in CI from source plus patches, each with an SBOM;
  - signed keyless with Sigstore, the signature naming the workflow;
  - the catalog pins the digest and the expected signer, and the agent refuses anything else.
- **Upstream releases**: a bot opens a pull request for each one, and CI rebases, builds, runs the app's sign-in and
  restore tests, and signs.
- **Security releases never wait**: when the patch doesn't apply, the release ships with the app guarded at the edge,
  and the install plan says so.
- **Third-party catalogs** may declare their own patched builds and signers.
- **Licences**: patches carry the app's own licence; the tooling is Apache-2.0.
- **Shown** in Admin's details of an installed app, not in the Store.

## Testing Decisions

- **What makes a good test**: it tests external behaviour through the highest seam and nothing internal. The seams
  are:
  - the manager's API (an install plan in, the household's state out);
  - the agent's API;
  - the adapter contracts.
- **Contract suites**, one per adapter kind, run against the real software in containers:
  - Authentik, and Kanidm with oauth2-proxy, including the RP ID read from the provider (#83), and the first-admin
    flow, the enrolment link, admins' credentials and user verification (#84);
  - Headscale and self-hosted NetBird;
  - Docker, rootful Podman and rootless Podman;
  - a test ACME CA with a test DNS server, including:
    - issuance on a primary that resolves the household's domain through a mesh zone with no SOA, beside a public `*`
      CNAME (#79);
    - with a fake clock, a renewal that fails, the terminal command replacing the DNS token, and a restore that issues
      nothing (#80);
  - restic targets: local, SFTP and S3-compatible.
- **The floor checker**: table tests over app definitions, one passing and one failing case per rule.
- **The converter**: golden tests, a template in and a definition out.
- **Every verified app in CI**: install → sign in through a headless browser → update → restore test → eject → run the
  ejected project.
- **The edge**: a request matrix for every app's names: who asks what, through which listener, and which answer they
  get. Prior art: classic's request matrix, a real Caddy with stub upstreams and a stub identity provider. Its rows
  include:
  - through the PROXY v2 listener, a public app's SNI with the `Host` of a mesh-only app, of Admin, or of a public app
    on another node → 421; SNI equal to `Host` → served (#81);
  - a cross-app `POST` to Admin → refused; an app's response that sets a cookie on the household domain → stripped
    (#83);
  - a local unprivileged process that connects to an app's upstream with a forged identity header → refused before
    the app sees anything (#82);
  - Authentik's first-admin flow, at `/if/flow/initial-setup/` and through the flow executor API
    (`/api/v3/flows/executor/initial-setup/`) → refused on every listener (#84);
  - the sign-in listener: SNI `auth.<main domain>` with Admin's `Host` → refused; an app on rootless Podman, and an
    app on another node, reach the sign-in through it → served (#82);
  - the primary's public listener with something public: SNI and `Host` `auth.<main domain>` → the sign-in; with
    nothing public → refused (#81).
- **The firewall**: namespace tests in which a container tries the LAN, the mesh and its node's own addresses, and is
  refused unless its extras allow it (#82).
- **Golden renders**: what HomersLab writes for an app (compose project, edge configuration, policies) is compared
  byte for byte with a committed baseline. A deliberate change updates the baseline and explains the diff. Prior art:
  classic's golden tests.
  - The edge's configuration has autosave off and no inline key, and a test finds no file under the edge's state
    directories that holds a private key (#78).
- **Claims each implementing ticket tests first**, before building on them. They are reported, not verified:
  - Caddy answers a request that no route matches with an empty response, not an error (#81);
  - the DNS challenge's zone lookup on the primary reaches the mesh's local zone, which has no SOA (#79);
  - Kanidm's sign-in takes a single origin, which holds with one URL per name, a port other than 443 included;
    Authentik derives the origin from the request (#92);
  - Authentik's WebAuthn stages don't require user verification unless told to, so a touch alone could approve a
    risky change (#84);
  - NetBird's cloud lets HomersLab create a TLS passthrough service on a name under the cluster's own domain, and
    remove it, by API, so the mesh step can find the cluster's port (#92);
  - container DNS resolves `auth.<main domain>` to the sign-in listener on hosts running systemd-resolved (#82).
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
- **No secret in any result**: checked against every secret HomersLab generated or holds, in plain, base64 and
  URL-encoded forms, across every tool's output, logs included (#85).
- **People and nodes**: role changes reach kid views, tested in both directions (#86). A lost node: a two-node CI
  job kills one node, applies the plan, then tries every old credential, and the test CA refuses the old certificate's
  serial (#91).
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

### Design decisions (round 1, 2026-11-10)

From the review of the first prototypes and logo concepts:
- **Logo**: C, "Harbour H": an H whose crossbar is a hull, with a sail rising inside it.
- **Look**: classic's own design language.
  - **Accent**: classic's teal (`#1B6E66`, `#5FB3A9` in dark mode), "teal blue". Clay was dropped: on cream it is the
    look of a well-known AI assistant's brand.
  - **Style**: Apple's system style from classic's home page: the system font (SF), inset grouped lists, widgets,
    frosted glass, and spring animations.
- **The install plan** is its own page, with an address that carries the plan's id.
- **One passkey per plan**: a single step-up at Install approves the whole plan, extras included, and any change makes a
  new plan.
- **Risky changes** also include restoring an app, eject, trusting a catalog and making a join code. Updates, audience
  changes, Remove, revoking a device and inviting an adult or a kid don't ask for the passkey.
- **A kid's Home** has its own layout: bigger tiles that say what each app is for.
- **Away from the mesh**, Home dims the apps that need it ("Needs the mesh") and offers "How to connect".
- **Apps on Home** show their product names with their own icons, which each app definition ships.
- **Defaults**:
  - the recovery kit can't be made in a browser running on the primary itself;
  - a failed first health check undoes the whole install.

### Design decisions (round 2, 2026-11-10)

From the interactive review of the reworked prototypes:
- **Admin names apps by product** (Immich, with "Photos" beside it). HomersLab is an app store: a household picks which
  app serves a purpose, or installs several apps of one kind side by side (story 26a).
- **Two more risky changes**: replacing a mesh token, and making a new recovery kit.
- **An admin's Home** carries one more fact, "something needs a look in Admin", whenever something does.
- **iOS green** marks apps that work and tests that pass.
- **Motion**: Home rises on entry, while Admin moves only in answer to the person.
- **More glass and more motion**, in the manner of iOS and macOS: translucent materials, depth, springy transitions.
  Contrast rules and the person's reduce-motion and reduce-transparency settings still apply.
- **First-time setup** becomes lean and friendly:
  - one question per screen, in plain words;
  - sensible defaults chosen for the person;
  - details behind "More options".

### Design decisions (round 3, 2026-11-10)

- **Admin is dark by default**, in Home's dark look: true black, the wallpaper's glow, glass panels. Admin's settings
  offer Dark, Light and System. Home follows the device.
- **The glass is adjustable**: Clear, Balanced (the default), Frosted, and Solid. People set it per person, in Admin's
  settings and Home's person menu. The device's reduce-transparency setting always wins, and text keeps AA contrast at
  every level.
- **Light glass is truly translucent**, over a light wallpaper with colour. Bars tint with what scrolls under them.
- **The Store** replaces "install an app". It has a featured app, collections, categories and search; app pages have
  Get, Open and Update; and Updates and Requests sit as tabs. It's named "Store", not "App Store" (Apple's
  trademark).
  - **Get** still opens the install plan, and one passkey approves it.
  - **Catalogs you trust** live in the Store's settings.
- **App requests** (stories 87–88):
  - people ask from Home, for a catalog app or in their own words;
  - kids see only kid-suitable apps;
  - admins approve through the install plan, or answer "Not now" with a reason;
  - requests for the same app merge;
  - the answer reaches the person on Home, and by mail or ntfy.

### Design decisions (round 4 answers, 2026-11-10)

- **No wallpaper choice**: every household gets HomersLab's own wallpaper.
- **Hiding an app**: where a household has two apps for one purpose, each person may hide the one they don't use, on
  their own Home only.
- **Setup defaults confirmed**:
  - NetBird alone;
  - the domain taken from the admin's mail address;
  - the DNS provider guessed from the domain's name servers.
- **Admin's motion confirmed**: screens push and pop, and icons fly to their page, always in answer to a click.

### The catalog's app waves (2026-11-11)

- **The order**, the maintainer's:
  - wave 0, Immich and BentoPDF, which prove the two install paths;
  - the move's daily essentials (#118);
  - the rest of the classic box's apps (#119);
  - family apps (#120);
  - downloads, opt-in (#121);
  - apps beyond classic (#123);
  - a second app for every classic purpose (#127);
  - the maintainer's own apps in his private catalog (#122).
- **Two apps for every purpose**, classic's included, so that each household chooses (the maintainer, 2026-11-11;
  #127 pairs classic's apps, #123 the purposes beyond classic). A household may install either or both, and each person
  may hide the one they don't use. Where no credible open-source second app exists, the Store says so instead.

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
| 16 | restic plus a write-only archive, on change and at least weekly | ADR 0010 (amended for #88) |
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
| 72 | Logo: Harbour H | Design decisions |
| 73 | Teal and classic's Apple-style look | Design decisions |
| 74 | The install plan is its own page | Design decisions |
| 75 | One passkey per plan | Design decisions |
| 76 | Risky changes plus four | Design decisions; story 67; glossary |
| 77 | A kid's Home has its own layout | Design decisions |
| 78 | Away from the mesh, Home dims mesh-only apps | Design decisions |
| 79 | Product names and their own icons on Home | Design decisions |
| 80 | Admin names apps by product; several apps of one kind may be installed | Design decisions (round 2); story 26a |
| 81 | Replacing a mesh token and making a new recovery kit are risky | Design decisions (round 2); story 67; glossary |
| 82 | An admin's Home says when something needs a look | Design decisions (round 2) |
| 83 | iOS green for what's fine | Design decisions (round 2) |
| 84 | Home rises on entry; Admin moves only on input | Design decisions (round 2) |
| 85 | More iOS/macOS glass and motion | Design decisions (round 2) |
| 86 | A lean, friendly first-time setup | Design decisions (round 2) |
| 87 | Admin dark by default | Design decisions (round 3); story 89 |
| 88 | Adjustable glass: Clear, Balanced, Frosted, Solid | Design decisions (round 3); story 90 |
| 89 | Translucent light glass; bars tint with content | Design decisions (round 3) |
| 90 | The Store instead of "install an app" | Design decisions (round 3); glossary |
| 91 | App requests from members, approved by admins | Design decisions (round 3); stories 87–88; glossary |
| 92 | Feedstocks only as a last resort, upstream first | ADR 0015; Feedstocks |
| 93 | One feedstocks repository, a folder per app | ADR 0015 |
| 94 | Signed images (Sigstore, tied to the workflow) | ADR 0015; Feedstocks |
| 95 | Security releases ship guarded when a patch lags | ADR 0015; story 92 |
| 96 | Third-party catalogs may declare patched builds | ADR 0015 |
| 97 | Patches under each app's licence | ADR 0015 |
| 98 | HomersLab builds shown in Admin's details only | ADR 0015 |
| 99 | A bot and CI keep feedstocks current | ADR 0015; story 91 |
| 100 | HomersLab writes the public path's DNS records itself | ADR 0016; Domains; story 93 |
| 101 | Only its own DNS records, marked; never others | ADR 0016; Adapter contracts; story 93 |
| 102 | Every domain equal for the apps | ADR 0016; Domains; story 94 |
| 103 | One sign-in domain, one passkey everywhere | ADR 0016; Domains; story 95 |
| 104 | Renaming the main domain is a guided migration | ADR 0016; Domains; story 96 |
| 105 | Admin sees and watches every disk | ADR 0017; Disks; story 97 |
| 106 | Adopt existing filesystems untouched | ADR 0017; story 98 |
| 107 | Prepare empty disks from Admin | ADR 0017; story 99 |
| 108 | Pools and mirrors, with guided replacement | ADR 0017; stories 100–101 |
| 109 | Prepared disks always LUKS2, TPM-unlocked | ADR 0017; story 99 |
| 110 | btrfs for prepared disks | ADR 0017 |
| 111 | App data mirrored by default; media asked | ADR 0017; story 100 |
| 112 | Erasing: empty disks freely, others by plan with the serial typed | ADR 0017 |
| 113 | Moving data: snapshot, copy, verify, switch, 7 days back | ADR 0017; story 102 |
| 114 | Low space: warn, then pause what can wait | ADR 0017; story 103 |
| 115 | In-place system-disk encryption offered | ADR 0017; story 104 |
| 116 | Disk analyzer: map, sorted lists, large/old files, duplicates, per person | ADR 0017; Disks; story 105 |
| 117 | It deletes only in safe places; apps' own cleanup inside their data | ADR 0017; story 106 |
| 118 | Passkey to open the analyzer and for every delete | ADR 0017; story 106 |
| 119 | Recently deleted, 7 days | ADR 0017; story 107 |
| 120 | An outside heartbeat, and nodes watching the primary | ADR 0018; story 108 |
| 121 | Uptime history and incidents, kept a year | ADR 0018; story 109 |
| 122 | Alerts for nodes, mesh, public path, expiries, security, resources | ADR 0018; story 110 |
| 123 | The family hears about outages on Home only | ADR 0018; story 114 |
| 124 | Grouped alerts, quiet hours, digest | ADR 0018; story 111 |
| 125 | A nightly maintenance window | ADR 0018; story 113; glossary |
| 126 | Each admin chooses alert channels; mail for critical | ADR 0018; story 112 |
| 127 | No status page; Home is the status | ADR 0018 |
| 128 | No wallpaper choice | Design decisions (round 4 answers) |
| 129 | A person may hide one of two same-purpose apps on their Home | Design decisions (round 4 answers) |
| 130 | Setup defaults confirmed | Design decisions (round 4 answers) |
| 131 | Admin's motion confirmed | Design decisions (round 4 answers) |
| 132 | Setup's first half over plain HTTP on the LAN, accepted | First-time setup in two halves (#14 Q3) |
| 133 | The setup session is a per-tab bearer token, not a cookie | First-time setup in two halves (#14 Q12–Q13) |
| 134 | The manager's database is SQLite | ADR 0019 (#14 Q4) |
| 135 | HTTPS enforced eventually; no secret over plain HTTP | Security defaults; First-time setup in two halves (#64) |
| 136 | No private key readable through the edge: no autosave, certificates as credential files, a closed admin socket | Modules (Edge); Testing Decisions (#78) |
| 137 | DNS ownership kept in the manager's database and one inventory record; nothing beside a CNAME | ADR 0016 (amended); Domains; Adapter contracts (#79) |
| 138 | The DNS challenge asks public resolvers and the authoritative servers, never following a CNAME | Names and certificates; Testing Decisions, claims to test first (#79) |
| 139 | Free names read from the provider's API; an existing `*` record means a CNAME per public app | ADR 0016 (amended); Domains (#79) |
| 140 | Renewal at a third of the lifetime left, a warning at the first failure; CAA read before issuing | Names and certificates (#80) |
| 141 | The certificate and ACME account in the archive; restores and rehearsals issue nothing they needn't | Names and certificates; Testing Decisions (#80) |
| 142 | The DNS token replaced from the machine's terminal | Getting back in; story 122 (#80) |
| 143 | The public listener holds only public routes; 421 when `Host` and SNI differ, or for a name not on the node | Modules (Edge); Testing Decisions, claims to test first (#81) |
| 144 | Admin and Home refuse cross-site changes; `__Host-` session cookies | Modules (Surfaces); Testing Decisions (#83) |
| 145 | The edge strips the guard's cookie and apps' domain cookies; a guard provider per app | ADR 0016 (amended); Modules (Edge); Domains; The app definition (#83) |
| 146 | Passkeys' RP ID is the sign-in's own name | Adapter contracts (#83) |
| 147 | One URL per name, its port chosen in first-time setup | ADR 0021; Names and certificates; Adapter contracts; Testing Decisions, claims to test first (#92) |
| 148 | Only the edge reaches an app, by the edge's user in the firewall (decided) | ADR 0020; ADR 0006 (amended); Modules (Agent); Security defaults; Testing Decisions (#82) |
| 149 | Apps reach only the internet, never the LAN, the mesh, other apps' networks or the node; the LAN and the mesh are extras (decided) | ADR 0020; Modules (Agent); Security defaults; Testing Decisions (#82) |
| 150 | No first-comer admin; the first passkey from a one-time enrolment link | Adapter contracts; First-time setup in two halves; Testing Decisions (#84) |
| 151 | Admins hold passkeys only with either provider; step-up refused when an admin holds anything else | ADR 0004 (amended); Adapter contracts; Testing Decisions (#84) |
| 152 | User verification required for every passkey | Adapter contracts; Testing Decisions, claims to test first (#84) |
| 153 | Six more risky changes; sign-in settings defined (decided) | Glossary; story 67 (#85) |
| 154 | No token applies invites, audience changes or removals (decided) | Modules (CLI and MCP server) (#85) |
| 155 | A plan names who made it; secrets looked for in every encoding | The install plan; Testing Decisions (#85) |
| 156 | Removing a person ends their sessions in every app, or the plan names the apps that can't (decided) | People leaving and visiting; The app definition; story 115 (#86) |
| 157 | A removed person's data kept under their name, offered for download first (decided) | People leaving and visiting; story 116 (#86) |
| 158 | Guests: an invite with an end date, named apps only (decided) | People leaving and visiting; Adapter contracts; story 117; glossary (#86) |
| 159 | A person adds their own device from Home with their passkey (decided) | People leaving and visiting; Adapter contracts; story 118 (#86) |
| 160 | Role changes reach kid views by the next sign-in (decided) | People leaving and visiting; Testing Decisions; story 119 (#86) |
| 161 | Two passkeys or two admins before first-time setup finishes (decided) | Getting back in; story 120 (#87) |
| 162 | An admin's recovery link and the sign-in's restore, from the machine's terminal (decided) | Getting back in; stories 120–121 (#87) |
| 163 | The mesh's control plane down: no LAN fallback, alerts that name the cause (decided) | Getting back in (#87) |
| 164 | A lost or stolen node: cut off, its certificate and credentials replaced, its apps restored | A lost or stolen node; Adapter contracts; Testing Decisions; story 123 (#91) |
| 165 | The archive also runs on change; a rotated token's predecessor outlives the next archive | ADR 0010 (amended); Backups and the archive; glossary (#88) |
| 166 | The platform's own state is backed up hourly | ADR 0010 (amended); Backups and the archive (#88) |
| 167 | Restore plans compare secrets with data; a TPM that no longer opens the secrets is reported at boot | ADR 0011 (amended); Backups and the archive (#88) |
| 168 | Catalog versions only go up, and expire (decided) | ADR 0009 (amended); Catalogs (#89) |
| 169 | Each catalog acts only on its own apps and signers (decided) | ADR 0009, ADR 0015 (amended); Catalogs; The install plan (#89) |
| 170 | Official pin bumps through CI and review, not security core (the maintainer, #89); upstream releases wait three days; upstream signers checked (decided) | ADR 0009 (amended); Updates, removal, eject (#89) |
| 171 | One restic repository per node (decided) | ADR 0010 (amended); Backups and the archive (#90) |
| 172 | Apps reach the sign-in through a sign-in listener of their own on the household's port, and the LDAP outpost on its port 636; the agent's probes reach apps (decided, #82) | ADR 0020; Modules (Agent); Testing Decisions (#82, reviews of #94) |
| 173 | The provider's first-admin flow applies to nobody before the sign-in is served, the executor API included | ADR 0004 (amended); Adapter contracts; Testing Decisions (#84, review of #94) |
| 174 | The recovery command also gives one of the admin's devices a mesh key (decided) | Getting back in (#87, review of #94) |
| 175 | Guests in no role, with their own mesh group; never admins; they can't add devices; the invite chooses the kid views (decided) | People leaving and visiting; glossary (#86, review of #94) |
| 176 | A new device through the manager's device page, mesh only, never through Home's process (decided) | People leaving and visiting; ADR 0013 (amended) (#86, reviews of #94) |
| 177 | Funnel only for households without a domain; with one, the public path is NetBird's (#102) | ADR 0021; Names and certificates (#92, #102) |
| 178 | The cluster's port found by a throwaway service; 443 when it can't run | ADR 0021; Names and certificates; Testing Decisions, claims to test first (#92, review of #94) |
| 179 | A lost or stolen primary: certificates reissued and the ACME account replaced; the node authority, peers, tokens, targets, the sign-in's secrets, guard cookie secrets, outstanding links and sessions replaced | A lost or stolen node; Names and certificates (#91, reviews of #94) |
| 180 | Catalogs signed again weekly; Admin warns before expiry (decided) | ADR 0009 (amended); Catalogs (#89, review of #94) |
| 181 | The primary's public listener carries the sign-in while anything is public | Modules (Edge); Testing Decisions (#81, second review of #94) |
| 182 | The probe: a recognisable throwaway service, a listener that serves nothing, named ports, leftovers swept | ADR 0021; Names and certificates (#92, #103) |
| 183 | Catalogs signed again with a separate timestamp key, limited to version, expiry and hash (decided) | ADR 0009 (amended); Catalogs (#89, second review of #94) |
| 184 | Admins sign in only through a passkey-only authentication flow | ADR 0004 (amended); Adapter contracts (#84, second review of #94) |
| 185 | Enrolment and recovery links run HomersLab's own passkey-enrolment flow | Adapter contracts (#84, #87, second review of #94) |
| 186 | The installer prints the command, not the link | First-time setup in two halves (#74) |
| 187 | The LAN includes the default-route interfaces' on-link global IPv6 prefixes | First-time setup in two halves (#73) |
| 188 | Admins' app-scoped LDAP credentials are exempt from the passkey-only check | Adapter contracts (#104) |
| 189 | The catalog's app waves, and two apps per purpose beyond classic | The catalog's app waves (#118–#122, #123) |
