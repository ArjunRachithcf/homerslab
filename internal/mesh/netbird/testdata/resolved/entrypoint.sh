#!/bin/sh
# dbus, then systemd-resolved with the stand-in for public DNS as its upstream and its stub as the machine's resolver,
# then NetBird's daemon. The test joins it with `netbird up`, as the agent does.
set -eu
mkdir -p /run/dbus /etc/systemd/resolved.conf.d
printf '[Resolve]\nDNS=%s\nDNSStubListener=yes\nLLMNR=no\nMulticastDNS=no\n' "$UPSTREAM_DNS" > /etc/systemd/resolved.conf.d/test.conf
rm -rf /run/systemd/resolve   # resolved makes it itself, for its own user
dbus-daemon --system --fork
/usr/lib/systemd/systemd-resolved &
for _ in $(seq 50); do [ -S /run/dbus/system_bus_socket ] && resolvectl status >/dev/null 2>&1 && break; sleep 0.1; done
printf 'nameserver 127.0.0.53\noptions edns0 trust-ad\n' > /etc/resolv.conf   # Docker's bind mount: rewrite, don't replace
exec netbird service run --log-file console
