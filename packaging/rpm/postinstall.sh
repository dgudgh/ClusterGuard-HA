#!/bin/sh
set -u

if [ -e /usr/local/sbin/clusterguard-upgrade ]; then
  chown root:clusterguard /usr/local/sbin/clusterguard-upgrade
  chmod 0750 /usr/local/sbin/clusterguard-upgrade
fi

install -d -m 0751 -o root -g clusterguard /etc/clusterguard
install -d -o root -g clusterguard -m 0750 /etc/clusterguard/trust
install -d -m 0700 -o root -g root /etc/clusterguard/power-snapshots
install -d -m 0750 -o clusterguard -g clusterguard /var/lib/clusterguard
install -d -m 0750 -o clusterguard -g clusterguard /var/lib/clusterguard/updates
install -d -m 0751 -o clusterguard -g clusterguard /var/log/clusterguard
install -d -m 0700 -o root -g root /var/lib/clusterguard-agent
install -d -m 0750 -o root -g clusterguard /opt/clusterguard/packages

if [ -f /etc/clusterguard/agent.json ]; then
  /usr/local/bin/clusterguard-agent --config /etc/clusterguard/agent.json --configure-systemd-sandbox || exit 1
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || :
fi

printf '%s\n' \
  "ClusterGuard HA 软件已安装，服务尚未自动启用。" \
  "请准备配置、密钥与数据库账号后执行：" \
  "  clusterguard-configure --help"

exit 0
