#!/bin/sh
set -u

if [ "${1:-0}" -eq 0 ] && command -v systemctl >/dev/null 2>&1; then
  systemctl disable --now clusterguard-agent-reconcile.timer >/dev/null 2>&1 || :
  systemctl disable --now clusterguard-agent.service >/dev/null 2>&1 || :
  systemctl disable --now clusterguard-ha.service >/dev/null 2>&1 || :
fi

exit 0
