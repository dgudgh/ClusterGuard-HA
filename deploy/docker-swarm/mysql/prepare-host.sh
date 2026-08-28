#!/usr/bin/env bash
set -euo pipefail

slot="${1:-}"
node_ip="${2:-}"
data_root="${3:-/data/clusterguard-swarm/mysql/3306}"
[[ "${slot}" =~ ^0[123]$ ]] || { echo "usage: $0 01|02|03 NODE_IP [DATA_ROOT]" >&2; exit 2; }
[[ "${node_ip}" =~ ^[0-9.]+$ ]] || { echo "invalid node IP" >&2; exit 2; }
[[ "${data_root}" = /* ]] || { echo "data root must be absolute" >&2; exit 2; }
[[ "${EUID}" -eq 0 ]] || { echo "run as root" >&2; exit 2; }

total_memory_mb="$(awk '/^MemTotal:/ {print int($2/1024)}' /proc/meminfo)"
seventy_percent_mb=$(((total_memory_mb * 70 + 99) / 100))
rounded_up_mb=$((((seventy_percent_mb + 4095) / 4096) * 4096))
max_safe_multiple_mb=$((((total_memory_mb * 80 / 100) / 4096) * 4096))
((max_safe_multiple_mb >= 4096)) || { echo "at least 5 GiB memory is required" >&2; exit 3; }
buffer_pool_mb="${rounded_up_mb}"
((buffer_pool_mb <= max_safe_multiple_mb)) || buffer_pool_mb="${max_safe_multiple_mb}"

case "${slot}" in
  01) server_id=101; report_host=192.168.102.152 ;;
  02) server_id=102; report_host=192.168.102.153 ;;
  03) server_id=103; report_host=192.168.102.154 ;;
esac
[[ "${node_ip}" = "${report_host}" ]] || report_host="${node_ip}"

# The directory is mounted read-only into the MySQL container. The mysqld
# process drops root privileges before parsing option files and therefore
# needs directory traversal; the files contain no credentials.
install -d -m 0755 /etc/clusterguard/docker
install -d -m 0750 "${data_root}"
cat >/etc/clusterguard/docker/mysql.cnf <<EOF
[mysqld]
port=3306
bind_address=0.0.0.0
server_id=${server_id}
report_host=${report_host}
log_bin=mysql-bin
relay_log=relay-bin
binlog_format=ROW
binlog_row_image=FULL
binlog_expire_logs_seconds=604800
gtid_mode=ON
enforce_gtid_consistency=ON
log_replica_updates=ON
relay_log_recovery=ON
master_info_repository=TABLE
relay_log_info_repository=TABLE
skip_name_resolve=ON
max_connections=1000
innodb_buffer_pool_size=${buffer_pool_mb}M
innodb_buffer_pool_instances=8
innodb_flush_log_at_trx_commit=1
sync_binlog=1
transaction_isolation=READ-COMMITTED
EOF
chmod 0644 /etc/clusterguard/docker/mysql.cnf

if [[ ! -e /etc/clusterguard/docker/mysql-fence.cnf ]]; then
  # The official image must create its system schema before super_read_only is
  # enabled. No writer endpoint or Agent is active during this bootstrap
  # window. bootstrap-replication.sh durably fences replicas before it starts
  # replication or exposes ClusterGuard ownership.
  cat >/etc/clusterguard/docker/mysql-fence.cnf <<EOF
# Managed by ClusterGuard HA.
[mysqld]
read_only=OFF
super_read_only=OFF
EOF
  chmod 0644 /etc/clusterguard/docker/mysql-fence.cnf
fi

if command -v chcon >/dev/null 2>&1; then
  chcon -Rt container_file_t "${data_root}" /etc/clusterguard/docker 2>/dev/null || true
fi

node_id="$(docker info --format '{{.Swarm.NodeID}}' 2>/dev/null || true)"
[[ -n "${node_id}" ]] || { echo "host is not an active Swarm node" >&2; exit 3; }
docker node update --label-add "clusterguard.mysql.slot=${slot}" "${node_id}" >/dev/null
printf 'slot=%s node_id=%s buffer_pool_mb=%s data_root=%s\n' "${slot}" "${node_id}" "${buffer_pool_mb}" "${data_root}"
