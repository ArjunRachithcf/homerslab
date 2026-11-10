#!/usr/bin/env bash
# Runs N tsnet nodes (s001…sNNN) in one process, lets them idle, and appends what it measured
# to results/scale.txt. Nodes reuse their state in run/oneoff/state-scale, so only new ones
# register (with the reusable key run/oneoff/keys/scale.key).
#   scale.sh N [IDLE_SECONDS]
set -euo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
n=$1 idle=${2:-120}
name=hlspike-headscale-oneoff
out=$here/results/scale.txt
"$here/scripts/oneoff.sh" -d -n "$n" -prefix s -key-file /work/keys/scale.key \
  -state /work/state-scale -parallel 8 -stats-every 30s -logs file >/dev/null
t0=$(date +%s)
until docker logs "$name" 2>&1 | command grep -aq 'all nodes started'; do
  sleep 1
  [ $(( $(date +%s) - t0 )) -gt 600 ] && { echo "N=$n did not start in 10 min" >&2; docker stop "$name" >/dev/null; exit 1; }
done
sleep "$idle"
{
  echo "=== N=$n ($(date -u +%FT%TZ)), idle ${idle}s after start"
  docker logs "$name" 2>&1 | command grep -aE 'all nodes started|FAILED' | sed 's/^[0-9/]* //'
  docker logs "$name" 2>&1 | command grep -a 'up in' | sed -E 's/.*up in ([0-9.]+)(m?s).*/\1 \2/' \
    | awk '{v=($2=="ms")?$1/1000:$1; s+=v; if(v>m)m=v; c++} END {if(c) printf "join: %d nodes, mean %.2fs, max %.2fs\n", c, s/c, m}'
  docker logs "$name" 2>&1 | command grep -a 'stats ' | tail -3 | sed 's/^[0-9/]* [0-9:.]* //'
  docker stats --no-stream --format '{{.Name}} cpu={{.CPUPerc}} mem={{.MemUsage}} pids={{.PIDs}}' "$name" hlspike-headscale-headscale-1
} | tee -a "$out"
docker stop "$name" >/dev/null
