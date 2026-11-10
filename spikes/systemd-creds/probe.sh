#!/usr/bin/env bash
# Runs INSIDE a transient user unit that spike.sh starts with LoadCredentialEncrypted=db-password:...
# Prints what the unit and its containers see. Never prints the secret, only a 12-character sha256 prefix.
# Only containers named hlspike-creds-* and compose project hlspike-creds; no network for any of them.
set -u
mode=${1:?usage: probe.sh MODE}
here=${HLSPIKE_DIR:?set by spike.sh}
c=${CREDENTIALS_DIRECTORY:?not started with LoadCredentialEncrypted=}
f=$c/db-password
IMG_DOCKER=busybox@sha256:a46257e06c82ad85242d0d38077d0a52d9896389ca60a1a59d3463db586e5fa4
IMG_PODMAN=docker.io/library/busybox@sha256:a46257e06c82ad85242d0d38077d0a52d9896389ca60a1a59d3463db586e5fa4
DOCKER=/usr/bin/docker
PODMAN=/usr/bin/podman
COMPOSE=("$DOCKER" compose -p hlspike-creds -f "$here/compose.yaml")
want=$(sha256sum <"$f" | cut -c1-12)

# What a container runs: who it is, the file as it sees it, and whether it can read it (hash prefix only).
READ='id; ls -ln /run/secrets/; if cat /run/secrets/db-password >/dev/null 2>/tmp/err; then echo "READ-OK sha256=$(sha256sum /run/secrets/db-password | cut -c1-12)"; else echo "READ-FAILED: $(cat /tmp/err)"; fi'
LOOP='trap "exit 0" TERM; while :; do sleep 1; done'

say() { printf '\n## %s\n' "$*"; }
label() { ls -Z "$f" | cut -d' ' -f1; }
# Count occurrences of the secret in stdin (the secret file itself is the grep pattern, so it is never echoed).
leaks() { printf '%s: secret found %s times\n' "$1" "$(grep -c -F -f "$f" || true)"; }

unit_view() {
  say "unit view ($mode)"
  echo "CREDENTIALS_DIRECTORY=$c, secret sha256=$want"
  stat -c '%A %U:%G %n' "$c" "$f"
  echo "label: dir $(ls -dZ "$c" | cut -d' ' -f1), file $(label)"
  findmnt -T "$c" -o TARGET,SOURCE,FSTYPE,OPTIONS
  systemd-creds list --no-pager
}

drun() { # NAME ARGS... : docker run --rm of the reader with extra args
  local name=$1; shift
  "$DOCKER" run --rm --name "$name" --network none "$@" "$IMG_DOCKER" sh -c "$READ" 2>&1
  echo "exit=$? label now: $(label)"
}

prun() { # NAME ARGS... : rootless podman run --rm of the reader with extra args
  local name=$1; shift
  "$PODMAN" run --rm --name "$name" --network none "$@" "$IMG_PODMAN" sh -c "$READ" 2>&1
  echo "exit=$? label now: $(label)"
}

