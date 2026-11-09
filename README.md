# HomersLab

A polished, security-first app manager for a household's self-hosted apps, on the household's own Linux machines.

> **Not ready.** This repository holds the definition: [glossary](GLOSSARY.md), [decisions](docs/adr/),
> [spec](docs/spec.md) and [research](docs/research/). There is no code yet, and there will be no release before 1.0.

## What it will do

- **Install an app from a catalog, already wired.**
  - One household account signs you in: through the app's own sign-in where it has one, otherwise at the edge in
    front of it.
  - It gets a name on the household's private network.
  - Its data is backed up, and its restore is tested every week.
  - Kids see only what's for them.
- **Private by default.**
  - No port on the router, ever.
  - Apps are reached over the household's mesh (NetBird or Tailscale).
  - A public app still has its TLS end at home.
- **Update to tested versions**, with a snapshot first and an automatic rollback if the app comes back unhealthy.
- **One machine or several.** A primary, plus optional nodes such as a NAS or a GPU box; each app runs on one of
  them.
- **No lock-in.**
  - Any app can be ejected as a plain compose project with its data, and keeps running without HomersLab.
  - Any backup can be restored by hand with standard tools.

## Priorities

Security, then polish, then breadth:
- An app that can't meet the security floor stays out of the catalog.
- A polished flow never skips a security step.
- The catalog grows with the apps that can meet both.

## Read

| | |
|---|---|
| [GLOSSARY.md](GLOSSARY.md) | The words we use, and the ones we avoid |
| [docs/spec.md](docs/spec.md) | Problem, solution, user stories, decisions, tests, scope |
| [docs/adr/](docs/adr/) | The decisions that are hard to reverse, and why |
| [docs/research/](docs/research/) | What exists today and what the APIs allow (2026-11-09), with sources |

## Where it comes from

HomersLab reimagines [homelab](https://github.com/ArjunRachithcf/homelab) ("classic" in these documents), one
household's homelab made component-driven, as an app manager for any household. Classic contributes its ideas, not
its code.

The name comes from Homer's Odyssey, the long journey home.

## Licence

Apache-2.0 ([LICENSE](LICENSE)).
