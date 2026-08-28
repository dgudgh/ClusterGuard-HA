#!/usr/bin/env bash
set -euo pipefail

slot="${1:-}"
[[ "${slot}" =~ ^0[123]$ ]] || { echo "usage: $0 01|02|03" >&2; exit 2; }
: "${CG_CLUSTER_ID:?CG_CLUSTER_ID is required}"
: "${CG_INSTANCE_01_ID:?CG_INSTANCE_01_ID is required}"
: "${CG_INSTANCE_02_ID:?CG_INSTANCE_02_ID is required}"
: "${CG_INSTANCE_03_ID:?CG_INSTANCE_03_ID is required}"
: "${CG_MYSQL_ROOT_PASSWORD:?CG_MYSQL_ROOT_PASSWORD is required}"
: "${CG_MYSQL_DISCOVERY_PASSWORD:?CG_MYSQL_DISCOVERY_PASSWORD is required}"
: "${CG_MYSQL_OPERATION_PASSWORD:?CG_MYSQL_OPERATION_PASSWORD is required}"
: "${CG_MYSQL_REPLICATION_PASSWORD:?CG_MYSQL_REPLICATION_PASSWORD is required}"
CG_MYSQL_SOURCE_HOST="${CG_MYSQL_SOURCE_HOST:-192.168.102.152}"
[[ "${CG_MYSQL_SOURCE_HOST}" =~ ^[A-Za-z0-9.-]+$ ]] || { echo "CG_MYSQL_SOURCE_HOST is invalid" >&2; exit 2; }

for secret_name in CG_MYSQL_ROOT_PASSWORD CG_MYSQL_DISCOVERY_PASSWORD CG_MYSQL_OPERATION_PASSWORD CG_MYSQL_REPLICATION_PASSWORD; do
  secret_value="${!secret_name}"
  [[ "${secret_value}" =~ ^[A-Za-z0-9._@%+=:-]{16,128}$ ]] || {
    echo "${secret_name} contains unsupported bootstrap characters or is too short" >&2
    exit 2
  }
done
if ((${#CG_MYSQL_REPLICATION_PASSWORD} > 32)); then
  echo "CG_MYSQL_REPLICATION_PASSWORD must not exceed the MySQL replication channel limit of 32 characters" >&2
  exit 2
fi

container_for_instance() {
  local instance_id="$1" containers
  containers="$(docker ps \
    --filter "label=clusterguard.cluster_id=${CG_CLUSTER_ID}" \
    --filter "label=clusterguard.instance_id=${instance_id}" \
    --format '{{.ID}}')"
  [[ "$(wc -w <<<"${containers}")" -eq 1 ]] || {
    echo "expected one running container for instance ${instance_id}" >&2
    exit 3
  }
  printf '%s' "${containers}"
}

case "${slot}" in
  01) local_instance_id="${CG_INSTANCE_01_ID}" ;;
  02) local_instance_id="${CG_INSTANCE_02_ID}" ;;
  03) local_instance_id="${CG_INSTANCE_03_ID}" ;;
esac
container="$(container_for_instance "${local_instance_id}")"

root_defaults="$(mktemp /var/tmp/clusterguard-root-client.XXXXXX)"
chmod 0600 "${root_defaults}"
cat >"${root_defaults}" <<EOF
[client]
user=root
password=${CG_MYSQL_ROOT_PASSWORD}
EOF
docker cp "${root_defaults}" "${container}:/run/clusterguard-root.cnf" >/dev/null
docker exec "${container}" chmod 0600 /run/clusterguard-root.cnf

mysql_root() {
  local container="$1"
  docker exec -i "${container}" /usr/bin/mysql --defaults-file=/run/clusterguard-root.cnf \
    --protocol=tcp --host=127.0.0.1 --port=3306 --batch --skip-column-names
}

replica_guard_relaxed=false
cleanup() {
  local status=$?
  trap - EXIT
  if [[ "${replica_guard_relaxed}" == "true" ]]; then
    mysql_root "${container}" >/dev/null 2>&1 <<'SQL' || true
SET GLOBAL read_only=ON;
SET GLOBAL super_read_only=ON;
SQL
  fi
  rm -f "${root_defaults}"
  docker exec "${container}" rm -f /run/clusterguard-root.cnf >/dev/null 2>&1 || true
  exit "${status}"
}
trap cleanup EXIT

write_restart_fence() {
  local value="$1" path=/etc/clusterguard/docker/mysql-fence.cnf directory temporary
  [[ "${value}" == "ON" || "${value}" == "OFF" ]] || return 2
  directory="$(dirname "${path}")"
  temporary="$(mktemp "${directory}/.mysql-fence.XXXXXX")"
  chmod 0644 "${temporary}"
  printf '# Managed by ClusterGuard HA.\n[mysqld]\nread_only=%s\nsuper_read_only=%s\n' "${value}" "${value}" >"${temporary}"
  sync "${temporary}"
  mv -f "${temporary}" "${path}"
  sync "${directory}"
}

if [[ "${slot}" != "01" ]]; then
  # Keep read_only enabled while super_read_only is relaxed for idempotent
  # local account reconciliation. The EXIT trap restores both controls on any
  # failure, and the host restart fence is already durable at this point.
  write_restart_fence ON
  replica_guard_relaxed=true
  mysql_root "${container}" <<'SQL'
SET GLOBAL read_only=ON;
SET GLOBAL super_read_only=OFF;
SQL
fi

