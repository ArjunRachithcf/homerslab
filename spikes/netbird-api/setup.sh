#!/usr/bin/env bash
# Prepares the spike: the server's config with fresh secrets, a self-signed certificate for the
# proxy cluster's own name, empty env files for the keys the API will hand out.
# Everything secret lands in secrets/ (git-ignored, mode 700).
set -euo pipefail
cd "$(dirname "$0")"
umask 077

gen() { openssl rand -base64 32; }

if [ ! -s secrets/server/config.yaml ]; then
  sed -e "s|__RELAY_AUTH_SECRET__|$(gen | tr -d '=')|" \
      -e "s|__SESSION_COOKIE_KEY__|$(gen)|" \
      -e "s|__DATASTORE_KEY__|$(gen)|" \
      server-config.yaml.tmpl > secrets/server/config.yaml
fi

if [ ! -s secrets/proxy-certs/tls.crt ]; then
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 7 \
    -subj /CN=proxy.hlspike.test \
    -addext 'subjectAltName=DNS:proxy.hlspike.test,DNS:*.proxy.hlspike.test' \
    -keyout secrets/proxy-certs/tls.key -out secrets/proxy-certs/tls.crt 2>/dev/null
fi

mkdir -p secrets/tmp
for f in edge device proxy; do [ -e "secrets/env/$f.env" ] || : > "secrets/env/$f.env"; done
[ -e secrets/tools/auth.hdr ] || : > secrets/tools/auth.hdr

docker compose -p hlspike-netbird -f compose.yaml config -q && echo "compose config: ok"
