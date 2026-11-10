# Spike: app secrets as systemd credentials

- **Question**: can an app's secrets live as TPM-sealed systemd credentials, decrypted only when the app's service
  starts, and reach its containers as files? It must work for Docker and rootless Podman, with nothing of HomersLab in
  the request path ([ADR 0011](../adr/0011-secrets-as-tpm-sealed-systemd-credentials.md), spec "Open spikes" 2).
- **Checked**: 2026-11-09, on the maintainer's PC, which runs a live homelab. No sudo, transient user units only,
  dummy secrets only. Everything that needs root is in the [test plan](#needs-root-test-in-a-vm-or-ci).
- **Spike**: [`spikes/systemd-creds/`](../../spikes/systemd-creds/). `spike.sh` runs each step, `probe.sh` runs inside
  the transient units, and `credhdr.py` reads a credential's key type from its unencrypted header without decrypting.
- **Versions**:
  - Fedora 44, kernel 7.2.11, systemd 259.10;
  - Docker CE 29.8.1 (containerd 2.3.5, runc 1.5.1, `"selinux-enabled": true`), Docker Compose 5.5.1;
  - Podman 5.8.7, rootless (crun 1.28, conmon 2.2.1);
  - SELinux enforcing (selinux-policy 44.10, container-selinux 2.251.0), tpm2-tss 4.1.5;
  - a firmware TPM 2.0 (`/dev/tpmrm0`, `root:tss`). The PC boots in legacy BIOS mode: there is no `/sys/firmware/efi`.
- **Outcome**: ADR 0011 holds for Docker and rootless Podman, with the [changes below](#what-adr-0011-must-change).
  System units, rootful Podman and sealing as root wait for the VM test.

**Verdict**:
- **Files in containers, with nothing of HomersLab in the path**: yes, for:
  - Docker and Docker Compose;
  - rootless Podman: its CLI, Quadlet, and Compose through Podman's Docker-compatible API.

  The unit's `LoadCredentialEncrypted=` decrypts at start, and the engine bind-mounts `$CREDENTIALS_DIRECTORY/<name>`
  read-only. The secret is never in `inspect`, `compose config` or `systemctl show`.
- **SELinux**: no container can read the credential until the credential is labelled for containers. What works: the
  unit labels its own credential with the app's MCS level, and the app's containers run at that level. The engines
  never relabel it.
- **Sealed to the TPM**: not by default.
  - On this PC, the default `auto` used the host key alone, because systemd doesn't count a TPM as fully supported
    without firmware support.
  - An unprivileged user can't ask for the TPM. Root can, so the agent must name the key.
- **Decrypted only at start**:
  - System credentials: yes, since only root can decrypt them.
  - A user's own credentials: no. That user can decrypt them at any time.
  - Either way, a container that outlives its unit keeps the plaintext.
- **Restore on new hardware**: no sealed credential survives a move or a reinstall. The archive must carry the
  plaintext, and a restore re-seals it.

## 1. User-scoped credentials

An unprivileged `systemd-creds` doesn't encrypt anything itself. It asks the `io.systemd.Credentials` varlink service
(`/run/systemd/io.systemd.Credentials`, socket-activated, running as root), because the CLI switches to IPC whenever
`geteuid() != 0` ([creds.c](https://github.com/systemd/systemd/blob/v259/src/creds/creds.c)).

```
$ systemd-analyze has-tpm2            # 'systemd-creds has-tpm2' now redirects here
partial
-firmware
+driver
+system
+subsystem
+libraries
$ systemd-creds --user encrypt --name=db-password - db-password.cred
db-password.cred: host, user-scoped (134 bytes)                                # credhdr.py
-r--------. 1 root root 4112 Nov  9 19:55 /var/lib/systemd/credential.secret   # made by this first use
```

| `systemd-creds --user …` (step 1) | Result |
|---|---|
| `encrypt` (default `--with-key=auto`) | host key, user-scoped |
| `--with-key=host` | host key, user-scoped |
| `--with-key=host+tpm2` | exit 0, but host key, user-scoped: the TPM was silently dropped |
| `--with-key=tpm2`, `null`, `auto-initrd` | `Selected key not available in --uid= scoped mode, refusing.` |
| `decrypt` as the same user, outside any unit | works |
| `decrypt` under another name | `Name in credential doesn't match expectations.` |
| system scope (encrypt or decrypt), or another user's scope | `io.systemd.InteractiveAuthenticationRequired`: polkit, `auth_admin_keep` |

**Why there's no TPM**:
- **What `auto` checks**: it uses the TPM only when `tpm2_is_fully_supported()`, which requires `TPM2_SUPPORT_FULL`.
  That includes the firmware flag ([tpm2-util.h](https://github.com/systemd/systemd/blob/v259/src/shared/tpm2-util.h)).
  A legacy BIOS boot never sets that flag, even with a working TPM, because `efi_has_tpm2()` returns false when the
  boot isn't EFI ([efi-api.c](https://github.com/systemd/systemd/blob/v259/src/shared/efi-api.c)). On a UEFI machine
  with a TPM, `auto` gives host+tpm2.
- **An unprivileged user can't choose**:
  - the CLI's IPC call carries no key type, because `ipc_encrypt_credential()` has no key parameter
    ([creds-util.c](https://github.com/systemd/systemd/blob/v259/src/shared/creds-util.c));
  - the service's `WithKey` type has no user-scoped value (introspection, step 1h). A user-scoped credential therefore
    gets `auto`.
- **Root can choose**: with `--uid=`, the CLI maps `--with-key=host+tpm2` to the user-scoped host+tpm2 key and encrypts
  locally ([creds.c](https://github.com/systemd/systemd/blob/v259/src/creds/creds.c), `parse_argv()`).
- **The host key is always there**: user-scoped credentials have no TPM-only variant
  ([creds-util.h](https://github.com/systemd/systemd/blob/v259/src/shared/creds-util.h)).

**What a user credential is bound to**:
- the host key `/var/lib/systemd/credential.secret` (root, `0400`);
- the user's UID and user name, and the machine ID, mixed into the key
  ([systemd-creds(1)](https://www.freedesktop.org/software/systemd/man/latest/systemd-creds.html), `--user`);
- and the TPM, when it is used.

**Restore on other hardware**: read from the code and the man page. The move itself is in the test plan.
- **The TPM key** opens only on that chip ([systemd-creds(1)](https://www.freedesktop.org/software/systemd/man/latest/systemd-creds.html)).
- **The host key** belongs to one installation. `credential.secret` stores the machine ID, and a file whose machine ID
  doesn't match is unlinked and replaced (`get_credential_host_secret()`,
  [creds-util.c](https://github.com/systemd/systemd/blob/v259/src/shared/creds-util.c)). Copying it to a new machine
  doesn't help, and a reinstall on the same PC (a new machine ID) loses it too.
- **User scope** also binds the UID and the user name. Recreating an app's user with another UID breaks its
  credentials.
- **systemd 262** pins TPM-sealed credentials to the TPM's SRK, and older systemd versions don't recognise credentials
  made that way ([NEWS](https://github.com/systemd/systemd/blob/v262/NEWS)).

**The host key alone is the option ADR 0011 rejected**: with `auto` on this PC, the key and the credentials are both
on the disk. Anyone with a copy of the disk has both, and this PC's disks aren't encrypted yet.

## 2. Transient user units

Each case runs `systemd-run --user --wait --pipe --collect -p LoadCredentialEncrypted=db-password:<file> …`.

```
CREDENTIALS_DIRECTORY=/run/user/1000/credentials/hlspike-creds-a.service
dr-x------ user:user /run/user/1000/credentials/hlspike-creds-a.service
-r-------- user:user /run/user/1000/credentials/hlspike-creds-a.service/db-password
label: dir unconfined_u:object_r:user_tmp_t:s0, file unconfined_u:object_r:user_tmp_t:s0
NAME        SECURE SIZE PATH
db-password weak    30B /run/user/1000/credentials/hlspike-creds-a.service/db-password
```

- **The user manager's credentials directory**:
  - A user manager can't mount, so it falls back to a plain directory on the `/run/user/1000` tmpfs ("Lacking privilege
    to mount credentials fs, falling back to plain directory",
    [exec-credential.c](https://github.com/systemd/systemd/blob/v259/src/core/exec-credential.c)).
  - It is swappable, which `systemd-creds list` reports as `weak`.
  - It is writable, which is why relabelling works below. It is labelled `user_tmp_t`.
- **A system unit's credentials directory**:
  - It is a separate read-only tmpfs (`noswap`), or ramfs.
  - Each file gets a read ACL for the unit's `User=`
    ([exec-credential.c](https://github.com/systemd/systemd/blob/v259/src/core/exec-credential.c)).
  - The test plan covers it.
- **The credential's ID**: in `LoadCredentialEncrypted=<id>:<path>`, the ID must be the name embedded at encryption,
  whatever the file is called. A mismatch fails the unit with `Failed to set up credentials: Destination address
  required` and `status=243/CREDENTIALS`.
- **`%d`**: transient units don't expand it, but they do expand `${CREDENTIALS_DIRECTORY}` at exec. Unit files and
  Quadlet can use `%d` ([systemd.unit(5)](https://www.freedesktop.org/software/systemd/man/latest/systemd.unit.html#Specifiers)).

### (a) Docker

| Container (step 2a) | Result |
|---|---|
| `--mount type=bind,…,readonly`, no relabel | denied by SELinux |
| `-v …:ro,z` | read; the credential is relabelled in place to `container_file_t:s0` |
| `-v …:ro,Z` | read; relabelled to `s0:c203,c650`, after which every other container is denied |
| `--user 999:999` | denied: the file is `0400` and owned by 1000 |
| `--user 1000:1000` | read |
| `--mount` with a source that wasn't loaded | refused: `bind source path does not exist` |
| `docker inspect` | the bind's path only; the secret was found 0 times |

```
avc:  denied  { open } for  pid=1424434 comm="cat" path="/run/secrets/db-password" dev="tmpfs" ino=140431
  scontext=system_u:system_r:container_t:s0:c501,c713 tcontext=unconfined_u:object_r:user_tmp_t:s0 tclass=file permissive=0
```

### (b) Rootless Podman

| Container (step 2b) | Result |
|---|---|
| `-v …:ro`, no relabel | denied by SELinux |
| `--mount type=bind,…,ro=true,relabel=shared` | read. Podman's `--mount` can relabel; Docker's can't |
| `-v …:ro,Z` | read |
| `--user 999:999` | denied: inside the container the file is `0:0`, since the host user is the container's root |
| `--userns=keep-id:uid=999,gid=999 --user 999:999` | read: inside the container the file is `999:999` |
| `-v` with a source that wasn't loaded | refused: `statfs …: no such file or directory` |
| `podman inspect` | the bind's path only; the secret was found 0 times |

### (c) Docker Compose

The project is `hlspike-creds`, and the secret is `secrets: db-password: file: ${CREDENTIALS_DIRECTORY}/db-password`.

| Service (step 2c) | Result |
|---|---|
| `secrets: [db-password]` | denied by SELinux: a read-only bind through the Mounts API, with no relabel |
| the same with `uid`, `gid`, `mode` | ``secrets `uid`, `gid` and `mode` are not supported, they will be ignored`` |
| long-syntax bind, `selinux: z`, `create_host_path: false` | denied: the relabel is silently dropped (Mounts API, empty `BindOptions`) |
| the same with `create_host_path: true` (the default) | read: Compose switches to the legacy `Binds` (`…:ro,z`) |
| short syntax `…:ro,z` | read (`Binds`) |
| `secrets:` after the unit ran `chcon -t container_file_t -l s0` on it | read, with no denials |
| `secrets:` from an environment variable | `uid`, `gid` and `mode` honoured, but `docker diff` shows `A /run/secrets/db-password`: the plaintext is in the container's writable layer, on disk |
| `docker compose config` | the path only; the secret was found 0 times |

### (d) One MCS level per app

The unit runs `chcon -t container_file_t -l s0:c100,c200` on its own credential (steps 2d and 2e).

| Container | Result |
|---|---|
| Docker `--mount`, `--security-opt label=level:s0:c100,c200` | read |
| Docker `--mount` at its own random level, as another app would be | denied |
| Podman `--mount`, no relabel, `--security-opt label=level:s0:c100,c200` | read |
| Podman at its own random level | denied |
| Compose `secrets:` with `security_opt: [label=level:s0:c100,c200]` | read (process label `container_t:s0:c100,c200`) |
| the same Compose file through rootless Podman's API (`DOCKER_HOST`) | read at the app's level, denied at another |

This combines what the other cases lack:
- the engines never relabel, so `-v` is never needed;
- Compose `secrets:` work unchanged;
- apps are kept apart by MCS, as with `:Z`, while all of one app's containers share its credentials.

### Quadlet

Quadlet's generator (`quadlet -dryrun -user` on
[`quadlet/hlspike-creds-q.container`](../../spikes/systemd-creds/quadlet/hlspike-creds-q.container)) passes
`LoadCredentialEncrypted=` through. It turns `Volume=%d/db-password:/run/secrets/db-password:ro,z` into `podman run …
-v %d/db-password:…`, and adds:
- `--cgroups=split`, `Type=notify` and `KillMode=mixed`;
- `ExecStop=podman rm -v -f -i`.

The generated unit worked when run as a transient unit, with `${CREDENTIALS_DIRECTORY}` in place of `%d` (step L7):
- conmon was the main process;
- the container ran inside the unit's cgroup;
- UID 999 read the credential;
- a restart re-decrypted it, and stopping the unit removed the container.

The MCS variant, [`quadlet/hlspike-creds-mcs.container`](../../spikes/systemd-creds/quadlet/hlspike-creds-mcs.container)
(`SecurityLabelLevel=`, `Mount=type=bind,src=%d/…`, `ExecStartPre=chcon …`), generates the expected
`podman run --security-opt label=level:… --mount …`. Only its generation was checked; it wasn't run.

## 3. The checks

**Permissions inside the container**:
- A user unit's credential is `0400` and owned by the unit's user; its directory is `0500`.
- **Docker**, rootful and without userns-remap: the file shows as `1000:1000`. Root in the container reads it, through
  `CAP_DAC_OVERRIDE`; another UID reads it only if it is 1000.
- **Rootless Podman**: the file shows as root's, `0:0`. `--userns=keep-id:uid=N,gid=N` makes it N's.
- **Compose** ignores `uid`, `gid` and `mode` for a file-based secret. Its reference says they're "silently ignored"
  ([services](https://docs.docker.com/reference/compose-file/services/#secrets)); version 5.5.1 warns.
- **The limit**: an image whose process reads the secret as some other UID, such as an entrypoint that re-executes
  itself as an app user, can't read it.

**SELinux**:
- **Denied by default**: `container_t` can't open `user_tmp_t` files (the AVC above).
- **`:z` and `:Z`** work on a user unit's `$CREDENTIALS_DIRECTORY`, and neither engine refused them.
  - They relabel the unit's own inode.
  - `:Z` gives it to one container, so a second `:Z` container takes it away from the first.
- **Docker can relabel only with `-v`**: "It is not possible to modify the SELinux label using the `--mount` flag"
  ([bind mounts](https://docs.docker.com/engine/storage/bind-mounts/#configure-the-selinux-label)). But `-v` recreates
  a missing source (see Restarts below).
- **Compose** drops `selinux:` unless `create_host_path: true`.
- **Podman** can relabel with either flag ([podman-run(1)](https://docs.podman.io/en/latest/markdown/podman-run.1.html),
  `--mount` `relabel=`, and `-v` `z`/`Z`).
- **The unit can label its own credential**: its user owns the file, so `chcon -t container_file_t -l <level>` works.
  That is the pattern in (d).
- **System units** keep their credentials on a read-only file system, so a relabel is expected to fail there (see the
  test plan).

**Not in `inspect`**:
- `docker inspect`, `podman inspect`, `docker compose config` and `systemctl --user show` contain only the bind's path
  (`LoadCredentialEncrypted=` prints as `[unprintable]`).
- The exception is Compose's secrets from an environment variable: the value passes through Compose's environment and
  lands in the container's writable layer.

**When the service stops**:
- **The directory goes**: systemd removes the credentials directory.
- **The unit's own containers stop with it**: those running in its foreground (`docker run --rm`, `docker compose up`,
  `podman run`, Quadlet). Compose stopped its container, which exited 0.
- **Where the containers run**:
  - Docker's containers live in Docker's cgroup (`/system.slice/docker-<id>.scope`), not in the unit's.
  - Plain `podman run` keeps conmon in the unit, but the container in its own `libpod-<id>.scope`.
  - Quadlet's `--cgroups=split` puts the container inside the unit.
- **A detached container survives the stop and keeps reading the secret**: its bind mount pins the deleted file. This
  happened with Docker and with Podman:

  ```
  credentials dir: ls: cannot access '$XDG_RUNTIME_DIR/credentials/hlspike-creds-ld2.service': No such file or directory
  hlspike-creds-ld2: running (exit 0)
  ld2 reads: READ-OK sha256=e51f325947fe
  ```

**Restarts**:
- **`systemctl restart`** re-decrypts into a new directory (a new inode), and the new container reads it (Docker,
  Podman, Quadlet).
- **`docker restart` and `podman restart`** work while the unit runs.
- **After the unit has stopped**:
  - **Docker `--mount`**: refused, and nothing is created (`failed to fulfil mount request: open
    …/db-password: no such file or directory`).
  - **Compose `secrets:`**: `docker start` is refused (`bind source path does not exist`).
  - **Podman**: `getxattr …/db-password: no such file or directory`.
  - **Docker `-v`**: Docker recreates a missing bind source as a root-owned directory at every start
    ([bind mounts](https://docs.docker.com/engine/storage/bind-mounts/): "It's always created as a directory"). Only
    `--mount` skips that ([volumes.go](https://github.com/moby/moby/blob/master/daemon/volumes.go),
    [mounts.go](https://github.com/moby/moby/blob/master/daemon/volume/mounts/mounts.go)).
    - Tested on a stand-in file: `host now has: directory root:root work/standin/db-password`.
    - Under `$XDG_RUNTIME_DIR/credentials/` it would leave a root-owned directory that the user manager can't remove.

## What ADR 0011 must change

**The decision**:
1. **Name the key.** The agent seals with `--with-key=host+tpm2` whenever a TPM works, and never relies on `auto`.
   - It seals as root, with `--uid=<app user>` for rootless apps.
   - Without a TPM, the host key alone protects no better than root-only files unless the disk is encrypted. The ADR
     should say so.
2. **The archive's copy is the plaintext**, inside the age archive.
   - A restore re-seals it on the new machine.
   - Sealed credentials and `credential.secret` are never the backup: they don't open anywhere else.
3. **Reword "decrypted only when the app's service starts"**: "decrypted at service start, kept in memory, never in an
   engine's configuration". That holds for system credentials only.
   - An app user can decrypt its own credentials at any time.
   - A container that outlives its unit keeps them.

**Rules for the agent and the floor**:
1. **systemd owns the containers.**
   - The unit's main process is the engine in the foreground: `docker compose up`, or Quadlet.
   - Engine restart policies are off: `restart: "no"` overrides the catalog's compose file, and the unit's `Restart=`
     restarts instead.
   - Drift checks report a container that runs without its unit.
2. **Credentials are mounted only with `--mount` or Compose `secrets:`**: never `-v`, the short syntax or
   `create_host_path: true`.
3. **One MCS level per app.**
   - The unit labels its credentials before starting (`ExecStartPre=chcon -t container_file_t -l <level>
     %d/<name>`).
   - Every container of the app runs at that level: `security_opt: [label=level:…]`, or Quadlet's
     `SecurityLabelLevel=`.
   - Proven for user units; system units wait for the test plan.
4. **Which UID reads the secrets is part of the app definition.**
   - Rootless: `UserNS=keep-id:uid=N,gid=N`.
   - Docker: the container runs as root, or the unit runs as `User=N` so that systemd's ACL covers N (test plan).
   - Compose's `uid`, `gid` and `mode` don't apply to files, and secrets from environment variables are refused,
     because they reach the disk.
5. **The embedded name is the unit's credential ID.**
   - Use `<app>.<name>`, so that one app's credential file can't stand in for another's.
   - Compose's `target:` gives the container `/run/secrets/<name>`.
6. **Swap**: a user unit's credentials are swappable. With rootless per-app users, the floor needs no disk swap or
   encrypted swap. zram is fine.
7. **No mount sandboxing in an app's unit for Docker or for Podman's API service.** With namespacing, a system unit's
   credentials are "invisible to all other services" ([CREDENTIALS](https://systemd.io/CREDENTIALS/)), and `dockerd`
   is another service (test plan).

## Needs root: test in a VM or CI

Use two Fedora 44 VMs, one with UEFI and one with a legacy BIOS boot, each with its own swtpm, Docker CE 29.8 with
`"selinux-enabled": true`, Podman 5.8, and `spikes/systemd-creds/`.

**0. The TPM**:

```sh
systemd-analyze has-tpm2; echo $?          # UEFI VM: yes, 0. BIOS VM: partial, -firmware
systemd-analyze srk >/dev/null && echo "SRK present"
```

**1. What the agent runs to seal.** The plaintext only ever goes in on stdin.

```sh
install -d -m 0700 /etc/credstore.encrypted
gen() { head -c 32 /dev/urandom | base64 -w0; }
gen | systemd-creds encrypt --with-key=host+tpm2 --name=db-password - /etc/credstore.encrypted/hlspike.db-password
gen | systemd-creds encrypt --with-key=tpm2 --name=tpm-only - /etc/credstore.encrypted/hlspike.tpm-only
gen | systemd-creds encrypt --name=auto - /etc/credstore.encrypted/hlspike.auto
python3 -I spikes/systemd-creds/credhdr.py /etc/credstore.encrypted/hlspike.*
# expect host+tpm2, tpm2, and for auto: host+tpm2 on UEFI, host on BIOS. "TPM2 PCRs: none" for all.
```

**2. Docker in a system unit.** Put `compose.yaml` in `/srv/hlspike/`.

```ini
# /etc/systemd/system/hlspike-creds.service
[Unit]
Requires=docker.service
After=docker.service
[Service]
LoadCredentialEncrypted=db-password:/etc/credstore.encrypted/hlspike.db-password
ExecStartPre=/usr/bin/chcon -t container_file_t -l s0:c100,c200 ${CREDENTIALS_DIRECTORY}/db-password
ExecStart=/usr/bin/docker compose -p hlspike-creds -f /srv/hlspike/compose.yaml up --no-log-prefix secret-mcs
ExecStop=/usr/bin/docker compose -p hlspike-creds -f /srv/hlspike/compose.yaml stop
```

- **The credentials directory**:
  - `findmnt -T /run/credentials/hlspike-creds.service -o TARGET,FSTYPE,OPTIONS`: expect tmpfs with `ro,noswap`, or
    ramfs, mounted where `dockerd` can see it.
  - `ls -lZ` and `getfacl` on it.
  - `systemd-creds list` inside the unit: expect `secure`.
- **The relabel**: does `ExecStartPre=chcon` fail with "Read-only file system"? If it does:
  - try `-v …:z`, expecting the same error;
  - note the files' label;
  - test a policy module instead, `allow container_t <that type>:file { open read getattr };`, built with
    `checkmodule -M -m`, `semodule_package` and `semodule -i`;
  - or copy the credential into a labelled `noswap` tmpfs in `ExecStartPre=`.
- **A drop-in with `PrivateMounts=yes`**: expect Compose to fail with `bind source path does not exist`, because
  `dockerd` can't see the credential.
- **`User=999` with `SupplementaryGroups=docker`**: expect `getfacl` to show `user:999:r--`, and a service with
  `user: "999:999"` to read the credential.
- **The lifecycle**:
  - `systemctl restart`;
  - `systemctl stop`, then `docker start hlspike-creds-secret-mcs-1`: expect a refusal;
  - `systemctl enable`, reboot, and expect the unit to decrypt and start. Then `systemctl disable` and remove the unit.

**3. Rootful Podman**:
- **Its API**: `systemctl start podman.socket`, and the same unit with `Environment=DOCKER_HOST=unix:///run/podman/podman.sock`
  and `Requires=podman.socket`. Repeat the checks in 2.
- **Quadlet**: `/etc/containers/systemd/hlspike-creds-q.container`, made from `spikes/systemd-creds/quadlet/` with
  `LoadCredentialEncrypted=db-password:/etc/credstore.encrypted/hlspike.db-password`.
  - Run `systemctl daemon-reload` and `systemctl start hlspike-creds-q`.
  - Repeat with `PrivateMounts=yes`. Expect it still to work, because podman runs inside the unit.

**4. Rootless Podman, one user per app** (ADR 0008):

```sh
useradd --system --create-home --home-dir /var/lib/hlspike-app --shell /usr/sbin/nologin hlspike-app
usermod --add-subuids 600000-665535 --add-subgids 600000-665535 hlspike-app
loginctl enable-linger hlspike-app
d=/var/lib/hlspike-app/.config
install -d -o hlspike-app -g hlspike-app -m 0700 "$d" "$d/credstore.encrypted" "$d/containers" "$d/containers/systemd"
gen | systemd-creds encrypt --uid=hlspike-app --with-key=host+tpm2 --name=db-password - "$d/credstore.encrypted/db-password"
chown hlspike-app: "$d/credstore.encrypted/db-password"   # expect credhdr: host+tpm2, user-scoped
install -o hlspike-app -m 0644 spikes/systemd-creds/quadlet/hlspike-creds-mcs.container "$d/containers/systemd/"
systemctl --user -M hlspike-app@ daemon-reload
systemctl --user -M hlspike-app@ start hlspike-creds-mcs.service
journalctl _UID="$(id -u hlspike-app)" -n 30
```

In that Quadlet file, `LoadCredentialEncrypted=db-password` has no path, so it finds the file in the user's credstore. Expect the app user's manager to decrypt a TPM-bound user
credential through the varlink service. Then repeat the checks in 2.

**5. Moves and failures**:
- **A move**: copy `/etc/credstore.encrypted/`, the user credstore and `/var/lib/systemd/credential.secret` to the
  other VM, which has another TPM. Expect decryption to fail, and the copied `credential.secret` to be deleted. Then
  re-seal from the plaintext and expect it to work.
- **A new machine ID**: `rm /etc/machine-id && systemd-machine-id-setup`. Expect credentials with a host key or a user
  scope to fail.
- **A cleared TPM** (`tpm2_clear` on swtpm): expect TPM-bound credentials to fail.
- **systemd 262**: credentials made by 259 still decrypt; credentials made by 262 are refused after a downgrade to 259.
- **PCRs** (`--tpm2-pcrs=7`, UEFI VM): try after a kernel update and after toggling Secure Boot.

## Open questions

- **SELinux for system units**: if the read-only credentials file system can't be relabelled, the choice is a policy
  module or a labelled copy.
- **PCR policy**: no PCRs protect against a copied disk but not against a stolen machine. PCR 7 needs UEFI and Secure
  Boot. A signed PCR 11 policy needs UKIs. A legacy BIOS boot, like this PC's, can't use PCR 7 meaningfully.
- **Images that read a secret as another UID** than the one the file is readable for: a floor rule, or a setting per
  app?
- **Apps that take secrets only from environment variables**:
  - a shim that exports `/run/secrets/*` changes the image's entrypoint;
  - putting the secrets in the environment shows them in `docker inspect`.
- **What the unit runs**:
  - the `docker compose` CLI, which is standard and survives an eject;
  - or HomersLab's binary with the pinned Compose SDK, which puts HomersLab in the start path, though not in the
    request path.
- **systemd's own behaviour**: one observation is left out of this document until it has been reported to the systemd
  project privately.

## Side effects on this PC

- **The host key**: the first user-scoped encrypt made systemd's service create `/var/lib/systemd/credential.secret`
  (root, `0400`).
  - It stays, and nothing else uses it.
  - Removing it needs root and would invalidate every credential that uses the host key.
- **The TPM's SRK**: one probe used the TPM through systemd's service. If the TPM had no SRK yet, systemd made one
  persistent at `0x81000001`, the SRK that `systemd-cryptenroll` also uses. To check, as root:
  `systemd-analyze srk >/dev/null && echo present`.
- **Removed**:
  - every unit, container and credentials directory of `hlspike-creds`;
  - the Podman busybox image that the spike pulled;
  - its working files.

  Docker's busybox image was already on the PC, and stays.

## Sources

- systemd 259:
  - [systemd-creds(1)](https://www.freedesktop.org/software/systemd/man/latest/systemd-creds.html);
  - [systemd.exec(5), Credentials](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html#Credentials);
  - [systemd.unit(5), Specifiers](https://www.freedesktop.org/software/systemd/man/latest/systemd.unit.html#Specifiers);
  - [systemd-analyze(1)](https://www.freedesktop.org/software/systemd/man/latest/systemd-analyze.html), `has-tpm2`;
  - [System and Service Credentials](https://systemd.io/CREDENTIALS/).
- systemd source at v259:
  - [creds-util.h](https://github.com/systemd/systemd/blob/v259/src/shared/creds-util.h) and
    [creds-util.c](https://github.com/systemd/systemd/blob/v259/src/shared/creds-util.c);
  - [creds.c](https://github.com/systemd/systemd/blob/v259/src/creds/creds.c);
  - [tpm2-util.h](https://github.com/systemd/systemd/blob/v259/src/shared/tpm2-util.h) and
    [efi-api.c](https://github.com/systemd/systemd/blob/v259/src/shared/efi-api.c);
  - [exec-credential.c](https://github.com/systemd/systemd/blob/v259/src/core/exec-credential.c).
- systemd [NEWS, 262](https://github.com/systemd/systemd/blob/v262/NEWS): SRK pinning.
- Docker:
  - [bind mounts](https://docs.docker.com/engine/storage/bind-mounts/);
  - moby [volumes.go](https://github.com/moby/moby/blob/master/daemon/volumes.go) and
    [mounts.go](https://github.com/moby/moby/blob/master/daemon/volume/mounts/mounts.go);
  - Compose's [services reference](https://docs.docker.com/reference/compose-file/services/#secrets) (`secrets`,
    `volumes`).
- Podman:
  - [podman-run(1)](https://docs.podman.io/en/latest/markdown/podman-run.1.html);
  - [podman-systemd.unit(5)](https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html) (Quadlet).
