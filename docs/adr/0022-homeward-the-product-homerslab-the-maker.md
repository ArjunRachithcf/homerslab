---
status: accepted
date: 2026-11-16
---

# Homeward is the product; HomersLab is the maker, and keeps its names in other software

Accepted (#250, the maintainer, 2026-11-16).

The manager is **Homeward** (nostos, the way home). **HomersLab** is the umbrella name its maker publishes under, with
no GitHub organisation for now; each app of the maker's takes a Homeric or Greek name with a plain subtitle (the reading
app Shelf is **Philobiblos**). The repository is `github.com/ArjunRachithcf/homeward` (GitHub redirects the old URLs).
ADRs 0001 to 0021 were written as HomersLab and are left as they were decided, with a note at the top.

**Homeward renames what the package installs on a host and what a person types or reads:**
- the Go module, `github.com/ArjunRachithcf/homeward`, and the one binary and its CLI, `homeward`;
- the units, `homeward-*` (the package's, and those the agent writes: apps', shares', backups', restores'), their
  drop-ins and the agent's files beside them (`homeward.yaml`);
- the folders, `/etc/homeward`, `/var/lib/homeward*`, `/var/cache/homeward-agent`, `/run/homeward*`,
  `/usr/share/homeward`, `/usr/libexec/homeward`;
- the users and groups, `homeward`, `homeward-edge`, `homeward-read`, `homeward-home`, `homeward-ldaps`, with the same
  fixed ids (#190);
- the environment, `HOMEWARD_*`;
- the docs, the UI's words and the design's copy.

**HomersLab's prefix stays wherever Homeward has written a name into other software, or published one to apps:**
- **the identity headers, `X-HomersLab-*`, and the identity contract `homerslab-v1`** (#236): a published contract with
  apps. Shelf (Philobiblos) and any app built for it read them; renaming them would break every such app at once, for
  no one's gain. They name the maker, which stays;
- the identity provider's objects: flows, stages, policies, groups (`homerslab-admins`, `-adults`, `-kids`), clients
  (`homerslab-<app>`), the scope `homerslab` and its claims, the service user, LDAP's `dc=homerslab`, the outpost;
- the mesh's: its groups, policies, setup keys, DNS zones and service user;
- the engine's: compose projects (`homerslab-<app>`, `homerslab-authentik`), and so their networks, volumes and
  containers. A Docker volume can't be renamed: a new project name would start every app on empty volumes;
- the DNS inventory record `_homerslab.<domain>` and its `homerslab=1` value;
- the text Homeward stores there and compares later: policy expressions, the names of flows and scope mappings,
  descriptions (an LDAP app closed is marked `homerslab: closed …`), the mesh clients' drop-ins. A changed word would
  read as drift, or as an object of someone else's;
- the firewall table, `inet homerslab`, and its saved file's first line: two tables side by side during a hand-over
  would each have to accept a packet, and a saved table must read as whole;
- the browser's: cookies (`__Host-homerslab-*`) and first-time setup's `sessionStorage` key;
- the official catalog's publisher, HomersLab, whose definitions' digests cover their files.

Renaming any of these would mean migrating household data inside other software, through each adapter, with nothing a
person sees changing. They are the maker's namespace, as `X-HomersLab-*` is.

**A node installed as HomersLab moves once** (`homeward agent migrate`, from `homeward-migrate.service`): at boot
before systemd-sysusers, the sockets, the firewall table and the agent, which requires it. It stops the old units (but
the firewall table's, which the engines require), renames the users and groups in place with their ids kept (a new
user sysusers made first, with another id, goes), moves each folder in one rename(2) (the agent's with every sealed
credential in it: the credentials' names carry no product name, so nothing is sealed again), drops what the agent
derives from its state, and leaves the agent a mark; the agent writes its units and drop-ins again under the new names
and removes the mark once all of it is in place. Every step is idempotent, and a run that stops anywhere is finished
by the next. A folder whose old and new names both hold files stops the run before it changes anything. The agent and
the manager refuse to start while an old folder is there, so a node never starts as a new household, with first-time
setup open, beside its own state. `homeward firewall restore` reads the old agent folder, never writing it, until it
has moved.

The migration, and the reading of the old folder, are kept for one release, then removed.
