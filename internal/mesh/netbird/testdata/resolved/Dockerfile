# A node whose resolver is systemd-resolved, as on Fedora, Ubuntu and Arch: NetBird's client beside it must leave the
# machine's own names working. systemd isn't PID 1 here; dbus and resolved run as plain daemons (entrypoint.sh).
FROM netbirdio/netbird:0.82.0@sha256:eca5b274c635bfbb04ec6724a0712d1146190dd450eea199eeb83916fdd94965 AS netbird
FROM debian:trixie@sha256:2dc419c6d7fe3dad0a2f9fd469c34cc90b0f9ba642d8fde837fe3d7d34a0e7f1
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      systemd-resolved dbus iproute2 nftables iptables ncat ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=netbird /usr/local/bin/netbird /usr/local/bin/netbird
COPY entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/bin/sh", "/entrypoint.sh"]
