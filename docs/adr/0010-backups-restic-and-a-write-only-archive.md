---
status: accepted
date: 2026-11-09
---

# Backups: deduplicated restic plus a weekly write-only archive, restorable by hand

**Frequent backups.** Every app's declared data is backed up frequently with restic, deduplicated and encrypted. The
targets are disk, SFTP, S3 or B2, plus Proton Drive through its official CLI.

**The weekly archive.** Once a week, the small sensitive things also go into an age archive:
- databases;
- settings;
- the sign-in's data;
- app secrets.

The archive is encrypted to a key that never touches the machines.

**Restore tests and restoring by hand.**
- Every app is restore-tested weekly.
- CI proves that a restore works by hand with plain restic and age, following the recovery kit.
- A household's data never depends on HomersLab existing.

## Considered options

- **Deduplicated backups only.** Whoever owns the primary could read every old backup.
- **Write-only age copies only, as in classic.** No deduplication, which doesn't scale to photos.

## Consequences

The restic repository key is on the primary, and in the recovery kit. Someone who takes over the primary can read the
frequent backups, but not the archive.
