#!/bin/sh
# Pulls the images a job's containers run, before its tests do, so a registry's refusal can't fail the tests (#222).
# Each argument is a file; every image reference pinned by digest in it (a compose file's `image:` lines, a Go file's
# constants) is pulled once. A refused pull (Docker Hub's "toomanyrequests" above all) is tried again after 15, 30,
# 60, 120 and 240 seconds. With DOCKER_HUB_MIRROR set (CI: mirror.gcr.io), the engine was told to take Docker Hub's
# images from that mirror first, and falls back to Docker Hub itself.
#   sh .github/scripts/pull-images.sh packaging/sign-in/authentik.compose.yaml internal/idp/authentik/native_test.go
set -eu
refs=$(grep -ohE '[a-z0-9][a-z0-9./_-]*(:[A-Za-z0-9._-]+)?@sha256:[0-9a-f]{64}' "$@" | sort -u)
[ -n "$refs" ] || { echo "pull-images.sh: no image pinned by digest in $*" >&2; exit 1; }
for ref in $refs; do
	if docker image inspect "$ref" >/dev/null 2>&1; then
		echo "have $ref"
		continue
	fi
	wait=15
	until docker pull --quiet "$ref"; do
		if [ "$wait" -gt 240 ]; then
			echo "pull-images.sh: $ref refused after 6 tries" >&2
			exit 1
		fi
		echo "pull-images.sh: $ref refused, again in ${wait}s" >&2
		sleep "$wait"
		wait=$((wait * 2))
	done
done
