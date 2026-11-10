---
status: accepted
date: 2026-11-10
---

# Domains: only our own DNS records, every domain equal, one sign-in domain

**HomersLab writes public DNS itself.** It uses the DNS provider token it already holds, through libdns, and writes:
- the certificate challenges;
- when the first app goes public through NetBird's proxy, the wildcard CNAME to the proxy cluster and its validation
  record.

The install plan shows these records. They're removed again when nothing is public any more.

**Only its own records.** A household's zone also holds mail and other records: MX, SPF, DKIM, DMARC, the apex.
- HomersLab marks each record it creates as its own, with an owner TXT record beside it.
- It never edits, replaces or deletes a record it didn't create.
- It refuses, and says why, when a name it needs is already taken by someone else's record.

**Every domain is equal for the apps.** A household may add more domains in Admin's settings, each with its own DNS
token and wildcard certificate. Every app answers on every domain, with no redirect.

**One sign-in domain.** The identity provider lives on the main domain only, so one passkey works everywhere.
- Every app's sign-in goes to `auth.<main domain>`.
- Each app's OIDC client lists one address per domain.
- Guards set a cookie per domain, which costs one extra hop the first time on each domain.

**Renaming the main domain** is a guided migration, as a plan with step-up:
- a new certificate;
- every app re-pointed, and every sign-in client updated;
- the old domain kept answering for 90 days;
- everyone asked to add a passkey on the new address, because passkeys are bound to the name.

It can be rehearsed and rolled back.

## Considered options

- **Records added by hand, guided.** No write access to public DNS beyond the challenge, but a manual step each time
  public access starts.
- **Relying on a token the provider scopes to `_acme-challenge`.** Not every provider can scope tokens, and the CNAME
  would stay manual.
- **One domain only, or extra domains that only redirect.**
- **The identity provider on every domain.** One passkey across domains needs WebAuthn related origins, which only
  some browsers support; elsewhere people would need a passkey per domain.
- **No renaming**, only a new household and the importer.

## Consequences

- The DNS adapter contract gains:
  - create, update and delete limited to records HomersLab owns;
  - an ownership marker;
  - a check that a name is free.
- Tailscale households without a domain can add one later through the same migration.
