#!/usr/bin/env bash
# A throwaway CA and a server certificate for Headscale (its API, its Noise endpoint and its
# embedded DERP server share one TLS listener). In a real deployment this is a public
# certificate on the off-site server; here the containers trust ca/ca.crt via SSL_CERT_DIR.
set -euo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
cd "$here"
umask 077
mkdir -p ca headscale/tls headscale/extra .secrets run/apps run/oneoff/keys
chmod 755 ca headscale/tls headscale/extra run run/apps run/oneoff
[ -f .secrets/ca.key ] || openssl ecparam -name prime256v1 -genkey -noout -out .secrets/ca.key
openssl req -x509 -new -key .secrets/ca.key -sha256 -days 30 -subj "/CN=hlspike throwaway CA" \
  -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" \
  -out ca/ca.crt
openssl ecparam -name prime256v1 -genkey -noout -out headscale/tls/server.key
openssl req -new -key headscale/tls/server.key -subj "/CN=headscale.hlspike.test" -out headscale/tls/server.csr
cat > headscale/tls/ext.cnf <<'EOF'
basicConstraints=CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
subjectAltName=DNS:headscale.hlspike.test,DNS:localhost,IP:127.0.0.1,IP:10.77.29.10
EOF
openssl x509 -req -in headscale/tls/server.csr -CA ca/ca.crt -CAkey .secrets/ca.key -CAcreateserial \
  -days 30 -sha256 -extfile headscale/tls/ext.cnf -out headscale/tls/server.crt
rm -f headscale/tls/server.csr headscale/tls/ext.cnf ca/ca.srl
chmod 644 ca/ca.crt headscale/tls/server.crt
openssl x509 -in headscale/tls/server.crt -noout -subject -ext subjectAltName -enddate
