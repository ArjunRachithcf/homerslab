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

## Amended (2026-11-11)

Accepted (#89, the maintainer, 2026-11-11).

"Signed git repositories, trusted by their publisher's key" stands, but a signature alone doesn't stop an older,
correctly signed commit from being served (rollback), or the same commit forever (freeze), and doesn't stop one catalog
from acting on another's apps. So:
- **Versions only go up, and expire.** A catalog's signed metadata carries a version number that only goes up and an
  expiry 30 days out. The publisher signs it again on a schedule (weekly) even when nothing changed, as TUF's
  timestamp role does, so a frozen catalog shows. That signature uses a separate timestamp key, which may only sign
  the version, the expiry and the hash of content the publisher's key signed, so the key kept online for the
  schedule can't sign apps. The manager refuses a lower version than the last it accepted.
  Admin warns as the expiry nears; after it, installed apps keep running and new installs from that catalog wait.
- **Each catalog acts only on its own apps.** App ids are scoped to their catalog; an installed app updates only from
  the catalog and publisher key it came from; the plan names the catalog and publisher beside the tier.
- **Upstream releases wait three days**, unless the catalog marks one a security release. In the official catalog, a
  pin bump goes through the same CI and independent review as any change, and the loop may merge it: the maintainer
  decided on 2026-11-11 (#89) that pins are not part of the security core. The three-day wait, the install and
  restore tests in CI, and the signer check carry the protection. Where an upstream signs its images, the catalog pins the expected signer, and the agent
  checks it.
