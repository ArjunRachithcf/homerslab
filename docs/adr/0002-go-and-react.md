---
status: accepted
date: 2026-11-09
---

# Go for the manager and the agents, React for the surfaces

The manager and the agents are written in Go:
- The Compose SDK, compose-go, Caddy, libdns, the NetBird and Tailscale clients, and Authentik's API client are all
  Go.
- One static binary ships to every distro and architecture.

The Admin and Home surfaces are React with TypeScript, compiled into the binary. We accept the npm toolchain, and the
supply-chain review that comes with it, in exchange for its ecosystem.

## Considered options

- **Rust**: its Docker and Compose libraries are thinner.
- **A TypeScript server**: a heavier runtime on every node.
- **Python** (classic's engine): hard to ship as one binary.
- **For the UI, Go templates with htmx**: no npm and a simpler content security policy. Recommended during the
  grilling, but not chosen.
- **For the UI, Svelte.**
