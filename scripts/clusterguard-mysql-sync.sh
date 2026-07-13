#!/usr/bin/env bash
set -euo pipefail

method="${1:-}"
case "${method}" in clone|xtrabackup|logical_dump) ;; *) echo "unsupported sync method" >&2; exit 2 ;; esac
command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }

payload="$(mktemp /tmp/clusterguard-sync.XXXXXX)"
donor_defaults="$(mktemp /tmp/clusterguard-donor.XXXXXX)"
chmod 0600 "${payload}" "${donor_defaults}"
target_ready=false
cleanup() {
  if [[ "${target_ready}" == "true" ]]; then
    mysql_target >/dev/null 2>&1 <<'SQL' || true
SET GLOBAL super_read_only=ON;
SET GLOBAL read_only=ON;
SQL
  fi
  rm -f "${payload}" "${donor_defaults}"
}
trap cleanup EXIT
cat >"${payload}"
jq -e '.request.cluster_id and .request.donor.port and .target.mysql_port and .secrets.mysql_root_password and .secrets.replication_password' "${payload}" >/dev/null

cluster_id="$(jq -r '.request.cluster_id' "${payload}")"
node_id="$(jq -r '.target.node_id' "${payload}")"
node_name="$(jq -r '.target.node_name' "${payload}")"
target_host="$(jq -r '.target.hostname' "${payload}")"
target_ip="$(jq -r '.target.ip_address // ""' "${payload}")"
target_port="$(jq -r '.target.mysql_port' "${payload}")"
donor_host="$(jq -r '.request.donor.ip_address // .request.donor.hostname' "${payload}")"
donor_port="$(jq -r '.request.donor.port' "${payload}")"
vip="$(jq -r '.request.vip // ""' "${payload}")"
root_password="$(jq -r '.secrets.mysql_root_password' "${payload}")"
replication_password="$(jq -r '.secrets.replication_password' "${payload}")"
replication_user="clusterguard_repl"

install_root="/opt/clusterguard/mysql/${target_port}/software"
mysql="${install_root}/bin/mysql"
mysqldump="${install_root}/bin/mysqldump"
target_defaults="/etc/clusterguard/mysql/${target_port}-client.cnf"
[[ -x "${mysql}" && -f "${target_defaults}" ]] || { echo "target MySQL installation is incomplete" >&2; exit 3; }
cat >"${donor_defaults}" <<EOF
[client]
user=root
password=${root_password}
EOF
chmod 0600 "${donor_defaults}"

sql_quote() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\'/\'\'}"
  printf "'%s'" "${value}"
}

mysql_target() {
  "${mysql}" --defaults-extra-file="${target_defaults}" --protocol=tcp --host=127.0.0.1 --port="${target_port}" "$@"
}

mysql_donor() {
  "${mysql}" --defaults-extra-file="${donor_defaults}" --protocol=tcp --host="${donor_host}" --port="${donor_port}" "$@"
}

target_ready=true
mysql_target <<'SQL'
SET GLOBAL super_read_only=OFF;
SET GLOBAL read_only=OFF;
SQL

case "${method}" in
  clone)
    helper="${CG_MYSQL_CLONE_HELPER:-/usr/local/libexec/clusterguard-mysql-clone}"
    [[ -x "${helper}" ]] || { echo "Clone was selected but the verified Clone helper is unavailable" >&2; exit 4; }
    "${helper}" <"${payload}" >/dev/null
    ;;
  xtrabackup)
    helper="${CG_MYSQL_XTRABACKUP_HELPER:-/usr/local/libexec/clusterguard-mysql-xtrabackup}"
    [[ -x "${helper}" ]] || { echo "XtraBackup was selected but the matching helper is unavailable" >&2; exit 4; }
    "${helper}" <"${payload}" >/dev/null
    ;;
  logical_dump)
    [[ -x "${mysqldump}" ]] || { echo "mysqldump is unavailable" >&2; exit 4; }
    "${mysqldump}" --defaults-extra-file="${donor_defaults}" --host="${donor_host}" --port="${donor_port}" \
      --single-transaction --routines --events --triggers --hex-blob --all-databases --set-gtid-purged=ON | mysql_target
    ;;
