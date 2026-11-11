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

- **Testing.** Every verified app is tested with both providers.
- **People** are managed in Admin, never in the provider's own console, which stays for emergencies.
- **Kanidm meets the contract by API** ([Kanidm spike](../research/2026-11-09-spike-kanidm-forward-auth.md)), at about
  100 MiB against Authentik's documented 2 GB minimum, with three duties for the adapter:
  - **Revocation reaches the guard.** Removing a person, a role or an app also ends that app's guard sessions. Kanidm
    doesn't re-check scope maps on refresh, and a deleted client lingered for about 15 minutes.
  - **LDAP is partial.** Per-app LDAP passwords are created by the person, and the service account needs
    `idm_account_mail_read`.
  - **Admins hold passkeys only.** HomersLab enforces this itself, because Kanidm 1.12 doesn't enforce a passkey-only
    policy at sign-in.
- **Step-up** (`prompt=login`, `max_age=0`) is proven with Kanidm.

## Amended (2026-11-11)

"Admins hold passkeys only" is enforced by HomersLab with **both** providers, not only with Kanidm (#84). Authentik's
own flows would otherwise undo it:
- its out-of-the-box first-admin flow makes whoever reaches it first a superuser. Blocking its page at the edge isn't
  enough, because the flow executor API runs it too, so it can't apply to anyone before the sign-in is served:
  `akadmin` gets a random password and is deactivated. The first admin's passkey comes only from the manager's
  one-time enrolment link. That link, and every recovery link, runs a flow HomersLab owns, with only a
  passkey-enrolment stage, never the provider's own recovery flow;
- from its user settings, a stolen session could give a passkey-only admin a password or a second factor, then pass
  step-up with it. So admins sign in, step-up included, only through a passkey-only authentication flow, and the
  provider's password, second-factor and recovery flows refuse admins. Binding the flow was chosen over refusing a
  step-up when the admin's credentials changed shortly before `auth_time`, which would rest on the provider's event
  log;
- HomersLab doesn't rely on `amr`, the claim that names how someone signed in: Kanidm sends none, and it describes one
  sign-in, not what an admin holds. So before accepting a step-up HomersLab asks the provider for the admin's
  credentials, and refuses the step-up when anything but passkeys is there;
- every passkey requires user verification, at enrolment and at every assertion.

The contract suite covers each point against both providers.
