---
status: accepted
date: 2026-11-09
---

# No lock-in: every app can be ejected

Eject writes an app out as a plain compose project with its data, settings and secrets. Where the household keeps its
identity provider, the app stays wired to the household account. The app then keeps running without HomersLab.

In the maintainer's words: "We are not trying people to keep using our interface; they can keep all benefits without
keeping HomersLab."

Eject is per app. The platform itself stays HomersLab's: the scheduler, Admin and Home.

## Considered options

- **A whole-platform "leave" command.**
- **Writing only standard files, with nothing of HomersLab in the request path.**

Both were considered; per-app eject, together with restoring by hand ([0010](0010-backups-restic-and-a-write-only-archive.md)),
was chosen.
