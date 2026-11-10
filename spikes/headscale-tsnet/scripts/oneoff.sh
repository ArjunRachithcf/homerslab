#!/usr/bin/env bash
# Runs the tsnet binary once in the oneoff service (./run/oneoff mounted at /work), always as
# the container hlspike-headscale-oneoff: a second run at the same time fails on the name
# before Docker relabels anything.
#   oneoff.sh [-d] ARGS...      -d: detached (stop it with: docker stop hlspike-headscale-oneoff)
set -euo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
detach=()
if [ "${1:-}" = -d ]; then detach=(-d); shift; fi
exec docker compose -p hlspike-headscale -f "$here/compose.yaml" --profile tools run --rm --no-deps -T \
  "${detach[@]}" --name hlspike-headscale-oneoff oneoff /work/apps "$@"
