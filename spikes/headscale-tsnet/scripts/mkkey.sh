#!/usr/bin/env bash
# Creates a pre-auth key through the REST API and writes the key itself to a file (0600),
# printing only its id, tags, owner and expiry. ENVVAR=NAME writes NAME=key (an env file).
#   mkkey.sh OUTFILE JSON
#   e.g. mkkey.sh go/keys/photos.key '{"aclTags":["tag:app"],"expiration":"2026-11-09T20:00:00Z"}'
set -euo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
out=$1 body=$2
umask 077
mkdir -p "$(dirname "$out")"
resp=$("$here/scripts/api.sh" POST /api/v1/preauthkey "$body" 2>/dev/null)
key=$(jq -r '.preAuthKey.key // empty' <<<"$resp")
if [ -z "$key" ]; then echo "no key in response: $resp" >&2; exit 1; fi
if [ -n "${ENVVAR:-}" ]; then printf '%s=%s\n' "$ENVVAR" "$key" > "$out"; else printf '%s' "$key" > "$out"; fi
jq -c '.preAuthKey | {id, aclTags, user: (.user.name // null), reusable, ephemeral, expiration}' <<<"$resp"
