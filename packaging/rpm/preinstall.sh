#!/bin/sh
set -u

if ! getent group clusterguard >/dev/null 2>&1; then
  groupadd --system clusterguard
fi
if ! getent passwd clusterguard >/dev/null 2>&1; then
  useradd --system \
    --gid clusterguard \
    --home-dir /var/lib/clusterguard \
    --shell /sbin/nologin \
    --comment "ClusterGuard HA service account" \
    clusterguard
fi

exit 0
