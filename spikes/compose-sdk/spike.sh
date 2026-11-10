#!/bin/sh
# Runs a spike binary (built by gorun.sh) on the host, as the calling user, with an empty Docker config.
# Usage: SPIKE_VERSION=v5.5.1|v5.7.0 ./spike.sh [flags] version|load|up|stop|start|ps|ls|inspect|logs|down|volumes
set -eu
S=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$S/out/docker-config" "$S/project/secrets"
# A random test secret (never printed); compose mounts it as the `token` secret.
[ -s "$S/project/secrets/token.txt" ] || (umask 077; head -c 24 /dev/urandom | base64 > "$S/project/secrets/token.txt")
export DOCKER_CONFIG="$S/out/docker-config"
exec "$S/go/bin/spike-${SPIKE_VERSION:-v5.5.1}" "$@"
