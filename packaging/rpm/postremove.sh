#!/bin/sh
set -u

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || :
  systemctl reset-failed >/dev/null 2>&1 || :
fi

if [ "${1:-0}" -eq 0 ]; then
  printf '%s\n' \
    "ClusterGuard HA 软件已卸载。" \
    "配置与运行数据仍保留在 /etc/clusterguard 和 /var/lib/clusterguard。"
fi

exit 0
