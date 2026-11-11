---
status: accepted
date: 2026-11-11
---

# The manager's database is SQLite, in pure Go

The manager keeps its state (first-time setup's steps, and later the household, its apps and their history) in one
SQLite file on the primary, through `modernc.org/sqlite`, a pure-Go driver.

**Why:**
- **One static binary.** No cgo and no database server to install, run, upgrade or back up separately.
- **Readable without HomersLab.** It's a standard file that the stock `sqlite3` tool opens, in keeping with
  no lock-in (ADR 0012).
- **Easy to back up and restore** with the rest of the primary's data (ADR 0010).
- **Enough for a household.** Writes are few and serialised through one connection; claims that must be atomic run in
  an immediate transaction.

**What it never holds:** secrets in the clear. Keys and tokens are stored only as SHA-256 hashes; app secrets are
systemd credentials (ADR 0011).

## Considered options

- **PostgreSQL.** A second server to run and back up, for a load a household never reaches.
- **SQLite through the cgo driver.** The same file format, but cgo breaks the static, cross-compiled binary.
- **An embedded key-value store (bbolt, Badger).** No SQL, and no standard tool to read it.
