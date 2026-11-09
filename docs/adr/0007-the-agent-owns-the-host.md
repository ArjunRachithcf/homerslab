---
status: accepted
date: 2026-11-09
---

# The agent owns the host; Linux only

A signed package per distro installs the container engine, the mesh clients and the agent:
- distros: Fedora, Debian, Ubuntu and Arch;
- architectures: x86_64 and arm64.

From then on, the agent owns its firewall table and runs HomersLab's jobs through the manager's scheduler, and it
corrects drift continuously.

There is no Ansible, unlike classic. There is no macOS either, because the agent's firewall and service model need
Linux.

## Considered options

- **Ansible run by the manager.** Proven in classic, but drift is found only by check runs.
- **Leaving the host to the admin.** No guarantees about the firewall or drift.
- **macOS, best effort.** Docker runs in a VM there, and there is no firewall model to own.
