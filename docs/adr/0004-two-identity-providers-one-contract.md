---
status: accepted
date: 2026-11-09
---

# Two identity providers from day one, behind one contract

A household chooses Authentik or Kanidm. Every identity provider adapter must provide:
- OIDC clients;
- forward-auth (for Kanidm, through oauth2-proxy);
- LDAP;
- people: invite, role, passkey reset, removal;
- groups for roles and audiences;
- step-up.

An app lists the sign-in methods it supports in order of preference, and the manager uses the best one, so no app
depends on which provider was chosen. Having two implementations at launch keeps the contract honest, and leaves the
household free to pick the lighter one.

## Considered options

Alternatives to two providers behind one contract:
- **Authentik only**: proven in classic, but heavy.
- **A light provider only.**
- **A contract that requires only OIDC**, with apps marked unavailable per provider.

Candidates for the second provider:
- **Pocket ID**: no LDAP, no forward-auth.
- **Authelia**: OIDC clients live only in its config file.
- **Zitadel**: AGPL, and heavier.

## Consequences

- Every verified app is tested with both providers.
- People are managed in Admin, never in the provider's own console, which stays for emergencies.
