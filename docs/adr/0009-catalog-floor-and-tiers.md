---
status: accepted
date: 2026-11-09
---

# The catalog: one enforced floor, two tiers, declared extras, signed catalogs

An app is one folder: a manifest (`app.yaml`), a plain compose file (none for a host app), icons and screenshots.

Every app from any catalog must pass the floor, which the manager checks itself:
- mesh-only by default;
- no published ports;
- behind the household account;
- generated secrets;
- data declared for backup;
- pinned images;
- an install test in CI.

The two tiers and the extras:
- **Verified** apps add native sign-in, passing restore and update tests, and hardening.
- **Community** apps are converted from Coolify, Dokploy and CasaOS templates, then reviewed. They carry a label.
- **Extras** beyond the floor (the machine's network, a device, a capability) are declared in the manifest, spelled
  out in the install plan, and approved by an admin with the passkey. Privileged containers are never allowed.

Other catalogs are signed git repositories, trusted by their publisher's key.

## Considered options

- **Compose with an `x-` block only.** One file, but host apps don't fit.
- **An own format that isn't compose.** Nothing could be imported.
- **Verified apps only.** Dozens of apps instead of hundreds.
- **Unsigned catalog URLs.** Nothing would prove who wrote an app.

Runtipi's catalog is GPL-3.0 and umbrel-apps has no licence, so neither seeds ours
([landscape](../research/2026-11-09-landscape.md)).
