# HomersLab: notes for agents

HomersLab is a security-first app manager for a household's self-hosted apps. Before changing anything, read:
- `GLOSSARY.md`, whose words are used in code, UI, commits and issues;
- the parts of `docs/spec.md` your ticket links;
- the ADRs it cites in `docs/adr/`.

A change that contradicts an ADR is a decision for the maintainer, not a code change.

## Working an issue, or running the loop

Follow `docs/loop.md` step by step. It is the procedure for picking a ticket, test-driven work, review, PRs, merging,
parking, and asking the maintainer.

## Conventions

- **Code**: Go for the manager and agents, React with TypeScript for Admin and Home. The design system in `design/` is
  the source of truth for the UI; `prototypes/` show how screens behave. Prototype data and sessionStorage are never
  product code.
- **Tests sit at the ticket's seam**, as named in the spec's Testing Decisions, and test behaviour through that seam.
  Adapters pass their contract suite against the real software in containers.
- **This machine runs a live homelab** (the compose project `homelab`). Tests and spikes keep to their own world:
  - compose projects named `hltest-<ticket>`;
  - ports published on 127.0.0.1 only;
  - a stubbed engine pointed at nowhere (`DOCKER_HOST=unix:///nonexistent`);
  - their own `HOME`;
  - no sudo;
  - `down -v` of their own project when done.
- **Secrets** stay in files ignored by git or in the environment. Outputs, logs, commits, issues and PRs carry
  placeholders.
- **Commits** use the noreply identity `Arjun Rachith <106335968+ArjunRachithcf@users.noreply.github.com>`, end with the
  session's attribution lines, and come one ticket per PR.
- **Pushing** goes over HTTPS:
  `git -c credential.helper= -c credential.helper='!gh auth git-credential' push https://github.com/ArjunRachithcf/homerslab.git <branch>`.

## The security core

These areas are the **security core**. A PR touching them is labelled `security-core` and is merged by the
maintainer, never by an agent:
- identity provider adapters, sign-in and step-up;
- secrets;
- firewall rules;
- the edge's authentication and header handling;
- DNS writes;
- disk erase and encryption;
- signing and verification (catalogs, feedstocks, releases);
- update and rollback;
- the workflows in `.github/`;
- this file and `docs/loop.md`.