esac

mysql_target <<'SQL'
SET GLOBAL super_read_only=ON;
SET GLOBAL read_only=ON;
SQL

replication_secret="$(sql_quote "${replication_password}")"
printf "CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY %s;\nALTER USER '%s'@'%%' IDENTIFIED BY %s;\nGRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO '%s'@'%%';\n" \
  "${replication_user}" "${replication_secret}" "${replication_user}" "${replication_secret}" "${replication_user}" | mysql_donor

target_version="$(mysql_target --batch --skip-column-names -e 'SELECT @@version')"
major_minor="$(awk -F. '{print $1"."$2}' <<<"${target_version}")"
donor_secret="$(sql_quote "${replication_password}")"
mysql_target <<'SQL'
SET GLOBAL super_read_only=ON;
SET GLOBAL read_only=ON;
SQL
if [[ "${major_minor}" == "5.7" ]]; then
  mysql_target -e 'STOP SLAVE; RESET SLAVE ALL;' || true
  printf "CHANGE MASTER TO MASTER_HOST='%s', MASTER_PORT=%s, MASTER_USER='%s', MASTER_PASSWORD=%s, MASTER_AUTO_POSITION=1;\nSTART SLAVE;\n" \
    "${donor_host}" "${donor_port}" "${replication_user}" "${donor_secret}" | mysql_target
  status_command='SHOW SLAVE STATUS\G'
  io_pattern='Slave_IO_Running: Yes'
  sql_pattern='Slave_SQL_Running: Yes'
else
  mysql_target -e 'STOP REPLICA; RESET REPLICA ALL;' || true
  printf "CHANGE REPLICATION SOURCE TO SOURCE_HOST='%s', SOURCE_PORT=%s, SOURCE_USER='%s', SOURCE_PASSWORD=%s, SOURCE_AUTO_POSITION=1;\nSTART REPLICA;\n" \
    "${donor_host}" "${donor_port}" "${replication_user}" "${donor_secret}" | mysql_target
  status_command='SHOW REPLICA STATUS\G'
  io_pattern='Replica_IO_Running: Yes'
  sql_pattern='Replica_SQL_Running: Yes'
fi

verified=false
for _ in $(seq 1 12); do
  status="$(mysql_target -e "${status_command}" 2>/dev/null || true)"
  if grep -Fq "${io_pattern}" <<<"${status}" && grep -Fq "${sql_pattern}" <<<"${status}"; then
    verified=true
    break
  fi
  sleep 5
done
[[ "${verified}" == "true" ]] || { echo "replication did not become healthy within 60 seconds" >&2; exit 5; }

read -r server_uuid reported_host reported_port version read_only super_read_only < <(
  mysql_target --batch --skip-column-names -e 'SELECT @@server_uuid, @@hostname, @@port, @@version, @@read_only, @@super_read_only'
)
[[ -n "${server_uuid}" && "${read_only}" == "1" && "${super_read_only}" == "1" ]] || { echo "target identity or read-only verification failed" >&2; exit 5; }
if [[ -n "${vip}" ]] && ip -4 -o addr show | grep -Fq " ${vip}/"; then
  echo "target unexpectedly owns the cluster VIP" >&2
  exit 5
fi

jq -nc \
  --arg cluster_id "${cluster_id}" --arg node_id "${node_id}" --arg node_name "${node_name}" \
  --arg hostname "${target_host:-${reported_host}}" --arg ip_address "${target_ip}" --argjson port "${reported_port}" \
  --arg server_uuid "${server_uuid}" --arg version "${version}" \
  '{cluster_id:$cluster_id,node_id:$node_id,engine:"mysql",engine_identity:{server_uuid:$server_uuid},display_name:$node_name,hostname:$hostname,ip_address:$ip_address,port:$port,role:"replica",health:{state:"healthy"},replication:{io_thread:"running",sql_thread:"running"},promotion_eligible:true,engine_metadata:{version:$version}}'
