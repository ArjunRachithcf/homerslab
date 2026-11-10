---
status: accepted
date: 2026-11-10
---

# Uptime and notifications: an outside heartbeat, incidents, alerts people can live with

**Someone notices when the primary is down.**
- The primary pings an outside check every 5 minutes: healthchecks.io or any compatible URL. When the pings stop, that
  service alerts the admins.
- Nodes also watch each other over the mesh: the first node that can't reach the primary for 5 minutes raises the
  alert itself.
- A one-machine household still has the outside check.

**History.** Admin keeps a year of history per app, per node and for the public path:
- uptime over 24 hours, 30 days and a year;
- response times;
- an incident log: what broke, when, for how long, and what fixed it.

**What alerts admins**, beyond the earlier decisions (backups, restore tests, disks, rolled-back updates, requests):
- a node offline for 5 minutes; a mesh client disconnected; the public path failing from outside;
- certificates, tokens and the identity provider's keys 14 and 3 days before they expire, when renewal hasn't
  already worked;
- security events: repeated failed sign-ins, a new admin, a new device on the mesh, a passkey reset, a catalog key
  added;
- resource pressure: memory or CPU pinned for 15 minutes, a container restarting in a loop, a GPU overheating.

**Alerts people can live with:**
- **One alert per incident**, a "resolved" message when it's over, and a reminder every 4 hours while it lasts.
- **Quiet hours** per admin, 22:00–07:00 by default. In those hours only critical alerts push: the primary down, a
  disk failing, security events. The rest waits for a morning digest.
- **Each admin chooses channels** per level (critical, warning, digest): mail, ntfy, the desktop app, or a webhook
  (Matrix, a Signal bot, Home Assistant). Mail stays on for critical, so nothing depends on one channel.

**Planned work happens in a maintenance window**, nightly by default, 02:00–05:00, chosen by the household.
- Automatic updates, scrubs, restore tests and moves run in it.
- Planned work silences its own alerts and shows "updating" on Home.
- An admin can still run something now; the plan says who it affects.

**The family hears on Home only**, for example "Photos isn't working right now; Inês knows" or "Photos is updating
until 03:00". Only admins get mails and pushes.

**No status page.** Home is the status, and nothing about the household's machines is published. The outside check
only learns that pings arrive.

## Considered options

- **Nodes watching each other only.** A one-machine household would get no alert.
- **Current state only, no history.**
- **Every alert at once, day and night.**
- **No maintenance window.**
- **The same channels for every admin.**
- **Pushes to family members.**
- **A status page.**
