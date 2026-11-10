#!/usr/bin/env bash
# Calls Headscale's REST API with the API key in .secrets/auth-header (never on the command line).
#   api.sh METHOD PATH [JSON]      e.g. api.sh GET /api/v1/node
# Prints the HTTP status on stderr and the body on stdout. AUTH=file overrides the header file.
set -euo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
m=$1 p=$2 body=${3:-}
auth=${AUTH:-$here/.secrets/auth-header}
args=(-sS --cacert "$here/ca/ca.crt" --resolve headscale.hlspike.test:18443:127.0.0.1
      -H @"$auth" -X "$m" -w '\n%{stderr}HTTP %{http_code}\n')
[ -n "$body" ] && args+=(-H 'Content-Type: application/json' --data "$body")
curl "${args[@]}" "https://headscale.hlspike.test:18443$p"
