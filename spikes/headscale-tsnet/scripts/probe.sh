#!/usr/bin/env bash
# From a client container, resolve each name (A record, through the client's own resolver,
# i.e. /etc/resolv.conf = 100.100.100.100) and fetch http://NAME/.
#   probe.sh client-adult photos.home.test notes.home.test ...
set -uo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
c=$1; shift
dc=(docker compose -p hlspike-headscale -f "$here/compose.yaml")
for name in "$@"; do
  addr=$("${dc[@]}" exec -T "$c" sh -c "nslookup -type=a -timeout=3 $name 2>/dev/null" \
         | awk '/^Name:/ {n=1; next} n && /^Address/ {print $2; exit}')
  body=$("${dc[@]}" exec -T "$c" sh -c "wget -q -T 4 -O- http://$name/ 2>&1" | head -1)
  printf '%s %-24s A=%-12s %s\n' "$c" "$name" "${addr:-none}" "${body:-no answer}"
done
