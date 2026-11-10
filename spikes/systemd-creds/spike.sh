#!/usr/bin/env bash
# HomersLab spike (2026-11-09): app secrets as systemd credentials, handed to Docker and rootless Podman
# containers as files. Findings: docs/research/2026-11-09-spike-systemd-creds.md.
#
# No sudo. Only transient user units named hlspike-creds-*, containers named hlspike-creds-*, compose project
# hlspike-creds, no container networks, dummy secrets. Never prunes. Outputs go to work/ (removed by cleanup).
#
# Usage: spike.sh prep|step1|step2a|step2b|step2c|step2d|step2e|life-docker|life-podman|life-compose|life-quadlet|cleanup
set -u
here=$(cd "$(dirname "$0")" && pwd)
work=$here/work
IMG_DOCKER=busybox@sha256:a46257e06c82ad85242d0d38077d0a52d9896389ca60a1a59d3463db586e5fa4
IMG_PODMAN=docker.io/library/busybox@sha256:a46257e06c82ad85242d0d38077d0a52d9896389ca60a1a59d3463db586e5fa4
CRED=db-password
creds_root=${XDG_RUNTIME_DIR:?}/credentials
READ='id; ls -ln /run/secrets/; if cat /run/secrets/db-password >/dev/null 2>/tmp/err; then echo "READ-OK sha256=$(sha256sum /run/secrets/db-password | cut -c1-12)"; else echo "READ-FAILED: $(cat /tmp/err)"; fi'

say() { printf '\n## %s\n' "$*"; }
dummy() { printf 'DUMMY-hlspike-%s' "$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"; }
hash12() { sha256sum | cut -c1-12; }
hdr() { /usr/bin/python3 -I "$here/credhdr.py" "$@"; }
# SELinux denials since a time that name the spike's credential (other containers on the box are not ours to report).
avc_since() { journalctl _TRANSPORT=audit --since "$1" --no-pager -q 2>/dev/null | grep -E 'avc: +denied' | grep -E '"(/run/secrets/)?db-password"' | sed -E 's/^.*(avc:)/\1/' | sort | uniq -c | head -n 8; }

# encrypt NAME [systemd-creds options...]: a fresh dummy secret, encrypted to work/NAME.cred; its hash in work/NAME.sha
encrypt() {
  local name=$1 s; shift
  s=$(dummy)
  printf %s "$s" | hash12 >"$work/$name.sha"
  printf %s "$s" | systemd-creds "$@" encrypt --name="$name" - "$work/$name.cred"
}

# unit_wait NAME MODE: run probe.sh MODE in a transient user service with the credential, wait, show its output.
unit_wait() {
  systemd-run --user --wait --pipe --collect --quiet --unit="hlspike-creds-$1" \
    -p LoadCredentialEncrypted="$CRED:$work/$CRED.cred" -E HLSPIKE_DIR="$here" \
    /usr/bin/bash "$here/probe.sh" "$2"
  echo "unit exit=$?"
}

# unit_start NAME MODE [props...]: start probe.sh MODE as a long-running transient user service.
unit_start() {
  local name=$1 mode=$2; shift 2
  systemd-run --user --collect --quiet --unit="hlspike-creds-$name" \
    -p LoadCredentialEncrypted="$CRED:$work/$CRED.cred" -E HLSPIKE_DIR="$here" "$@" \
    /usr/bin/bash "$here/probe.sh" "$mode"
}

wait_running() { # ENGINE NAME: up to 15 s
  local i
  for i in $(seq 30); do
    [ "$("$1" inspect -f '{{.State.Running}}' "$2" 2>/dev/null)" = true ] && return 0
    sleep 0.5
  done
  echo "$2 did not start"; return 1
}

state() { # ENGINE NAME
  "$1" inspect -f "$2: {{.State.Status}} (exit {{.State.ExitCode}})" "$2" 2>/dev/null || echo "$2: gone"
}

credir() { # UNIT: the unit's credentials directory as seen from outside
  ls -ld "$creds_root/hlspike-creds-$1.service" 2>&1 | sed "s|$creds_root|\$XDG_RUNTIME_DIR/credentials|"
}

