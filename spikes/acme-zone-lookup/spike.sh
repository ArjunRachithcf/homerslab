#!/bin/sh
# Claim (#79), reported in #16: certmagic's DNS-01 solver, with no resolvers set, looks the challenge's zone up through
# the machine's own resolver. Builds go/ in the golang image, then runs it in a container with no network whose only
# resolver is the spike itself on 127.0.0.1, answering like a mesh's local zone (A records, no SOA).
set -eu
S=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$S/.cache"
docker run --rm --user "$(id -u):$(id -g)" -v "$S/go:/src:Z" -v "$S/.cache:/cache:Z" \
  -e GOCACHE=/cache/go-build -e GOMODCACHE=/cache/mod -e HOME=/tmp -e GOFLAGS=-modcacherw -e CGO_ENABLED=0 -e GOTOOLCHAIN=local \
  -w /src golang:1.27.3@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e go build -o claim .
docker run --rm --network none --dns 127.0.0.1 -v "$S/go/claim:/claim:ro,Z" \
  alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f /claim | tee "$S/out/result.txt"
