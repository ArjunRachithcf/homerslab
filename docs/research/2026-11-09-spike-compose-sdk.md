# Spike: Docker Compose's Go SDK, pinned, on Docker and rootless Podman

- **Question**: can a Go program use Docker Compose v5's official SDK to load a per-app compose project and run it
  against the local engine? The operations: up (detached), list and health, logs, down with or without volumes, and a
  dry run. How do we pin it, and does the same code work against rootless Podman's Docker-compatible socket? (Spec
  "Open spikes" 4; [ADR 0008](../adr/0008-container-engines-and-host-apps.md).)
- **Checked**: 2026-11-09, on the maintainer's PC, which runs a live homelab (61 containers in project `homelab`).
  Ground rules:
  - only projects named `hlspike-compose*`;
  - ports on 127.0.0.1 only;
  - no sudo. Everything that needs root is in [Needs root](#needs-root-test-in-a-vm-or-ci).
- **Spike**: [`spikes/compose-sdk/`](../../spikes/compose-sdk/):
  - `go/main.go`: one program for every operation;
  - `go/go.mod` (v5.5.1) and `go/go.v5.7.0.mod` (the latest);
  - `go/internal/apicheck/`: code that a minor upgrade breaks;
  - `project/`: the test project and one compose file per failure;
  - `gorun.sh` runs Go in the golang image, and `spike.sh` runs a build;
  - `out/` holds every transcript quoted below.
- **Outcome**: open spike 4 can close with the [pinning rules](#pinning-recommendation). The adapter must guard
  against [four SDK behaviours](#what-the-adapter-must-do-around-the-sdk).

**Verdict**:
- **The SDK does the job.** Everything asked for works through `github.com/docker/compose/v5`, at v5.5.1 and at v5.7.0:
  - load;
  - up, detached, waiting for healthy;
  - ps and health, and project lists;
  - logs, tail and follow;
  - stop and start;
  - down, with or without volumes;
  - dry run.
- **Rootless Podman: the same code works.** Only the endpoint changes. The differences are in
  [error texts, inspect output and two failure modes](#rootless-podman-every-difference).
- **Pin**: pin compose exactly, and take `docker/cli`, `moby/moby` and compose-go from compose's own go.mod. The docs'
  pin, `docker/cli v28.5.2+incompatible`, has been stale since v5.1.0.
- **Upgrading from v5.5.1 to v5.7.0**:
  - it breaks source: `Compose.Port`, `DownOptions.Images` and the hash helpers;
  - it changes how `Up` behaves;
  - it recreated no container, because the config hash is kept stable on purpose.
- **Four traps** for the adapter:
  1. `WithDryRun` combined with any stream option is silently a real run.
  2. The event processor's `Done(operation, success)` reports `success` inverted.
  3. An interrupted `Up` leaves the service under a temporary name. On v5.7.0, the next `Up` returns nil with the
     service stopped.
  4. `file:` secrets are bind-mounted without a relabel:
     - under SELinux, no container can read them;
     - on Podman, a missing file becomes an empty directory.

## Setup

| What | Version | Pin |
|---|---|---|
| Host | Fedora 44, kernel 7.2.11, SELinux enforcing (selinux-policy 44.10, container-selinux 2.251.0) | |
| Docker | docker-ce 29.8.1 (API 1.56, minimum 1.40), containerd 2.3.5, runc 1.5.1, containerd image store | |
| Compose CLI on the host | docker-compose-plugin 5.5.1 | |
| Podman | 5.8.7 rootless (compat API 1.44, minimum 1.24), crun 1.28, conmon 2.2.1, netavark 1.17.2, pasta 2026-07-28 | |
| Go | `golang:1.27.3` (Debian 13), go1.27.3, `GOTOOLCHAIN=local` | `sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e` |
| SDK, the host CLI's version | `github.com/docker/compose/v5 v5.5.1` (2026-09-03) | `h1:saWlMxB0tfQ+FVUlWGIUjLvR0jF5cfTU18KI5zKLD/M=` |
| its docker/cli | `github.com/docker/cli v29.7.2+incompatible` | `h1:dlkwallR8XqfeVnA2ELEhdwvb4lsSwuB4IgsG8Q9cLY=` |
| its other modules | compose-go v2.15.0, moby/moby/client v0.5.1, moby/moby/api v1.55.0 | |
| SDK, the latest v5.x | `github.com/docker/compose/v5 v5.7.0` (2026-11-02) | `h1:nRBxm4HkCFo8Bgb49o6xdhnJ0nl1eGb1H0FAb/ExldE=` |
| its docker/cli | `github.com/docker/cli v29.9.2+incompatible` | `h1:trup/vUxx5uUo2t+87Cd9AmITR+DvKUe4H2tTtZN8FY=` |
| its other modules | compose-go v2.16.2, moby/moby/client v0.6.2, moby/moby/api v1.56.1 | |
| Test image | `busybox:1.38.0` | `sha256:a46257e06c82ad85242d0d38077d0a52d9896389ca60a1a59d3463db586e5fa4` |
| Test image | `traefik/whoami:v1.12.0` | `sha256:c4717a8d1f0134a7444e24f881160e033991f23027c6c5a9a3f8fd22e70d1d44` |

**The program.** One `main.go`, built twice from the same source:
- **Builds**:
  - v5.5.1 with `go build`;
  - v5.7.0 with `go build -modfile=go.v5.7.0.mod -tags compose57`.
- **The code** follows the official example ([docs](https://docs.docker.com/compose/compose-sdk/),
  [docs/sdk.md](https://github.com/docker/compose/blob/v5.7.0/docs/sdk.md)):
  - `command.NewDockerCli()`, then `Initialize(&flags.ClientOptions{Hosts: …})`;
  - `compose.NewComposeService(cli, options…)`;
  - then `LoadProject`, `Up`, `Ps`, `List`, `Logs`, `Stop`, `Start` and `Down`.
- **A guard**: it refuses any project not named `hlspike-compose*`.
- **Version-specific code**: only one error check (`api.IncompatibleOptionsError`, v5.7.0).
- **What a build costs**:
  - with `CGO_ENABLED=0 -trimpath`, static binaries of 40.7 MB (v5.5.1) and 42.9 MB (v5.7.0);
  - about 370 modules and 810–830 packages;
  - a 2.8 GB module and build cache.

**A deviation: the binary ran on the host, not in the golang container.** SELinux denies a confined container the
Docker socket:
```
$ docker run --user 1000:1000 --group-add 960 -v …/go:/src:Z -v /var/run/docker.sock:/var/run/docker.sock golang:1.27.3 …
system_u:system_r:container_t:s0:c473,c919
ls: cannot access '/var/run/docker.sock': Permission denied
curl: (7) Failed to connect to d port 80 after 0 ms: Could not connect to server
```
- **Why not fix it in the container**: the only container-side fix is `--security-opt label=disable`. That runs a
  process holding the engine socket unconfined, so the session's permission policy refused it.
- **What ran instead**:
  - Go built in the golang image, with `go/` and `.cache/` mounted `:Z` and no socket;
  - the static binary ran on the host as the user who runs `docker compose`, with `DOCKER_CONFIG` pointed at an empty
    folder.
- **For HomersLab**: the agent runs on the host, so this doesn't affect it. Any containerised part that talks to an
  engine socket would need its own SELinux policy.

**The test project** ([`project/compose.yaml`](../../spikes/compose-sdk/project/compose.yaml)):
- **`web`**: busybox `httpd`, with:
  - a named volume `data`, where it counts its boots;
  - the secret `token` from `./secrets/token.txt`: random, generated by `spike.sh`, never printed;
  - `cap_drop: [ALL]` plus four capabilities back;
  - a healthcheck that passes only when the secret is readable and httpd answers.
- **`whoami`**: published on `127.0.0.1:18080` (18090 on Podman), with `depends_on: web: service_healthy`.
- **`compose.selinux.yaml`**: an override that mounts the secret's folder once with `bind.selinux: Z`.
- **`errors/*.yaml`**: one compose file per failure.
- **`gpu.yaml`**: a GPU through CDI.

## What happened, step by step (Docker)

### Load

- **No engine needed.** `LoadProject` reads files only, so it works with the engine unreachable.
- **Labels.** It stamps every service with `CustomLabels`:
  - `com.docker.compose.project` and `.service`;
  - `.oneoff=False`;
  - `.project.config_files` and `.project.working_dir`;
  - `.version`.
- **The version label is empty**:
  - Why: `internal.Version` defaults to `"dev"`, which doesn't parse as a version
    ([variables.go](https://github.com/docker/compose/blob/v5.5.1/internal/variables.go)).
  - The fix: `-ldflags "-X github.com/docker/compose/v5/internal.Version=v5.5.1"` made it `5.5.1`.
  - Side effects: none. Custom labels are `json:"-"`, so they aren't in the config hash, and a version change
    recreates nothing.
- **Interpolation reads more than the files**
  ([loader.go](https://github.com/docker/compose/blob/v5.5.1/pkg/compose/loader.go#L81-L117)):
  - the whole process environment;
  - `.env` in the project folder;
  - `COMPOSE_FILE` and `COMPOSE_PROFILES`, when no files or profiles are given.

  Example: `HLSPIKE_REV=2 ./spike.sh up` changed the app.
- **Precedence.** Variables set through `ProjectOptionsFns: cli.WithEnv(…)` beat the OS environment
  ([options.go](https://github.com/compose-spec/compose-go/blob/v2.15.0/cli/options.go#L301-L309)). Variables left
  unset still come from it. (This one is from the source, not run.)

### Up, waiting for healthy

The first run, without the SELinux override (`out/docker-01…`, `docker-02…`):
```
  [event] Container hlspike-compose-web-1    Working Waiting
  [event] Container hlspike-compose-web-1    Error   Error dependency web failed to start
  [event] done up success=true
== up took 15.18s
== ERROR dependency failed to start: container hlspike-compose-web-1 is unhealthy
   chain=[*fmt.wrapError *errors.errorString]
```
- **The cause**: web's last probe was `exit=1 output="head: /run/secrets/token: Permission denied"`.
- **The rest**: `whoami` stayed `created`, never started.

With the override (`out/docker-04…`):
```
  [event] Container hlspike-compose-web-1    Done    Recreated
  [event] Container hlspike-compose-web-1    Done    Healthy
  [event] Container hlspike-compose-whoami-1 Done    Healthy
  [event] done up success=false
== containers before up: 2, after: 2, kept (same ID): 1
hlspike-compose-web-1        web      running   healthy    0
hlspike-compose-whoami-1     whoami   running              0    127.0.0.1:18080->80/tcp
```
- **How `Start.Wait` waits**: for every service, healthy where it has a healthcheck, running otherwise. The event still
  says `Healthy` for whoami, which has no healthcheck.
- **The relabel worked**: `selinux: Z` labelled the secret's folder with the web container's own level,
  `container_file_t:s0:c426,c753`. The named volume kept its data across the recreate.
- **`Done`'s flag is inverted**: `success=false` on success, `true` on failure.
  - Why: `Run` calls `bus.Done(operation, err != nil)`
    ([progress.go#L37](https://github.com/docker/compose/blob/v5.7.0/pkg/compose/progress.go#L37)).
  - Upstream: known ([epic #14074](https://github.com/docker/compose/issues/14074)); the fix
    ([PR #14141](https://github.com/docker/compose/pull/14141)) was closed unmerged on 2026-11-08, so it is still in
    v5.7.0.

### Labels, and what isolates a project

- **On every container**:
  - `com.docker.compose.project`, `.service`, `.container-number`, `.oneoff`;
  - `.config-hash`;
  - `.depends_on`, e.g. `web:service_healthy:false`;
  - `.image`, the engine's local image ID;
  - `.project.config_files`, `.project.working_dir`, `.version`.
- **The filter is exact.** Compose selects containers by exactly `project=<name>` plus the presence of `config-hash`
  ([containers.go#L74](https://github.com/docker/compose/blob/v5.5.1/pkg/compose/containers.go#L74)).
  - Removing `hlspike-compose` left `hlspike-compose-err` running (`out/docker-27…`).
  - Two other spikes' projects, `hlspike-headscale` and `hlspike-kanidm`, ran on the same engine at the same time
    and weren't touched.
- **The labels are the whole ownership model** (`out/docker-28…`):
  - A container I started by hand with only the project label was ignored.
  - One that also carried `com.docker.compose.config-hash=x` was removed as an orphan by the next `up`
    (`RemoveOrphans`).

### A second up, and changes

- **The same files again**: 1.2 s, `Running` events, both container IDs kept.
- **`HLSPIKE_REV=2`**: only `web` was recreated (`Recreate`, then `Recreated`); whoami was kept.
- **A new SDK over old containers**:
  - v5.7.0's `up` over containers made by v5.5.1 kept both, with the same config hashes;
  - this held after the version label changed (`5.5.1` to `5.7.0`).

  v5.7.0 freezes the hash's byte layout precisely so that upgrades don't recreate containers
  ([confighash](https://github.com/docker/compose/blob/v5.7.0/internal/confighash/hash.go)).
- **v5.5.1 with `StartOptions.Project` nil**: it worked, because the start phase rebuilds the project from the labels.
  v5.7.0 ignores that field.

### Logs, stop, start, down

- **Logs**:
  - Lines arrive as `web-1 | secret: readable, 33 bytes`. The consumer gets `service-index`, not the container name.
  - `Follow: true` streams until the context ends, then returns `context deadline exceeded`.
- **Stop**: both containers `exited`. Web exited with 137: busybox as PID 1 ignores SIGTERM, so it was killed after
  10 s.
- **Start**: by project name only, without the files; it waited for healthy again.
- **Down without volumes**: containers and network removed, `hlspike-compose_data` kept. The next up found its boot
  count, 5.
- **Down with volumes**, with the files or by name only: nothing was left with the label.
- **A project that doesn't exist**: `Down`, `Ps` and `Logs` return no error and empty results. Down only logs a
  logrus warning, `No resource found to remove`.

### Dry run

- **`compose.WithDryRun` alone**, with a change pending (`out/docker-10…`). The events show the plan, and the engine
  is untouched:
  ```
    [event] Container hlspike-compose-web-1    Working Recreate
    [event] Container hlspike-compose-web-1    Done    Recreated
    [event] Container 3205a5dadace_hlspike-compose-web-1 Working Starting
  web id before=3205a5dadace after=3205a5dadace   rev=2
  ```
  The events carry no dry-run marker, and they include the temporary name compose uses while it replaces a container.
- **`WithDryRun` plus `WithOutputStream`/`WithErrorStream`: the change is applied** (`out/docker-11…`, `docker-22…`,
  `podman-05…`):
  - What happened: `web id before=3205a5dadace after=e5262590f698`, `rev=3`.
  - Where: both versions, both engines.
  - Why: with a stream option set, `NewComposeService` wraps the *original* CLI and discards the dry-run one
    ([compose.go#L97](https://github.com/docker/compose/blob/v5.7.0/pkg/compose/compose.go#L97) against
    [#L171](https://github.com/docker/compose/blob/v5.7.0/pkg/compose/compose.go#L171)).
  - Why the CLI doesn't show it: it never combines the two options.
  - Upstream: no issue or PR found on 2026-11-09.

### How errors surface

| Case | When | Error (excerpt) | Type or classifier |
|---|---|---|---|
| YAML syntax | load | `failed to parse …/bad-yaml.yaml: … at L4.C13: mapping values are not allowed in this context` | `*libyaml.LoadError`, wrapped |
| Unknown key | load | `validating …: services.web additional properties 'imagee' not allowed` | `schema.validationError`, wrapped |
| Required variable | load | `required variable HLSPIKE_REQUIRED is missing a value: HLSPIKE_REQUIRED must be set` | `*errors.joinError` |
| Bad project name | load | `invalid project name "hlspike-compose-BAD!": must consist only of lowercase alphanumeric characters…` | string |
| Engine unreachable | the first engine call (not init, not load) | `failed to connect to the docker API at unix:///nonexistent/docker.sock; check if the path is correct…` | `client.IsErrConnectionFailed` |
| Image missing | up, before anything is created | `pull access denied for hlspike-compose-does-not-exist, repository does not exist…` | `errdefs.IsNotFound` |
| Port taken | up, after create: the network and container stay | `Bind for 127.0.0.1:18080 failed: port is already allocated` | `errdefs.IsInternal` (HTTP 500) |
| Secret file missing | up, after a logrus warning | `invalid mount config for type "bind": bind source path does not exist: …/missing.txt` | `errdefs.IsInvalidArgument` |
| Unhealthy | up with Wait | `dependency failed to start: container hlspike-compose-web-1 is unhealthy` | string only |
| Wait timeout | up with Wait; the container keeps starting | `application not healthy after 5s` | string only |
| Context deadline | anywhere | `context deadline exceeded` | `context.DeadlineExceeded` |
| Incompatible options | v5.7.0, before anything | `incompatible options (Start.Wait, Start.Attach, Start.OnExit): Wait implies detached mode…` | `*api.IncompatibleOptionsError`; v5.5.1 silently ignored the same options |

- **Warnings** go to logrus's global logger on stderr: "secret file does not exist" and "No resource found to remove".
  They never reach the event processor or the stream options.
- **Health failures carry no type.** The reason is in `Ps` (`unhealthy`) and in the container's health log.

### Interrupted operations

A context deadline in the middle of a forced recreate leaves the replacement beside the running original. The
replacement is created but not started, under compose's temporary name (`out/docker-15…`, `docker-23…`):
```
4cdd1626cfba_hlspike-compose-err-web-1 web      created
hlspike-compose-err-web-1    web      running   starting
```
- **v5.5.1**: the next plain up stops and removes the original and starts the replacement, but never renames it. The
  service runs as `4cdd1626cfba_hlspike-compose-err-web-1`.
- **v5.7.0**: the next plain up removes the original and **returns nil with the service stopped**
  (`40fa779996d2_hlspike-compose-err-web-1  created`). A third up starts it, still under the temporary name.

### Extras

- **Capabilities**: `cap_drop: [ALL]` plus `cap_add` shows in Docker's inspect as
  `CapAdd=[CAP_CHOWN CAP_DAC_OVERRIDE CAP_SETGID CAP_SETUID] CapDrop=[ALL]`.
- **A GPU through CDI**: `devices: [nvidia.com/gpu=all]`, as classic's Ollama uses (`out/docker-26…`):
  - it became `DeviceRequests=[{"Driver":"cdi","DeviceIDs":["nvidia.com/gpu=all"]}]`;
  - the container saw `nvidia0 nvidiactl nvidia-uvm …` and `/usr/bin/nvidia-smi`, and only listed them.

## Rootless Podman: every difference

Podman ran the same binaries, with `-host unix:///run/user/1000/podman/podman.sock` and whoami on 127.0.0.1:18090.
These behaved as on Docker:
- load, up waiting for healthy, an idempotent second up;
- a change, recreating only web;
- the dry run, including the streams bug;
- logs and follow, stop, start;
- down with and without volumes, also by name only.

| | Docker 29.8.1 | Podman 5.8.7, rootless |
|---|---|---|
| API version used | 1.55, v5.5.1's client maximum (v5.7.0's client goes up to 1.56) | 1.44 from both clients, which accept 1.40 and up |
| `ServerVersion().Platform.Name` | `Docker Engine - Community` | `linux/amd64/fedora-44`, component `Podman Engine 5.8.7` |
| Missing secret file | refused: `bind source path does not exist` | **creates an empty directory** at the source path, mounts it and starts the container |
| Port taken | `Bind for 127.0.0.1:18080 failed: port is already allocated` | `rootlessport listen tcp 127.0.0.1:18090: bind: address already in use` |
| Image missing | `pull access denied for …, repository does not exist or may require 'docker login'` | `denied: requested access to the resource is denied` |
| Capabilities in inspect | `CapAdd=[4 caps]`, `CapDrop=[ALL]` | `CapAdd=[]`, `CapDrop=[FOWNER FSETID KILL NET_BIND_SERVICE SETFCAP SETPCAP SYS_CHROOT]`: its default set minus what's kept. The effective set is the same, `CapEff 0xc3` |
| Mounts in inspect | the `Z` bind shows `Mode="ro,Z"`; the secret only in `Mounts` | `Mode=""`; the secret also in `Binds` (`…token.txt:/run/secrets/token:ro,bind,private`) |
| Container states | `exited` | `stopped` for a moment (not a Docker state), then `exited` |
| A stopped container's ports in `Ps` | none | still listed (`127.0.0.1:18090->80/tcp`) |
| Named volume | `/var/lib/docker/volumes/…` | `~/.local/share/containers/storage/volumes/…` |
| `com.docker.compose.image` | `sha256:f97baa53…` | `sha256:aaef90e0…`: each engine's local image ID. The **config hashes are identical** for the same model |
| SELinux and `file:` secrets | unreadable until relabelled; the override works | the same; the override works |
| A GPU through CDI | works | the nodes are injected but unreadable: `ls: /dev/nvidia0: Permission denied` |

The GPU failure on Podman: rootless Podman can't create device nodes, so it bind-mounts the host's. Those are labelled
`xserver_misc_device_t`, and `container_use_xserver_devices` is off on this PC
([Fedora discussion](https://discussion.fedoraproject.org/t/how-to-use-nvidia-container-toolkit-without-disabling-selinux/125970)).
Intel's `/dev/dri/renderD128` is `dri_device_t`, and `container_use_dri_devices` is on.

## API changes from v5.5.1 to v5.7.0

`apidiff -incompatible` ([apidiff](https://pkg.go.dev/golang.org/x/exp/cmd/apidiff)) ran on the export data of both
versions (`out/apidiff-v5.5.1-v5.7.0.txt`):
```
pkg/api
- Compose.Port: removed                       (now Ports(…) (PortPublishers, error), since v5.6.0)
- DownOptions.Images: changed from string to ImagePruneMode                          (v5.7.0)
pkg/compose
- ServiceHash, NetworkHash, VolumeHash: removed                                      (v5.7.0)
- NewImagePruner: gained a *semaphore.Weighted parameter
- CommandMetadata, JsonMessage, ParameterMetadata, ProviderMetadata: now internal/provider types
- ImagePruneMode and its constants: now api.ImagePruneMode
compose-go/v2/types (v2.15.0 to v2.16.2)
- 83 ServiceConfig fields "removed": moved into the embedded ContainerSpec and WorkloadSpec
- ServiceConfig.PreStart: changed from []ServiceHook to []PreStartHook
moby/moby/client (v0.5.1 to v0.6.2)
- MaxAPIVersion: value changed from "1.55" to "1.56"
```
- **What compiled.** The spike's own code compiled against both versions unchanged, including its `docker/cli` calls
  (`NewDockerCli`, `Initialize`, `ClientOptions`).
- **What didn't.** Code written for v5.5.1
  ([`internal/apicheck`](../../spikes/compose-sdk/go/internal/apicheck/apicheck.go)) fails on v5.7.0:
  ```
  svc.Port undefined (type api.Compose has no field or method Port)
  cannot use mode (variable of type string) as api.ImagePruneMode value in struct literal
  ```
- **Struct literals.** `types.ServiceConfig{Name: …, Image: …}` still compiles, but only because the module's `go`
  line is 1.27: Go 1.27 accepts promoted fields as keys in struct literals ([Go 1.27](https://go.dev/doc/go1.27)).
  With `go 1.26.10` it fails:
  `use of promoted field ContainerSpec.Image in struct literal … requires go1.27 or later`.
- **The release notes miss a break.** `Port` became `Ports` in v5.6.0
  ([#13577](https://github.com/docker/compose/pull/13577), "PRIVATE_PORT optional for `docker compose port`"), and
  [v5.6.0's notes](https://github.com/docker/compose/releases/tag/v5.6.0) have no library note. Only
  [v5.7.0's](https://github.com/docker/compose/releases/tag/v5.7.0) has one.
- **Behaviour changes in v5.7.0**:
  - `Up` runs one plan over one daemon snapshot;
  - it validates its options first (see the error table);
  - it ignores `StartOptions.Project`;
  - a container that exits between the plan and the start isn't started again;
  - the interactive menu needs `WithNavigationMenu`.

  Here, only validation and the interrupted recreate behaved differently.
- **The toolchain floor rises with compose.** Compose's `go` line: 1.24.9 (v5.0.0), 1.26.3 (v5.5.1), 1.26.10
  (v5.7.0).

## The pin, and the docs

**docs.docker.com is stale.** On 2026-11-09 its "Requirements" still say to pin
`github.com/docker/cli v28.5.2+incompatible`, "whereas Docker Compose v5 currently depends on `github.com/docker/docker`"
([docs](https://docs.docker.com/compose/compose-sdk/),
[source history](https://github.com/docker/docs/commits/main/content/manuals/compose/compose-sdk.md)). That held for
v5.0.x only. The repository's own [docs/sdk.md](https://github.com/docker/compose/blob/v5.7.0/docs/sdk.md) no longer
has the section. What compose's go.mod requires at each tag:

| compose | docker/cli | moby | go line |
|---|---|---|---|
| [v5.0.0](https://github.com/docker/compose/blob/v5.0.0/go.mod) | v28.5.2 | docker/docker v28.5.2 | 1.24.9 |
| [v5.1.0](https://github.com/docker/compose/blob/v5.1.0/go.mod) | v29.2.1 | moby/moby/client v0.2.2 | 1.25.0 |
| [v5.5.1](https://github.com/docker/compose/blob/v5.5.1/go.mod) | v29.7.2 | client v0.5.1, api v1.55.0 | 1.26.3 |
| [v5.6.0](https://github.com/docker/compose/blob/v5.6.0/go.mod) | v29.8.2 | client v0.6.1, api v1.56.1 | 1.26.8 |
| [v5.7.0](https://github.com/docker/compose/blob/v5.7.0/go.mod) | v29.9.2 | client v0.6.2, api v1.56.1 | 1.26.10 |

What happened when I tried the docs' pin and `go get -u` (`out/pinning-checks.txt`):
- **The docs' line, as written**, with v5.5.1: `go mod tidy` silently raised the require to v29.7.2 (minimal version
  selection). The line does nothing.
- **Forced with `replace … => v28.5.2+incompatible`**: the module graph doesn't resolve
  (`github.com/docker/docker/api@v1.56.1: … module declares its path as: github.com/moby/moby/api`), and nothing
  builds.
- **`go get -u ./...` on the v5.5.1 module**:
  - it moved compose to v5.7.0;
  - it moved docker/cli to v29.9.3, newer than any compose release requires;
  - it still built.

  One careless command changes the pin.
- **Why docker/cli can't be avoided**: `NewComposeService` takes a docker/cli `command.Cli`, still in v5.7.0. The
  v5.0.0 note "without requiring Docker CLI" means without the `docker` binary, not without the module
  ([v5.0.0](https://github.com/docker/compose/releases/tag/v5.0.0)).

## The adapter contract: what is proven

| Contract item | Docker | Rootless Podman | Through |
|---|---|---|---|
| Run a compose project, detached, wait for healthy | yes | yes | `LoadProject`, then `Up` with `Start.Wait` |
| Converge after a change; an idempotent re-run | yes | yes | `Up` with `Recreate: diverged`; the config hash |
| Stop and start without removing | yes | yes | `Stop`, `Start` (by name only) |
| Remove, keeping or deleting data | yes | yes | `Down` with or without `Volumes`, from the files or by name |
| Inspect: list projects | yes | yes | `List` (`running(2)`, the config files) |
| Inspect: containers and health | yes | yes | `Ps` (`State`, `Health`, published ports); the raw inspect for the probe log |
| Logs | yes | yes | `Logs` with `Tail`, and with `Follow` |
| Progress for the UI | yes | yes | `WithEventProcessor`, ignoring `Done`'s flag |
| Plan preview | partly | partly | `WithDryRun`, never with stream options |
| Capability extras | yes | yes, with a different inspect output | `cap_add`, `cap_drop` |
| Secrets as files | with a relabel | with a relabel; a missing file is not an error | `secrets: file:` |
| NVIDIA GPU through CDI | yes | **no**, not without an SELinux change (root) | `devices: [nvidia.com/gpu=all]` |
| Intel render devices | not tested | not tested | |
| Rootful Podman | | needs root | |

## Pinning recommendation

1. **Pin compose exactly** in go.mod: `github.com/docker/compose/v5 vX.Y.Z`. Let docker/cli, moby/moby/client,
   moby/moby/api and compose-go come from compose's own go.mod: no `replace`, and no `require` above them.
2. **Guard it in CI.** A test fails when the build list's docker/cli, moby and compose-go versions differ from what
   the pinned compose requires (from `go mod graph`, or `debug.ReadBuildInfo` in a test). Otherwise `go get -u` or
   another dependency would move them.
3. **Upgrade them as one group**: compose plus those modules, in a PR of their own, as one Renovate or Dependabot
   group. Never a blanket `go get -u`.
4. **Gate each upgrade on**:
   - `apidiff -incompatible` of `pkg/api`, `pkg/compose` and compose-go's `types`, with the output in the PR;
   - the container-engine contract suite on Docker, rootful Podman and rootless Podman, including this spike's cases:
     dry run, interrupted up, missing secret, idempotency;
   - a golden test of config hashes: the catalog's apps must keep their hashes, or the upgrade restarts every app;
   - the release notes and `docs/sdk.md` (the docs site lags).
5. **Keep compose inside the container-engine adapter.** Nothing else imports it. Use only `pkg/api`,
   `compose.NewComposeService` and its options. Keep the `go` line at 1.27 or later, so that compose-go's struct
   layering doesn't break literals.
6. **Stamp the version** at build time:
   `-ldflags "-X github.com/docker/compose/v5/internal.Version=<the pinned version>"`.
7. **Raise the Go toolchain with compose.** Compose's `go` line follows Go's security releases.
8. **Start at v5.7.0**: proven here, with typed option validation. Then follow minor releases deliberately, through
   the gate.

## What the adapter must do around the SDK

- **Engines**: one `DockerCli` and one `NewComposeService` per engine endpoint, with `DOCKER_CONFIG` pointed at the
  agent's own folder.
- **Project names**:
  - always set `ProjectName`, from a namespace of HomersLab's own (for example `homerslab-<app>`);
  - before the first install, refuse if any container already carries that project label.
- **Interpolation**:
  - pass every variable through `ProjectOptionsFns`;
  - run with a minimal environment;
  - keep no `.env` in an app's folder that HomersLab didn't write.
- **Dry run**: never with stream options. Until that's fixed upstream, check after a preview that nothing changed.
- **Results**: ignore `Done`'s flag and use the error `Up` returns.
- **Interruptions**:
  - don't cancel an `Up`; give it a generous context;
  - after any error, check with `Ps` that every service runs, healthy, under its canonical name;
  - repair with an `Up` that forces recreation (to be verified, see the open questions).
- **Secrets**: check that every secret file exists before `Up`, because Podman won't.
- **Logging**: route logrus into the agent's log (`logrus.SetOutput`, or a hook).
- **States**: treat unknown container states, such as Podman's `stopped`, as not running.
- **SELinux**: label each secret with the app's MCS level, and run the app's containers at that level, as the
  [systemd-credentials spike](2026-11-09-spike-systemd-creds.md) found. The `selinux: Z` folder bind used here works,
  but every new container relabels the folder with a new random level.

## Needs root: test in a VM or CI

- **Rootful Podman**, through `/run/podman/podman.sock`.
- **Rootless Podman with one unprivileged user per app** ([ADR 0008](../adr/0008-container-engines-and-host-apps.md)):
  - the users themselves, and lingering;
  - one socket per user;
  - the root agent reaching each user's socket under SELinux.
- **NVIDIA through CDI on rootless Podman**: which SELinux boolean or policy module lets the device nodes through,
  without `label=disable`.
- **The agent as a system service**: its SELinux domain reaching `/var/run/docker.sock` and the per-user Podman
  sockets.

## Open questions

- **Concurrency**: can one process drive several projects at once? logrus and `api.Separator` are global. v5.7.0 moved
  other state into the service.
- **Repair after an interrupted `Up`**: does a forced recreate restore the canonical names, on both versions?
- **Reporting upstream**: `WithDryRun` with streams; Podman creating a missing bind source behind the Mounts API.
- **Remote engines** (SSH, TCP): not needed, because each node's agent talks to its local engine. Not tested.
- **API floors**: the moby clients accept API 1.40 and up, and Podman 5.8 reports 1.44. A later client that raises
  its floor past 1.44 would lock Podman out.
- **Intel render devices**, on both engines.

## Side effects on this PC

- **Images pulled**:
  - on Docker: `golang:1.27.3`, `busybox:1.38.0` and `traefik/whoami:v1.12.0`;
  - on Podman: whoami (busybox was already there).

  None were removed: no pruning, and another spike uses `golang:1.27.3`.
- **Podman's socket**: `podman.socket` (user unit) was started and stopped twice. It's inactive and disabled, as
  before. Its socket file stays at `/run/user/1000/podman/podman.sock`, but nothing listens on it.
- **SELinux labels** changed by the engines' `Z` relabels, inside the spike folder only.
- **Removed at the end**: `.cache/` (2.8 GB), the binaries, the test secret and every `hlspike-compose*` container,
  volume and network on both engines. `spike.sh` makes a new secret when needed.
- **The homelab**: still 61 containers in `homelab`, never touched.

**Re-running**:
```
cd ~/homerslab/spikes/compose-sdk
./gorun.sh go build -trimpath -ldflags "-X github.com/docker/compose/v5/internal.Version=v5.5.1" -o bin/spike-v5.5.1 .
./gorun.sh go build -modfile=go.v5.7.0.mod -tags compose57 -trimpath -o bin/spike-v5.7.0 .
./spike.sh -events -f project/compose.yaml,project/compose.selinux.yaml up
./spike.sh -v -f project/compose.yaml,project/compose.selinux.yaml down
```
For Podman: `systemctl --user start podman.socket`, then add `-host unix:///run/user/1000/podman/podman.sock` and
set `HLSPIKE_PORT=18090`.

## Sources

- Compose SDK: [docs.docker.com](https://docs.docker.com/compose/compose-sdk/) and its
  [source](https://github.com/docker/docs/blob/main/content/manuals/compose/compose-sdk.md); the repository's
  [docs/sdk.md](https://github.com/docker/compose/blob/v5.7.0/docs/sdk.md).
- Releases: [v5.0.0](https://github.com/docker/compose/releases/tag/v5.0.0),
  [v5.5.1](https://github.com/docker/compose/releases/tag/v5.5.1),
  [v5.6.0](https://github.com/docker/compose/releases/tag/v5.6.0),
  [v5.6.1](https://github.com/docker/compose/releases/tag/v5.6.1),
  [v5.7.0](https://github.com/docker/compose/releases/tag/v5.7.0).
- Compose source, by tag:
  - [compose.go](https://github.com/docker/compose/blob/v5.7.0/pkg/compose/compose.go): `NewComposeService`,
    `WithDryRun`;
  - [progress.go](https://github.com/docker/compose/blob/v5.7.0/pkg/compose/progress.go): `Done`;
  - [create.go](https://github.com/docker/compose/blob/v5.5.1/pkg/compose/create.go#L1254): secret mounts;
  - [secrets.go](https://github.com/docker/compose/blob/v5.5.1/pkg/compose/secrets.go): `environment`/`content`
    secrets copied in;
  - [loader.go](https://github.com/docker/compose/blob/v5.5.1/pkg/compose/loader.go);
  - [containers.go](https://github.com/docker/compose/blob/v5.5.1/pkg/compose/containers.go#L74);
  - [start.go](https://github.com/docker/compose/blob/v5.5.1/pkg/compose/start.go#L77-L96);
  - [confighash](https://github.com/docker/compose/blob/v5.7.0/internal/confighash/hash.go);
  - [variables.go](https://github.com/docker/compose/blob/v5.5.1/internal/variables.go),
    [Makefile](https://github.com/docker/compose/blob/v5.5.1/Makefile).
- Upstream: [epic #14074](https://github.com/docker/compose/issues/14074),
  [PR #14141](https://github.com/docker/compose/pull/14141), [PR #13577](https://github.com/docker/compose/pull/13577).
- compose-go: [types.go v2.16.2](https://github.com/compose-spec/compose-go/blob/v2.16.2/types/types.go),
  [cli/options.go v2.15.0](https://github.com/compose-spec/compose-go/blob/v2.15.0/cli/options.go).
- [moby/moby/client v0.6.2](https://pkg.go.dev/github.com/moby/moby/client@v0.6.2): API 1.40 to 1.56.
- Go: [Go 1.27 release notes](https://go.dev/doc/go1.27), [apidiff](https://pkg.go.dev/golang.org/x/exp/cmd/apidiff),
  [golang image](https://hub.docker.com/_/golang).
- Podman: [podman system service](https://docs.podman.io/en/latest/markdown/podman-system-service.1.html).
- SELinux and NVIDIA:
  [Fedora discussion](https://discussion.fedoraproject.org/t/how-to-use-nvidia-container-toolkit-without-disabling-selinux/125970).
- HomersLab: [spec](../spec.md) ("Open spikes", "Adapter contracts"),
  [APIs research](2026-11-09-apis.md#docker-on-several-machines-and-the-compose-sdk),
  [systemd-credentials spike](2026-11-09-spike-systemd-creds.md).
