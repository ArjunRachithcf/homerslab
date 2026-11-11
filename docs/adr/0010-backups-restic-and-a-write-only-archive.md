---
status: accepted
date: 2026-11-09
---

# Backups: deduplicated restic plus a weekly write-only archive, restorable by hand

**Hourly backups.** Every hour, every app's own data is backed up with restic, deduplicated and encrypted. The
targets are disk, SFTP, S3 or B2, plus Proton Drive through its official CLI. Photos are an app's own data, so they
are always included. Storage locations (films, music, often terabytes and obtainable again) are backed up only when
the admin opts in, per location.

**The weekly archive.** Once a week, the small sensitive things also go into an age archive:
- databases;
- settings;
- the sign-in's data;
- app secrets, in plaintext inside age.

The archive is encrypted to a key that never touches the machines.

**Restore tests and restoring by hand.**
- Every app is restore-tested weekly.
- CI proves that a restore works by hand with plain restic and age, following the recovery kit.
- A household's data never depends on HomersLab existing.

## Considered options

- **Deduplicated backups only.** Whoever owns the primary could read every old backup.
- **Write-only age copies only, as in classic.** No deduplication, which doesn't scale to photos.
- **Daily backups of everything, storage locations included.** Simpler to explain, but slower and much bigger off site.

## Consequences

- The restic repository key is on the primary, and in the recovery kit. Someone who takes over the primary can read
  the hourly backups, but not the archive.
- Deleting an app's data leaves its backups and archive copies to expire on their own schedule.

## Amended (2026-11-11)

The intent stands: hourly, deduplicated backups that whoever owns a machine could read, and an archive that nobody on
the machines can. Two things change.

- **The archive also runs on change** (#88). Hourly data and a weekly archive drift apart: an app installed on Monday
  has data in restic but no secrets in any archive, and a rotated token leaves only a dead one there. So every secret
  created or rotated, every change to the sign-in's people or clients, and every change to the manager's desired
  state starts an archive run, debounced (quiet for 10 minutes, at most 30). The weekly run stays as the floor. A
  rotated token's predecessor is revoked only once an archive holds its successor. The platform's own state (the
  identity provider's database, the manager's) is backed up hourly with the apps.
- **One restic repository per node**, not one for the household. Accepted (#90, the maintainer, 2026-11-11).
  Every key of a restic repository opens all of it, so a stolen node holding the household's key would read every
  app's backups. Each node's repository sits at the same target with its own key and, where the target allows it, its
  own scoped credentials. A node holds only its own key; the primary holds them all, for restore tests and moves; the
  recovery kit lists each one. "The restic repository key is on the primary" now reads: every repository's key is on
  the primary, and each node holds its own.

Streaming every node's data to the primary to be backed up was considered: one key, but every byte crosses the mesh
twice and the primary becomes the bottleneck, which ADR 0006 avoided for the edge.