prep() {
  mkdir -p "$work"
  say "versions"
  systemctl --version | head -1
  docker version --format 'docker client {{.Client.Version}}, server {{.Server.Version}}'
  docker compose version
  podman version --format 'podman {{.Client.Version}}'
  echo "SELinux $(getenforce); docker selinux-enabled: $(docker info --format '{{json .SecurityOptions}}')"
  say "podman image (pulled by digest into this user's rootless store if missing; cleanup removes it again)"
  if podman image exists "$IMG_PODMAN"; then echo "already present: leave it"; else
    podman pull -q "$IMG_PODMAN" && touch "$work/podman-image-pulled"
  fi
  docker image inspect "$IMG_DOCKER" --format 'docker image present: {{.Id}}'
}

step1() {
  mkdir -p "$work"
  say "TPM2 support"
  systemd-analyze has-tpm2; echo "exit=$?"
  say "host key before"; ls -l /var/lib/systemd/credential.secret 2>&1
  say "1a systemd-creds --user encrypt (default --with-key=auto)"
  encrypt "$CRED" --user; echo "exit=$?"
  hdr "$work/$CRED.cred"
  say "host key after"; ls -l /var/lib/systemd/credential.secret 2>&1
  say "1b --user with each --with-key="
  for k in host host+tpm2 tpm2 null auto-initrd; do
    n=k-${k/+/-}
    encrypt "$n" --user --with-key="$k" 2>&1; echo "with-key=$k exit=$?"
    [ -s "$work/$n.cred" ] && hdr "$work/$n.cred"
  done
  say "1c decrypt as the same user, outside any unit (sha256 prefix: got / expected)"
  for f in "$work"/*.cred; do
    n=$(basename "$f" .cred)
    printf '%s: %s / %s\n' "$n" "$(systemd-creds --user decrypt --name="$n" "$f" - | hash12)" "$(cat "$work/$n.sha")"
  done
  say "1d decrypt under another credential name (the name is authenticated)"
  systemd-creds --user decrypt --name=other-app-password "$work/$CRED.cred" - >/dev/null; echo "exit=$?"
  say "1e decrypt the user credential in system scope, as an unprivileged user"
  systemd-creds --no-ask-password decrypt "$work/$CRED.cred" - >/dev/null; echo "exit=$?"
  say "1f encrypt a system-scoped credential as an unprivileged user"
  encrypt sys --no-ask-password; echo "exit=$?"
  say "1g encrypt for another user (nobody)"
  encrypt other --no-ask-password --uid=nobody; echo "exit=$?"
  rm -f "$work"/sys.* "$work"/other.*
  say "1h the key types the varlink API offers (none is user-scoped, so 'auto' decides for a user credential)"
  varlinkctl introspect /run/systemd/io.systemd.Credentials io.systemd.Credentials |
    sed -n '/^type WithKey/,/^)/p' | grep -v '^\s*#'
}

step2() { # a|b|c|c-chcon|c-env|d
  local mode ts
  case $1 in a) mode=docker ;; b) mode=podman ;; c) mode=compose ;; c-chcon) mode=compose-chcon ;; c-env) mode=compose-env ;; d) mode=mcs ;; esac
  ts=$(date '+%Y-%m-%d %H:%M:%S')
  unit_wait "$1" "$mode"
  sleep 1
  say "SELinux denials logged during step 2$1 (count, first 8)"
  avc_since "$ts" | grep . || echo "none"
}

# Compose through rootless Podman's Docker-compatible API: the API service runs as its own transient user unit on a
# private socket, and Compose runs in the unit that holds the credential.
step2e() {
  local sock=$work/podman.sock ts i
  say "e podman system service (rootless) on a private socket, as a transient user unit"
  systemd-run --user --collect --quiet --unit=hlspike-creds-podman-api /usr/bin/podman system service --time=0 "unix://$sock"
  for i in $(seq 20); do [ -S "$sock" ] && break; sleep 0.5; done
  ts=$(date '+%Y-%m-%d %H:%M:%S')
  systemd-run --user --wait --pipe --collect --quiet --unit=hlspike-creds-e \
    -p LoadCredentialEncrypted="$CRED:$work/$CRED.cred" -E HLSPIKE_DIR="$here" -E DOCKER_HOST="unix://$sock" \
    /usr/bin/bash "$here/probe.sh" compose-podman
  echo "unit exit=$?"
  systemctl --user stop hlspike-creds-podman-api.service
  sleep 1
  say "SELinux denials logged during step 2e (count, first 8)"
  avc_since "$ts" | grep . || echo "none"
}

life_docker() {
  say "L1 docker: the unit's main process is 'docker run --rm' (foreground, -v :z)"
  unit_start ld1 docker-fg
  wait_running docker hlspike-creds-ld1
  docker exec hlspike-creds-ld1 sh -c "$READ"
  echo "credentials dir: $(credir ld1)"
  echo "secret in 'systemctl --user show': $(systemctl --user show hlspike-creds-ld1.service | grep -c -F -f "$creds_root/hlspike-creds-ld1.service/$CRED" || true) times"
  systemctl --user show hlspike-creds-ld1.service -p LoadCredentialEncrypted -p ControlGroup
  echo "container's cgroup: $(docker inspect -f '{{.HostConfig.CgroupParent}} pid={{.State.Pid}}' hlspike-creds-ld1) -> $(cut -d: -f3 /proc/"$(docker inspect -f '{{.State.Pid}}' hlspike-creds-ld1)"/cgroup)"
  say "L1 restart the unit"
  ino1=$(stat -c %i "$creds_root/hlspike-creds-ld1.service")
  systemctl --user restart hlspike-creds-ld1.service
  wait_running docker hlspike-creds-ld1 && docker exec hlspike-creds-ld1 sh -c "$READ" | tail -n 1
  echo "credentials dir inode before/after restart: $ino1 / $(stat -c %i "$creds_root/hlspike-creds-ld1.service")"
  say "L1 stop the unit"
  systemctl --user stop hlspike-creds-ld1.service
  sleep 2
  state docker hlspike-creds-ld1
  echo "credentials dir: $(credir ld1)"

  say "L2 docker: detached containers left behind by the unit (ld2: --mount, ld3: -v :z)"
  unit_start ld2 docker-detach
  wait_running docker hlspike-creds-ld2 && wait_running docker hlspike-creds-ld3
  for n in ld2 ld3; do printf '%s: ' "$n"; docker exec "hlspike-creds-$n" sh -c "$READ" | tail -n 1; done
  say "L2 docker restart ld2 while the unit runs (source present)"
  docker restart -t 1 hlspike-creds-ld2 >/dev/null; echo "exit=$?"; state docker hlspike-creds-ld2
  say "L2 stop the unit: the credentials directory goes away"
  systemctl --user stop hlspike-creds-ld2.service
  sleep 1
  echo "credentials dir: $(credir ld2)"
  for n in ld2 ld3; do state docker "hlspike-creds-$n"; printf '%s reads: ' "$n"; docker exec "hlspike-creds-$n" sh -c "$READ" 2>&1 | tail -n 1; done
  say "L2 docker restart ld2 (--mount) after the unit stopped"
  docker restart -t 1 hlspike-creds-ld2 2>&1 | tail -n 2; state docker hlspike-creds-ld2
  echo "credentials dir: $(credir ld2)"
  say "L2 ld3 (-v) is removed, never restarted: Docker would recreate its missing source as a root-owned directory"
  docker rm -f hlspike-creds-ld2 hlspike-creds-ld3 >/dev/null; state docker hlspike-creds-ld3

  say "L3 docker -v with a missing source, on a stand-in file in work/ (not a credential)"
  mkdir -p "$work/standin"
  echo "not a secret" >"$work/standin/$CRED"
  docker run -d --name hlspike-creds-ld4 --network none -v "$work/standin/$CRED:/run/secrets/$CRED:ro,z" "$IMG_DOCKER" sh -c 'trap "exit 0" TERM; while :; do sleep 1; done' >/dev/null
  rm "$work/standin/$CRED"
  docker restart -t 1 hlspike-creds-ld4 >/dev/null; echo "restart exit=$?"
  stat -c 'host now has: %F %U:%G %n' "$work/standin/$CRED" | sed "s|$here/||"
  docker exec hlspike-creds-ld4 ls -ld "/run/secrets/$CRED"
  docker rm -f hlspike-creds-ld4 >/dev/null
  rmdir "$work/standin/$CRED" "$work/standin" && echo "stand-in removed"
}

life_podman() {
  say "L4 podman: the unit's main process is 'podman run --rm' (foreground, -v :z)"
  unit_start lp1 podman-fg
  wait_running podman hlspike-creds-lp1
  podman exec hlspike-creds-lp1 sh -c "$READ" | tail -n 1
  systemctl --user show hlspike-creds-lp1.service -p ControlGroup
  echo "container's cgroup: $(cut -d: -f3 /proc/"$(podman inspect -f '{{.State.Pid}}' hlspike-creds-lp1)"/cgroup)"
  echo "conmon's cgroup: $(cut -d: -f3 /proc/"$(podman inspect -f '{{.State.ConmonPid}}' hlspike-creds-lp1)"/cgroup)"
  say "L4 restart the unit"
  ino1=$(stat -c %i "$creds_root/hlspike-creds-lp1.service")
  systemctl --user restart hlspike-creds-lp1.service
  sleep 1
  wait_running podman hlspike-creds-lp1 && podman exec hlspike-creds-lp1 sh -c "$READ" | tail -n 1
  echo "credentials dir inode before/after restart: $ino1 / $(stat -c %i "$creds_root/hlspike-creds-lp1.service")"
  say "L4 stop the unit"
  systemctl --user stop hlspike-creds-lp1.service
  sleep 2
  state podman hlspike-creds-lp1
  echo "credentials dir: $(credir lp1)"

  say "L5 podman: a detached container left behind by the unit (-v :z)"
  unit_start lp2 podman-detach
  wait_running podman hlspike-creds-lp2
  podman exec hlspike-creds-lp2 sh -c "$READ" | tail -n 1
  echo "conmon's cgroup: $(cut -d: -f3 /proc/"$(podman inspect -f '{{.State.ConmonPid}}' hlspike-creds-lp2)"/cgroup)"
  say "L5 podman restart while the unit runs"
  podman restart -t 1 hlspike-creds-lp2 >/dev/null; echo "exit=$?"; state podman hlspike-creds-lp2
  say "L5 stop the unit: the credentials directory goes away"
  systemctl --user stop hlspike-creds-lp2.service
  sleep 1
  echo "credentials dir: $(credir lp2)"
  state podman hlspike-creds-lp2
  printf 'lp2 reads: '; podman exec hlspike-creds-lp2 sh -c "$READ" 2>&1 | tail -n 1
  say "L5 podman restart after the unit stopped"
  podman restart -t 1 hlspike-creds-lp2 2>&1 | tail -n 2; state podman hlspike-creds-lp2
  echo "credentials dir: $(credir lp2)"
  podman rm -f -t 0 hlspike-creds-lp2 >/dev/null; state podman hlspike-creds-lp2
}

life_quadlet() {
  say "L7 Quadlet's generated unit (quadlet/hlspike-creds-q.container, '-dryrun -user'), run as a transient unit"
  QUADLET_UNIT_DIRS=$here/quadlet /usr/libexec/podman/quadlet -dryrun -user 2>/dev/null | grep -E '^(ExecStart|ExecStop|KillMode|Type|Delegate|LoadCredentialEncrypted)='
  # Transient units expand ${VAR} at exec time but not specifiers, hence ${CREDENTIALS_DIRECTORY} for Quadlet's %d.
  systemd-run --user --collect --quiet --unit=hlspike-creds-q \
    -p Type=notify -p NotifyAccess=all -p KillMode=mixed -p Delegate=yes \
    -p LoadCredentialEncrypted="$CRED:$work/$CRED.cred" -p Environment=PODMAN_SYSTEMD_UNIT=hlspike-creds-q.service \
    -p "ExecStop=/usr/bin/podman rm -v -f -i hlspike-creds-q" -p "ExecStopPost=-/usr/bin/podman rm -v -f -i hlspike-creds-q" \
    /usr/bin/podman run --name hlspike-creds-q --replace --rm --cgroups=split --network none --sdnotify=conmon -d \
    --user 999:999 --userns keep-id:uid=999,gid=999 -v '${CREDENTIALS_DIRECTORY}/db-password:/run/secrets/db-password:ro,z' \
    "$IMG_PODMAN" sh -c 'trap "exit 0" TERM; while :; do sleep 1; done'
  echo "start exit=$? state=$(systemctl --user is-active hlspike-creds-q.service)"
  podman exec hlspike-creds-q sh -c "$READ"
  echo "main pid is conmon: $(systemctl --user show -p MainPID --value hlspike-creds-q.service) = $(podman inspect -f '{{.State.ConmonPid}}' hlspike-creds-q)"
  echo "container's cgroup: $(cut -d: -f3 /proc/"$(podman inspect -f '{{.State.Pid}}' hlspike-creds-q)"/cgroup)"
  say "L7 restart the unit"
  ino1=$(stat -c %i "$creds_root/hlspike-creds-q.service")
  systemctl --user restart hlspike-creds-q.service
  podman exec hlspike-creds-q sh -c "$READ" | tail -n 1
  echo "credentials dir inode before/after restart: $ino1 / $(stat -c %i "$creds_root/hlspike-creds-q.service")"
  say "L7 stop the unit"
  systemctl --user stop hlspike-creds-q.service
  state podman hlspike-creds-q
  echo "credentials dir: $(credir q)"
}

life_compose() {
  say "L6 compose: the unit's main process is 'docker compose up' (foreground)"
  unit_start lc compose-fg
  wait_running docker hlspike-creds-secret-file-1
  sleep 2
  docker logs hlspike-creds-secret-file-1 2>&1 | tail -n 1
  say "L6 stop the unit"
  systemctl --user stop hlspike-creds-lc.service
  sleep 2
  state docker hlspike-creds-secret-file-1
  echo "credentials dir: $(credir lc)"
  say "L6 docker start the stopped compose container after the unit stopped"
  docker start hlspike-creds-secret-file-1 2>&1 | tail -n 2; state docker hlspike-creds-secret-file-1
  echo "credentials dir: $(credir lc)"
  docker ps -aq --filter label=com.docker.compose.project=hlspike-creds | xargs -r docker rm -f >/dev/null
  state docker hlspike-creds-secret-file-1
}

cleanup() {
  say "cleanup"
  systemctl --user stop 'hlspike-creds-*' 2>/dev/null
  systemctl --user reset-failed 'hlspike-creds-*' 2>/dev/null
  docker ps -aq --filter name=hlspike-creds- | xargs -r docker rm -f >/dev/null
  docker ps -aq --filter label=com.docker.compose.project=hlspike-creds | xargs -r docker rm -f >/dev/null
  podman ps -aq --filter name=hlspike-creds- | xargs -r podman rm -f -t 0 >/dev/null
  [ -e "$work/podman-image-pulled" ] && podman rmi "$IMG_PODMAN" >/dev/null && echo "removed the podman busybox image this spike pulled"
  rm -rf "$work"
  echo "units:      $(systemctl --user list-units --all --no-legend 'hlspike-creds-*' | wc -l)"
  echo "docker:     $(docker ps -aq --filter name=hlspike-creds- | wc -l) containers, $(docker ps -aq --filter label=com.docker.compose.project=hlspike-creds | wc -l) in the project"
  echo "podman:     $(podman ps -aq --filter name=hlspike-creds- | wc -l) containers"
  echo "creds dirs: $(ls -d "$creds_root"/hlspike-creds-* 2>/dev/null | wc -l)"
}

case ${1:-} in
  prep) prep ;;
  step1) step1 ;;
  step2a) step2 a ;;
  step2b) step2 b ;;
  step2c) step2 c && step2 c-chcon && step2 c-env ;;
  step2d) step2 d ;;
  step2e) step2e ;;
  life-docker) life_docker ;;
  life-podman) life_podman ;;
  life-compose) life_compose ;;
  life-quadlet) life_quadlet ;;
  cleanup) cleanup ;;
  *) sed -n '2,8p' "$0"; exit 2 ;;
esac