mysql_root "${container}" <<SQL
SET sql_log_bin=0;
CREATE USER IF NOT EXISTS 'cg_discovery'@'%' IDENTIFIED BY '${CG_MYSQL_DISCOVERY_PASSWORD}';
ALTER USER 'cg_discovery'@'%' IDENTIFIED BY '${CG_MYSQL_DISCOVERY_PASSWORD}';
GRANT SELECT, PROCESS, REPLICATION CLIENT ON *.* TO 'cg_discovery'@'%';
CREATE USER IF NOT EXISTS 'cg_operator'@'%' IDENTIFIED BY '${CG_MYSQL_OPERATION_PASSWORD}';
ALTER USER 'cg_operator'@'%' IDENTIFIED BY '${CG_MYSQL_OPERATION_PASSWORD}';
GRANT ALL PRIVILEGES ON *.* TO 'cg_operator'@'%' WITH GRANT OPTION;
CREATE USER IF NOT EXISTS 'cg_replication'@'%' IDENTIFIED BY '${CG_MYSQL_REPLICATION_PASSWORD}';
ALTER USER 'cg_replication'@'%' IDENTIFIED BY '${CG_MYSQL_REPLICATION_PASSWORD}';
GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO 'cg_replication'@'%';
SET sql_log_bin=1;
SQL

if [[ "${slot}" != "01" ]]; then
  mysql_root "${container}" <<'SQL'
SET GLOBAL read_only=ON;
SET GLOBAL super_read_only=ON;
SQL
  replica_guard_relaxed=false
fi

if [[ "${slot}" == "01" ]]; then
  mysql_root "${container}" <<SQL
SET PERSIST_ONLY super_read_only=OFF;
SET PERSIST_ONLY read_only=OFF;
SET GLOBAL super_read_only=OFF;
SET GLOBAL read_only=OFF;
SQL
  write_restart_fence OFF
  mysql_root "${container}" <<'SQL'
CREATE DATABASE IF NOT EXISTS clusterguard_validation;
CREATE TABLE IF NOT EXISTS clusterguard_validation.swarm_probe (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  marker VARCHAR(128) NOT NULL,
  created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB;
INSERT INTO clusterguard_validation.swarm_probe(marker) VALUES ('docker-swarm-bootstrap');
SQL
  echo "MySQL Docker Swarm primary bootstrap is ready"
  exit 0
fi

gtid_baseline_marker="/var/lib/mysql/.clusterguard-replica-gtid-baseline"
if ! docker exec "${container}" test -f "${gtid_baseline_marker}"; then
  # Official image initialization may create local GTIDs before replication is
  # configured. They are harmless bootstrap writes but are errant from the HA
  # topology's perspective. Reset them only on a provably virgin replica.
  bootstrap_state="$(mysql_root "${container}" <<'SQL'
SELECT CONCAT(
  (SELECT COUNT(*) FROM information_schema.schemata
    WHERE schema_name NOT IN ('information_schema','mysql','performance_schema','sys')), ':',
  (SELECT COUNT(*) FROM performance_schema.replication_connection_configuration)
);
SQL
)"
  if [[ "${bootstrap_state}" != "0:0" ]]; then
    echo "replica has data or replication metadata but no GTID baseline marker; refusing destructive GTID reset" >&2
    exit 5
  fi
  mysql_root "${container}" <<'SQL'
STOP REPLICA;
RESET REPLICA ALL;
SQL
  mysql_version="$(mysql_root "${container}" <<'SQL'
SELECT SUBSTRING_INDEX(VERSION(), '-', 1);
SQL
)"
  if [[ "${mysql_version}" == 8.0.* ]]; then
    mysql_root "${container}" <<'SQL'
RESET MASTER;
SQL
  else
    mysql_root "${container}" <<'SQL'
RESET BINARY LOGS AND GTIDS;
SQL
  fi
  docker exec "${container}" /bin/sh -c 'umask 077; : > /var/lib/mysql/.clusterguard-replica-gtid-baseline'
fi

mysql_root "${container}" <<SQL
STOP REPLICA;
RESET REPLICA ALL;
CHANGE REPLICATION SOURCE TO
  SOURCE_HOST='${CG_MYSQL_SOURCE_HOST}', SOURCE_PORT=3306,
  SOURCE_USER='cg_replication', SOURCE_PASSWORD='${CG_MYSQL_REPLICATION_PASSWORD}',
  SOURCE_AUTO_POSITION=1, SOURCE_CONNECT_RETRY=2, SOURCE_RETRY_COUNT=86400,
  GET_SOURCE_PUBLIC_KEY=1;
START REPLICA;
SET PERSIST_ONLY super_read_only=ON;
SET PERSIST_ONLY read_only=ON;
SET GLOBAL read_only=ON;
SET GLOBAL super_read_only=ON;
SQL

deadline=$((SECONDS + 90))
while true; do
  state="$(mysql_root "${container}" <<'SQL'
SELECT CONCAT(
  COALESCE((SELECT SERVICE_STATE FROM performance_schema.replication_connection_status LIMIT 1), 'OFF'), ':',
  COALESCE((SELECT SERVICE_STATE FROM performance_schema.replication_applier_status LIMIT 1), 'OFF'), ':',
  (SELECT COUNT(*) FROM information_schema.tables
    WHERE table_schema='clusterguard_validation' AND table_name='swarm_probe')
);
SQL
)"
  if [[ "${state}" == "ON:ON:1" ]]; then
    rows="$(mysql_root "${container}" <<'SQL'
SELECT COUNT(*) FROM clusterguard_validation.swarm_probe;
SQL
)"
    if [[ "${rows}" =~ ^[1-9][0-9]*$ ]]; then
      printf 'replica_%s=%s:rows=%s\n' "${slot}" "${state}" "${rows}"
      break
    fi
  fi
  ((SECONDS < deadline)) || { echo "replica ${slot} did not converge: ${state}" >&2; exit 4; }
  sleep 2
done

echo "MySQL Docker Swarm replica ${slot} is ready"
