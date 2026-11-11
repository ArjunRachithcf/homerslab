---
status: accepted
date: 2026-11-11
---

# One URL per name, its port chosen in first-time setup

Every name has one URL, `https://<name>.<domain>[:<port>]`, the same on the mesh and from the internet. The
household's port is chosen once, in first-time setup's mesh step:
- **443** when the mesh's public path can use it (self-hosted NetBird, Funnel), or when the household's meshes have
  no public path at all (Headscale alone);
- **the TLS port NetBird's cloud cluster accepts**, found by trying at setup time, even before anything is public.
  Classic found only 9443 open on the shared eu1 cluster (2026-10-17).

The edge serves every site on the mesh at that port too, and every URL HomersLab writes uses it: base URLs, redirect
addresses, the sign-in's origin, mails, invites and Home (#92).

**Why now, and not when the first app goes public.** An app builds its OIDC redirect addresses from one base URL;
WebAuthn checks the origin, port included; Home is installed as a PWA per origin; mails and invites carry links. Choosing
the port on the day of the first public app would change all of them at once: in effect, a domain rename.

## Considered options

- **443 on the mesh, the cluster's port only on the public path.** Two URLs per name: mesh and public users couldn't
  both sign in to an app with one base URL, and passkeys would need two origins.
- **Decide when the first app goes public.** Every app's URLs and every installed Home would change that day.

## Consequences

- On NetBird's cloud, URLs carry a port, even for a household that never makes anything public.
- Changing the port later is a guided migration, like renaming the main domain (ADR 0016).
- NetBird's capability report names the TLS ports its cluster accepts.
