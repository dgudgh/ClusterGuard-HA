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

install -d -m 0750 -o root -g clusterguard /etc/clusterguard
install -d -m 0750 -o clusterguard -g clusterguard \
  /var/lib/clusterguard \
  /var/log/clusterguard
install -d -m 0700 -o root -g root /var/lib/clusterguard-agent
install -d -m 0750 -o root -g clusterguard /opt/clusterguard/packages

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || :
  if [ "${1:-1}" -gt 1 ]; then
    systemctl try-restart clusterguard-ha.service >/dev/null 2>&1 || :
    systemctl try-restart clusterguard-agent.service >/dev/null 2>&1 || :
  fi
fi

printf '%s\n' \
  "ClusterGuard HA 软件已安装，服务尚未自动启用。" \
  "请准备配置、密钥与数据库账号后执行：" \
  "  clusterguard-configure --help"

exit 0
