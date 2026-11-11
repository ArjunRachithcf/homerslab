---
status: accepted
date: 2026-11-09
---

# Two surfaces: Home, unprivileged; Admin, privileged and mesh only

**Home** is where people open their apps:
- it may be public, behind the household account;
- it installs as a PWA;
- the desktop family app is a second way into it.

**Admin** is the manager:
- catalog, install plans, nodes, people, backups;
- mesh only;
- step-up for every risky change.

They are separate processes with one look, so a flaw in the public-facing Home can't reach the manager.

The CLI and the MCP server use Admin's API. An assistant can never make a risky change, because that needs a person's
passkey.

## Considered options

- **One web app, with pages chosen by role.** Simpler, but then the public surface would be the privileged one.
- **A desktop app as the main way in.** The box would serve no admin UI, but nothing could be done without that app.

## Amended (2026-11-11)

Accepted (#86, the maintainer, 2026-11-11).

**A third surface: the manager's device page.** A person adds one of their own devices to the mesh from another
device of theirs. That needs a mesh key, which is privileged, so it can't come from Home's process. Instead, the
manager serves a page of its own for it:
- its own origin, separate from Admin's and Home's;
- mesh only, like Admin, but reachable by every person's own devices, not only admins';
- a step-up at the sign-in before any key is made.

Home only links to it.
