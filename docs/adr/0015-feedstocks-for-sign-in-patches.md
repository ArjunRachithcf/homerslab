---
status: accepted
date: 2026-11-10
---

# Feedstocks: HomersLab-built images, only where sign-in needs a patch

**What a feedstock is.** Some open-source apps can't reach Verified sign-in through their own settings, OIDC, LDAP or
the edge's guard. Examples:
- an app that makes its first visitor the admin;
- an app that keeps a local password login nobody can switch off.

For such an app, HomersLab keeps a **feedstock**: the recipe that builds the app from its upstream source, together
with the few patches it needs to fit the household's sign-in. The name comes from conda-forge.

**The rules:**
- **A last resort.** A feedstock exists only when no setting, guard or plugin can do the job.
- **Upstream first.** Every patch is also offered to the app's own project, and the feedstock carries it only until
  upstream merges it or a setting replaces it. The aim for each feedstock is to need no patches.
- **One public repository**, with a folder per app: recipe, patches, tests. It can be split later if outside
  maintainers join.
- **Signed images.**
  - CI builds each image from source plus patches and attaches an SBOM.
  - It signs the image keyless with Sigstore, and the signature names the repository and workflow that built it.
  - The catalog pins the digest and the expected signer, and the agent refuses anything else.
  - Nodes never build.
- **Upstream releases.** A bot watches upstream releases and opens a pull request. CI then rebases the patches, builds,
  and runs the app's sign-in and restore tests.
- **Security fixes never wait on a patch.** If a patch no longer applies to an upstream security release, the catalog
  offers that release straight away with the app guarded at the edge. The household account still signs people in,
  but the app's own login may appear as well. The install plan says so, and the guard stays until the patch is
  rebased.
- **Third-party catalogs** may ship their own patched builds, declaring the expected signer. They are still trusted by
  key and still checked against the floor.
- **Licences.** Each app's patches carry that app's licence; an AGPL app's changes are public in the feedstock anyway.
  Each image names its source and patches. The tooling is Apache-2.0.
- **Where it shows.** A HomersLab build is shown in Admin's details of the installed app, not in the Store.

## Considered options

- **Build every Verified app from source.** Uniform signing and SBOMs, but every app becomes a build to maintain.
- **Keep forks indefinitely.** Every upstream release becomes a rebase forever.
- **One repository per app** (conda-forge's layout). Per-app maintainers, but too much overhead for one maintainer.
- **Apply patches on each node at install time.** No registry, but builds that can't be reproduced, in every household.
- **Hold a security release until the patch is rebased.** The household would knowingly run a vulnerable version.
- **Sign with a maintainer's key.** One key to protect, and CI couldn't sign without it.

## Consequences

- A container registry (GitHub's) and its signing identity become part of HomersLab's supply chain.
- The first feedstock is Memos, carrying classic's `sso-admin` patch.

## Amended (2026-11-11)

Accepted (#89, the maintainer, 2026-11-11).

A third-party catalog's declared signers apply to **its own apps only**: it can't declare a signer for another
catalog's app, and an installed app updates only from the catalog it came from
([ADR 0009](0009-catalog-floor-and-tiers.md), amended).
