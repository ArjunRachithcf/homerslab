#!/bin/sh
# Runs a command in the official golang image with the spike's Go module (go/) and caches (.cache/).
# Build only: no engine socket is mounted (SELinux denies container_t the Docker socket; see the findings).
set -eu
S=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$S/.cache"  # created here, or Docker would create it as root
exec docker run --rm --user "$(id -u):$(id -g)" \
  -v "$S/go:/src:Z" -v "$S/.cache:/cache:Z" \
  -e GOCACHE=/cache/go-build -e GOMODCACHE=/cache/mod -e GOPATH=/cache/gopath -e HOME=/tmp \
  -e GOFLAGS=-modcacherw -e CGO_ENABLED=0 -e GOTOOLCHAIN=local \
  -w /src golang:1.27.3@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e "$@"
