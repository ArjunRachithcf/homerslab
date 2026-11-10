---
status: accepted
date: 2026-11-10
---

# Disks: managed from Admin, btrfs, always encrypted, mirrored by default

Admin manages each node's disks well beyond classic's one-time "second disk" script.

**What Admin does with disks:**
- **See and watch** every disk: model, size, SMART health and temperature, filesystems, use per app and per storage
  location, growth, "full in N days", and scrub results. Health or space turning bad raises an alert by mail, ntfy and
  on Home.
- **Adopt** an existing filesystem, or a folder on it, as app-data space or a storage location, without touching what's
  on it.
- **Prepare** a new disk: partition, encrypt, format and mount it, then offer it.
  - **Encryption, always**: LUKS2, unlocked by the TPM at boot with no prompt, with a recovery key in the household's
    recovery kit.
  - **The filesystem is btrfs**, which gives:
    - snapshots, for restore points and the snapshot before each update;
    - reflink copies;
    - checksums with scheduled scrubs;
    - compression;
    - mirrors.
- **Mirrors by default**: disks that hold app data become a btrfs mirror whenever there are two or more. For a disk
  meant for media (terabytes that can be fetched again), Admin asks: mirror, or single for all the space. Mirrors
  don't replace backups, and the UI says so.
- **Pools**: disks are combined into a pool and grown, and a failed disk is replaced in a guided flow.
  - The mirror keeps running on the good disk, marked at risk.
  - Admin shows which physical disk to pull (model, serial, slot).
  - It rebuilds and scrubs, and the apps stay up.
- **Erasing**: a disk with no partitions or filesystem can be prepared with the passkey. A disk with data needs an
  "Erase this disk" plan:
  - it shows what's on the disk;
  - it needs the passkey plus the disk's serial, typed;
  - the system disk, and any disk in use, is never offered.
- **Moving an app's data** to another disk or node is a plan:
  - snapshot, then copy (btrfs send/receive where it can), then verify checksums;
  - a short stop, a final sync, the switch, and a health check;
  - the old copy is kept for 7 days, with rollback in one action.
- **Low space**: warnings at 80% and 90%. At 95%, apps whose definition says they can pause (downloads, imports,
  transcodes) pause, while backups and the household's sign-in keep running. There are no hard quotas by default;
  an admin may set a size limit per app.
- **The system disk**: Admin offers to encrypt it in place.
  - It's a guided, rehearsable migration: a dry run in a VM, a full copy to an external disk first, and a time at the
    console.
  - This generalises the procedure rehearsed for the maintainer's own machine (29/29 in a VM).

**The disk analyzer**, for admins only, works like a desktop disk tool:
- **Views**:
  - a space map and a list sortable by size, age, type and owner (app or storage location);
  - the biggest files and files not opened in a year;
  - duplicates, which can be freed or shared as reflinks;
  - space per person, as far as apps report it.
- **The passkey**: opening it asks for the passkey (it shows file names across the household's data), and the session
  lasts up to 10 minutes. Every delete asks again, bound to the exact list of items and their size.
- **Where it may delete**: freely only in storage locations and in HomersLab's own leftovers (removed apps' data, old
  snapshots, old move copies, caches, logs). Inside an installed app's data it offers that app's own cleanup instead,
  because deleting behind an app's back corrupts it.
- **Undo**: deleted items stay in "Recently deleted" for 7 days, kept by a btrfs snapshot, and can be restored with the
  passkey. "Free the space now" empties it, also with the passkey.

## Considered options

- **ext4.** Simpler, but no snapshots, checksums or mirrors.
- **ZFS.** Strong, but an out-of-tree kernel module with a licence clash.
- **Encryption as an option per disk, or not at all.**
- **Backups only, without mirrors; or a mirror for media too.**
- **Only erasing empty disks.**
- **Moving data by backup and restore.**
- **Quotas for every app.**
- **Advising on, but never touching, the system disk.**

## Consequences

- **The agent's contract grows**: disks, LUKS with TPM enrolment, btrfs (subvolumes, snapshots, send/receive, scrub,
  mirrors) and SMART. All of it is root, so every write is a plan, and the destructive ones need step-up.
- **The TPM binding differs by firmware.** On machines that boot in legacy BIOS mode it binds weaker PCRs (no Secure
  Boot state). Admin says so.
- **The in-place system-disk encryption** is the riskiest flow in HomersLab. It ships only with its VM rehearsal in CI
  and a verified full copy as a precondition.