case $mode in
  docker)
    unit_view
    say "a1 docker --mount, readonly, no relabel"
    drun hlspike-creds-a1 --mount "type=bind,src=$f,dst=/run/secrets/db-password,readonly"
    say "a2 docker -v :ro,z (shared relabel)"
    drun hlspike-creds-a2 -v "$f:/run/secrets/db-password:ro,z"
    say "a3 docker --mount again, after the z relabel"
    drun hlspike-creds-a3 --mount "type=bind,src=$f,dst=/run/secrets/db-password,readonly"
    say "a4 docker -v :ro,Z (private relabel)"
    drun hlspike-creds-a4 -v "$f:/run/secrets/db-password:ro,Z"
    say "a5 docker --mount after the Z relabel (another container, other MCS categories)"
    drun hlspike-creds-a5 --mount "type=bind,src=$f,dst=/run/secrets/db-password,readonly"
    say "a6 docker --user 999:999 (not the file's owner), :z"
    drun hlspike-creds-a6 --user 999:999 -v "$f:/run/secrets/db-password:ro,z"
    say "a7 docker --user 1000:1000 (the file's owner), :z"
    drun hlspike-creds-a7 --user 1000:1000 -v "$f:/run/secrets/db-password:ro,z"
    say "a8 docker --mount with a missing source (a credential that was not loaded)"
    drun hlspike-creds-a8 --mount "type=bind,src=$c/not-loaded,dst=/run/secrets/not-loaded,readonly"
    say "a9 docker inspect of a running container"
    "$DOCKER" run -d --name hlspike-creds-a9 --network none -v "$f:/run/secrets/db-password:ro,z" "$IMG_DOCKER" sh -c "$LOOP" >/dev/null
    out=$("$DOCKER" inspect hlspike-creds-a9)
    leaks "docker inspect" <<<"$out"
    jq -c '.[0] | {Path, Args, Env: .Config.Env, Binds: .HostConfig.Binds, Mounts: [.Mounts[] | {Type, Source, Destination, Mode, RW}], MountLabel, ProcessLabel}' <<<"$out"
    "$DOCKER" rm -f hlspike-creds-a9 >/dev/null
    ;;

  podman)
    unit_view
    say "b1 podman -v :ro, no relabel"
    prun hlspike-creds-b1 -v "$f:/run/secrets/db-password:ro"
    say "b2 podman --mount type=bind,ro,relabel=shared"
    prun hlspike-creds-b2 --mount "type=bind,src=$f,dst=/run/secrets/db-password,ro=true,relabel=shared"
    say "b3 podman -v :ro,Z (private relabel)"
    prun hlspike-creds-b3 -v "$f:/run/secrets/db-password:ro,Z"
    say "b4 podman --user 999:999 (a subuid on the host), :z"
    prun hlspike-creds-b4 --user 999:999 -v "$f:/run/secrets/db-password:ro,z"
    say "b5 podman --userns=keep-id:uid=999,gid=999 --user 999:999 (the unit's user is 999 inside), :z"
    prun hlspike-creds-b5 --userns=keep-id:uid=999,gid=999 --user 999:999 -v "$f:/run/secrets/db-password:ro,z"
    say "b6 podman -v with a missing source"
    prun hlspike-creds-b6 -v "$c/not-loaded:/run/secrets/not-loaded:ro,z"
    say "b7 podman inspect of a running container"
    "$PODMAN" run -d --name hlspike-creds-b7 --network none -v "$f:/run/secrets/db-password:ro,z" "$IMG_PODMAN" sh -c "$LOOP" >/dev/null
    out=$("$PODMAN" inspect hlspike-creds-b7)
    leaks "podman inspect" <<<"$out"
    jq -c '.[0] | {Path, Args, Env: .Config.Env, Binds: .HostConfig.Binds, Mounts: [.Mounts[] | {Type, Source, Destination, Options, RW}], MountLabel, ProcessLabel}' <<<"$out"
    "$PODMAN" rm -f -t 0 hlspike-creds-b7 >/dev/null
    ;;

  compose)
    unit_view
    say "c0 docker compose config (secrets and bind-z volumes)"
    cfg=$("${COMPOSE[@]}" config 2>&1)
    leaks "compose config" <<<"$cfg"
    "${COMPOSE[@]}" config --format json 2>/dev/null | jq -c '{secrets, "secret-mode": .services["secret-mode"].secrets, "bind-z": .services["bind-z"].volumes}'
    for svc in secret-file secret-mode bind-z bind-z-create short-z secret-file; do
      say "c1 compose up -d $svc (label before: $(label))"
      "${COMPOSE[@]}" up -d --force-recreate --no-deps "$svc" 2>&1 | grep -v -E '^ *(Container|Network) .* (Creat|Start|Recreat|Runn)' || true
      sleep 2
      "${COMPOSE[@]}" logs --no-log-prefix "$svc" 2>&1 | tail -n 4
      echo "label now: $(label)"
    done
    say "c2 docker inspect of the compose containers"
    for svc in secret-file secret-mode bind-z bind-z-create short-z; do
      out=$("$DOCKER" inspect "hlspike-creds-$svc-1")
      leaks "inspect $svc" <<<"$out"
      jq -c --arg s "$svc" '.[0] | {service: $s, Env: .Config.Env, Binds: .HostConfig.Binds, Mounts: [.HostConfig.Mounts[]? | {Type, Source, Target, ReadOnly, BindOptions}]}' <<<"$out"
    done
    "${COMPOSE[@]}" down -t 2 2>&1 | grep -v -E '^ *Container .* (Stopp|Remov)' || true
    ;;

  mcs)
    # One MCS level per app: the unit labels its own credential, the engines never relabel.
    unit_view
    lvl=s0:c100,c200
    say "d0 chcon -t container_file_t -l $lvl on the credential, as the unit's user"
    chcon -t container_file_t -l "$lvl" "$f" 2>&1; echo "exit=$? label now: $(label)"
    say "d1 docker --mount, container at the app's level"
    drun hlspike-creds-d1 --security-opt "label=level:$lvl" --mount "type=bind,src=$f,dst=/run/secrets/db-password,readonly"
    say "d2 docker --mount, container at its own random level (another app)"
    drun hlspike-creds-d2 --mount "type=bind,src=$f,dst=/run/secrets/db-password,readonly"
    say "d3 rootless podman --mount without relabel, container at the app's level"
    prun hlspike-creds-d3 --security-opt "label=level:$lvl" --mount "type=bind,src=$f,dst=/run/secrets/db-password,ro=true"
    say "d4 rootless podman, container at its own random level"
    prun hlspike-creds-d4 --mount "type=bind,src=$f,dst=/run/secrets/db-password,ro=true"
    say "d5 compose secrets: with security_opt label=level:$lvl"
    HLSPIKE_MCS=$lvl "${COMPOSE[@]}" up -d --force-recreate --no-deps secret-mcs 2>&1 | grep -v -E '^ *(Container|Network) .* (Creat|Start|Recreat|Runn)' || true
    sleep 2
    "${COMPOSE[@]}" logs --no-log-prefix secret-mcs 2>&1 | tail -n 1
    "$DOCKER" inspect -f 'process label: {{.ProcessLabel}}' hlspike-creds-secret-mcs-1
    "${COMPOSE[@]}" down -t 2 2>&1 | grep -v -E '^ *Container .* (Stopp|Remov)' || true
    ;;

  compose-podman)
    # Docker Compose against rootless Podman's Docker-compatible API (DOCKER_HOST, set by spike.sh step2e).
    unit_view
    lvl=s0:c100,c200
    say "e0 chcon -t container_file_t -l $lvl on the credential; DOCKER_HOST=${DOCKER_HOST:?}"
    chcon -t container_file_t -l "$lvl" "$f" 2>&1; echo "exit=$? label now: $(label)"
    "$DOCKER" version --format 'engine behind DOCKER_HOST: {{.Server.Platform.Name}} {{.Server.Version}}'
    for svc in secret-mcs secret-file; do
      say "e1 compose up -d $svc (through Podman's API)"
      HLSPIKE_MCS=$lvl "${COMPOSE[@]}" up -d --force-recreate --no-deps "$svc" 2>&1 | grep -v -E '^ *(Container|Network) .* (Creat|Start|Recreat|Runn)' || true
      sleep 2
      "${COMPOSE[@]}" logs --no-log-prefix "$svc" 2>&1 | tail -n 1
      out=$("$PODMAN" inspect "hlspike-creds-$svc-1")
      leaks "podman inspect $svc" <<<"$out"
      jq -c '.[0] | {ProcessLabel, Mounts: [.Mounts[] | {Type, Source, Destination}]}' <<<"$out"
    done
    "${COMPOSE[@]}" down -t 2 2>&1 | grep -v -E '^ *Container .* (Stopp|Remov)' || true
    ;;

  compose-env)
    unit_view
    say "c4 compose secret from an environment variable (uid/gid/mode 999:999 0400), the value read from the credential"
    envc=("$DOCKER" compose -p hlspike-creds -f "$here/compose-env.yaml")
    HLSPIKE_DB=$(cat "$f") "${envc[@]}" up -d env-secret 2>&1 | grep -v -E '^ *(Container|Network) .* (Creat|Start|Recreat|Runn)' || true
    sleep 2
    "${envc[@]}" logs --no-log-prefix env-secret 2>&1 | tail -n 4
    out=$("$DOCKER" inspect hlspike-creds-env-secret-1)
    leaks "docker inspect" <<<"$out"
    echo "docker diff (files the container's writable layer gained):"
    "$DOCKER" diff hlspike-creds-env-secret-1
    jq -c '.[0] | {Env: .Config.Env, Mounts: [.Mounts[] | {Type, Destination}], GraphDriver: .GraphDriver.Name}' <<<"$out"
    "${envc[@]}" down -t 2 2>&1 | grep -v -E '^ *Container .* (Stopp|Remov)' || true
    ;;

  compose-chcon)
    # The unit relabels its own credential before Compose starts (what an ExecStartPre= could do).
    unit_view
    say "c3 chcon -t container_file_t -l s0 on the credential, as the unit's user"
    chcon -t container_file_t -l s0 "$f" 2>&1; echo "exit=$? label now: $(label)"
    say "c3 compose up -d secret-file"
    "${COMPOSE[@]}" up -d --force-recreate --no-deps secret-file 2>&1 | grep -v -E '^ *(Container|Network) .* (Creat|Start|Recreat|Runn)' || true
    sleep 2
    "${COMPOSE[@]}" logs --no-log-prefix secret-file 2>&1 | tail -n 1
    "${COMPOSE[@]}" down -t 2 2>&1 | grep -v -E '^ *Container .* (Stopp|Remov)' || true
    ;;

  # Long-running modes for the lifecycle tests (spike.sh life-*): the unit's main process owns the container,
  # or the unit leaves a detached container behind and sleeps, so the credentials stay until the unit stops.
  docker-fg)
    exec "$DOCKER" run --rm --name hlspike-creds-ld1 --network none -v "$f:/run/secrets/db-password:ro,z" "$IMG_DOCKER" sh -c "$LOOP"
    ;;
  docker-detach)
    "$DOCKER" run -d --name hlspike-creds-ld2 --network none --mount "type=bind,src=$f,dst=/run/secrets/db-password,readonly" "$IMG_DOCKER" sh -c "$LOOP"
    "$DOCKER" run -d --name hlspike-creds-ld3 --network none -v "$f:/run/secrets/db-password:ro,z" "$IMG_DOCKER" sh -c "$LOOP"
    exec sleep infinity
    ;;
  podman-fg)
    exec "$PODMAN" run --rm --name hlspike-creds-lp1 --network none -v "$f:/run/secrets/db-password:ro,z" "$IMG_PODMAN" sh -c "$LOOP"
    ;;
  podman-detach)
    "$PODMAN" run -d --name hlspike-creds-lp2 --network none -v "$f:/run/secrets/db-password:ro,z" "$IMG_PODMAN" sh -c "$LOOP"
    exec sleep infinity
    ;;
  compose-fg)
    chcon -t container_file_t -l s0 "$f"
    exec "${COMPOSE[@]}" up --no-log-prefix secret-file
    ;;
  *)
    echo "unknown mode $mode" >&2
    exit 2
    ;;
esac
