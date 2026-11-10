#!/usr/bin/env bash
# Builds the tsnet process in golang:1.27.3 (the builder service, the only container that
# mounts ./go) and copies the static binary into each run folder.
set -euo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
docker compose -p hlspike-headscale -f "$here/compose.yaml" --profile tools run --rm --no-deps -T \
  --name hlspike-headscale-builder builder \
  sh -c 'go mod tidy && go vet ./... && go build -trimpath -o bin/apps ./cmd/apps'
for d in apps oneoff; do
  mkdir -p "$here/run/$d"
  cp "$here/go/bin/apps" "$here/run/$d/apps"
done
ls -l "$here/go/bin/apps"
