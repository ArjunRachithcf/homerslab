---
status: accepted
date: 2026-11-09
---

# No lock-in: every app can be ejected

Eject hands over everything an app needs to run without HomersLab:
- its compose project, which already runs on standard tools ([0014](0014-app-units-run-standard-tools.md));
- its secrets, as files readable only by their owner (`0600`);
- its data and settings;
- for a guarded app, a ready edge snippet: Caddy with forward-auth to the household's sign-in.

An app with native sign-in stays wired to the household account for as long as the household keeps its identity
provider.

HomersLab's edge stops serving the app at eject. The eject plan says plainly that nobody can reach the app until an
edge is in front of it again, because apps publish no ports.

In the maintainer's words: "We are not trying people to keep using our interface; they can keep all benefits without
keeping HomersLab."

Eject is per app. The platform itself stays HomersLab's: the scheduler, Admin and Home.

## Considered options

- **Eject only apps with native sign-in.** Guarded apps would have to be removed instead.
- **A whole-platform "leave" command.**
- **Writing only standard files, with nothing of HomersLab in the request path.**

Per-app eject, together with restoring by hand ([0010](0010-backups-restic-and-a-write-only-archive.md)), was chosen.
