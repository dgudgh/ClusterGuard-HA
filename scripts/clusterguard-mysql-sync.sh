#!/usr/bin/env bash
set -euo pipefail

method="${1:-}"
case "${method}" in clone|xtrabackup|logical_dump) ;; *) echo "unsupported sync method" >&2; exit 2 ;; esac
command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }

payload="$(mktemp /tmp/clusterguard-sync.XXXXXX)"
donor_defaults="$(mktemp /tmp/clusterguard-donor.XXXXXX)"
replication_defaults="$(mktemp /tmp/clusterguard-replication.XXXXXX)"
chmod 0600 "${payload}" "${donor_defaults}" "${replication_defaults}"
target_ready=false
donor_dump_file=""
verified_sync_marker_tmp=""
cleanup() {
  if [[ "${target_ready}" == "true" ]]; then
    mysql_target >/dev/null 2>&1 <<'SQL' || true
SET GLOBAL read_only=ON;
SET GLOBAL super_read_only=ON;
SQL
  fi
  rm -f "${payload}" "${donor_defaults}" "${replication_defaults}"
  [[ -z "${donor_dump_file}" ]] || rm -f "${donor_dump_file}"
  [[ -z "${verified_sync_marker_tmp}" ]] || rm -f "${verified_sync_marker_tmp}"
}
trap cleanup EXIT
cat >"${payload}"
jq -e '.request.cluster_id and .request.donor.port and .target.mysql_port and .secrets.mysql_root_password and .secrets.mysql_replication_username and .secrets.replication_password' "${payload}" >/dev/null

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
operation_user="$(jq -r '.secrets.mysql_operation_username // "root"' "${payload}")"
operation_password="$(jq -r '.secrets.mysql_operation_password // .secrets.mysql_root_password' "${payload}")"
replication_password="$(jq -r '.secrets.replication_password' "${payload}")"
replication_user="$(jq -r '.secrets.mysql_replication_username' "${payload}")"
[[ "${operation_user}" =~ ^[A-Za-z0-9_]{1,32}$ ]] || { echo "invalid managed MySQL operation username" >&2; exit 2; }
[[ "${replication_user}" =~ ^[A-Za-z0-9_]{1,32}$ ]] || { echo "invalid managed MySQL replication username" >&2; exit 2; }
(( ${#replication_password} <= 32 )) || { echo "managed MySQL replication password exceeds the 32-character channel limit" >&2; exit 2; }

install_root="/opt/clusterguard/mysql/${target_port}/software"
data_root="$(jq -r '.target.data_root // "/var/lib/clusterguard"' "${payload}")"
[[ "${data_root}" == /* && "${data_root}" != "/" && "${data_root}" != *$'\n'* && "${data_root}" != *$'\r'* ]] || {
  echo "MySQL data root must be a safe absolute directory" >&2
  exit 2
}
data_root="${data_root%/}"
case "/${data_root#/}/" in
  */../*|*/./*) echo "MySQL data root cannot contain dot path segments" >&2; exit 2 ;;
esac
sync_state_dir="${data_root}/mysql/${target_port}"
verified_sync_marker="${sync_state_dir}/.last-verified-sync.json"

discover_running_mysql_client() {
  command -v ss >/dev/null 2>&1 || return 1
  local pid executable candidate
  while IFS= read -r pid; do
    [[ "${pid}" =~ ^[0-9]+$ ]] || continue
    executable="$(readlink -f "/proc/${pid}/exe" 2>/dev/null || true)"
    [[ "$(basename "${executable}")" == "mysqld" ]] || continue
    candidate="$(dirname "${executable}")/mysql"
    if [[ -x "${candidate}" ]]; then
      printf '%s\n' "${candidate}"
      return 0
    fi
  done < <(ss -H -ltnp "sport = :${target_port}" 2>/dev/null | grep -oE 'pid=[0-9]+' | cut -d= -f2 | sort -u)
  return 1
}

running_mysql_client="$(discover_running_mysql_client || true)"
mysql=""
for candidate in "${install_root}/bin/mysql" "${CG_MYSQL_CLIENT:-}" "$(command -v mysql 2>/dev/null || true)" "${running_mysql_client}"; do
  if [[ -n "${candidate}" && -x "${candidate}" ]]; then
    mysql="${candidate}"
    break
  fi
done
target_defaults="/etc/clusterguard/mysql/${target_port}-client.cnf"
[[ -n "${mysql}" && -x "${mysql}" && -f "${target_defaults}" ]] || { echo "target MySQL client or protected option file is unavailable" >&2; exit 3; }
cat >"${donor_defaults}" <<EOF
[client]
user=${operation_user}
password=${operation_password}
EOF
cat >"${replication_defaults}" <<EOF
[client]
user=${replication_user}
password=${replication_password}
EOF
chmod 0600 "${donor_defaults}" "${replication_defaults}"

sql_quote() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\'/\'\'}"
  printf "'%s'" "${value}"
}

mysql_target() {
  "${mysql}" --defaults-file="${target_defaults}" --protocol=tcp --host=127.0.0.1 --port="${target_port}" "$@"
}

mysql_donor() {
  "${mysql}" --defaults-file="${donor_defaults}" --protocol=tcp --host="${donor_host}" --port="${donor_port}" "$@"
}

mysql_replication_donor() {
  local -a connection_args=(
    --defaults-file="${replication_defaults}"
    --protocol=tcp
    --host="${donor_host}"
    --port="${donor_port}"
  )
  if "${mysql}" --help 2>/dev/null | grep -q -- '--get-server-public-key'; then
    connection_args+=(--get-server-public-key)
  fi
  "${mysql}" "${connection_args[@]}" "$@"
}

target_basedir="$(mysql_target --batch --skip-column-names -e 'SELECT @@basedir')"
native_mysql="${target_basedir%/}/bin/mysql"
native_mysqldump="${target_basedir%/}/bin/mysqldump"
if [[ -x "${native_mysql}" ]]; then
  mysql="${native_mysql}"
fi
mysqldump="${native_mysqldump}"
if [[ ! -x "${mysqldump}" ]]; then
  mysqldump="$(dirname "${mysql}")/mysqldump"
fi

# Account provisioning belongs to node installation. Synchronization only
# proves that the managed replication identity can authenticate to the donor,
# so a read-only primary never needs a temporary mutation window.
replication_auth_result="$(mysql_replication_donor --batch --skip-column-names -e 'SELECT 1')" || {
  echo "managed MySQL replication account cannot authenticate to donor" >&2
  exit 4
}
[[ "${replication_auth_result}" == "1" ]] || {
  echo "managed MySQL replication account returned an unexpected authentication probe result" >&2
  exit 4
}

target_ready=true
sync_strategy="${method}"
donor_gtid=""

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
    rebuild="$(jq -r '.target.rebuild // false' "${payload}")"
    target_server_uuid="$(mysql_target --batch --skip-column-names -e 'SELECT @@server_uuid')"
	    target_gtid="$(mysql_target --batch --raw --skip-column-names -e 'SELECT @@GLOBAL.gtid_executed')"
	    donor_gtid="$(mysql_donor --batch --raw --skip-column-names -e 'SELECT @@GLOBAL.gtid_executed')"
    marker_server_uuid=""
    if [[ -s "${verified_sync_marker}" ]]; then
      marker_server_uuid="$(jq -r '.server_uuid // empty' "${verified_sync_marker}" 2>/dev/null || true)"
    fi
    safe_incremental=false
    if [[ "${rebuild}" == "true" && -n "${target_gtid}" && "${marker_server_uuid}" == "${target_server_uuid}" ]]; then
      target_gtid_sql="$(sql_quote "${target_gtid}")"
      donor_gtid_sql="$(sql_quote "${donor_gtid}")"
      if [[ "$(mysql_target --batch --skip-column-names -e "SELECT GTID_SUBSET(${target_gtid_sql}, ${donor_gtid_sql})")" == "1" ]]; then
        safe_incremental=true
      fi
    fi
    if [[ "${safe_incremental}" == "true" ]]; then
      sync_strategy="incremental_rejoin"
    else
      sync_strategy="full_reseed"
      install -d -m 0700 /var/lib/clusterguard/stage
      donor_dump_file="$(mktemp "/var/lib/clusterguard/stage/mysql-${target_port}-donor.XXXXXX.sql")"
      chmod 0600 "${donor_dump_file}"
      mapfile -t user_databases < <(mysql_donor --batch --skip-column-names -e \
        "SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('information_schema','performance_schema','mysql','sys','cg_rc28_test','clusterguard_ha_test','clusterguard_matrix_probe','clusterguard_validation') ORDER BY schema_name")
      if [[ "${#user_databases[@]}" -gt 0 ]]; then
        "${mysqldump}" --defaults-file="${donor_defaults}" --host="${donor_host}" --port="${donor_port}" \
          --single-transaction --routines --events --triggers --hex-blob --set-gtid-purged=ON --databases "${user_databases[@]}" > "${donor_dump_file}"
      else
        : > "${donor_dump_file}"
      fi

      # The donor artifact is complete at this boundary. Only now invalidate the
      # previous verification marker and begin destructive target replacement.
      rm -f "${verified_sync_marker}"
      mysql_target <<'SQL'
SET GLOBAL super_read_only=OFF;
SET GLOBAL read_only=ON;
SQL
      mysql_target -e 'STOP REPLICA; RESET REPLICA ALL;' >/dev/null 2>&1 || mysql_target -e 'STOP SLAVE; RESET SLAVE ALL;' >/dev/null 2>&1 || true
      mysql_target -e 'RESET BINARY LOGS AND GTIDS' >/dev/null 2>&1 || mysql_target -e 'RESET MASTER'
      mapfile -t target_database_hex < <(mysql_target --batch --skip-column-names -e \
        "SELECT HEX(schema_name) FROM information_schema.schemata WHERE schema_name NOT IN ('information_schema','performance_schema','mysql','sys') ORDER BY schema_name")
      drop_schema_template="$(cat <<'SQL'
SET SESSION sql_log_bin=0;
SET @cg_schema=CONVERT(0xSCHEMA_HEX USING utf8mb4);
SET @cg_drop_schema_sql=CONCAT('DROP DATABASE IF EXISTS `', REPLACE(@cg_schema, '`', '``'), '`');
PREPARE cg_drop_schema FROM @cg_drop_schema_sql;
EXECUTE cg_drop_schema;
DEALLOCATE PREPARE cg_drop_schema;
SQL
)"
      for target_schema_hex in "${target_database_hex[@]}"; do
        [[ "${target_schema_hex}" =~ ^[0-9A-F]+$ ]] || { echo "target schema identity is invalid" >&2; exit 4; }
        mysql_target -e "${drop_schema_template/SCHEMA_HEX/${target_schema_hex}}"
      done
      if [[ "${#user_databases[@]}" -gt 0 ]]; then
        mysql_target <"${donor_dump_file}"
      elif [[ -n "${donor_gtid}" ]]; then
        printf 'SET GLOBAL GTID_PURGED=%s;\n' "$(sql_quote "${donor_gtid}")" | mysql_target
      fi
    fi
    ;;
esac

mysql_target <<'SQL'
SET GLOBAL read_only=ON;
SET GLOBAL super_read_only=ON;
SQL

target_version="$(mysql_target --batch --skip-column-names -e 'SELECT @@version')"
major_minor="$(awk -F. '{print $1"."$2}' <<<"${target_version}")"
donor_secret="$(sql_quote "${replication_password}")"
mysql_target <<'SQL'
SET GLOBAL read_only=ON;
SET GLOBAL super_read_only=ON;
SQL
if [[ "${major_minor}" == "5.7" ]]; then
  mysql_target -e 'STOP SLAVE; RESET SLAVE ALL;' || true
	printf "CHANGE MASTER TO MASTER_HOST='%s', MASTER_PORT=%s, MASTER_USER='%s', MASTER_PASSWORD=%s, MASTER_AUTO_POSITION=1, MASTER_CONNECT_RETRY=5, MASTER_RETRY_COUNT=86400;\nSTART SLAVE;\n" \
		"${donor_host}" "${donor_port}" "${replication_user}" "${donor_secret}" | mysql_target
  status_command='SHOW SLAVE STATUS'
  io_pattern='Slave_IO_Running: Yes'
  sql_pattern='Slave_SQL_Running: Yes'
else
  mysql_target -e 'STOP REPLICA; RESET REPLICA ALL;' || true
	printf "CHANGE REPLICATION SOURCE TO SOURCE_HOST='%s', SOURCE_PORT=%s, SOURCE_USER='%s', SOURCE_PASSWORD=%s, SOURCE_AUTO_POSITION=1, GET_SOURCE_PUBLIC_KEY=1, SOURCE_CONNECT_RETRY=5, SOURCE_RETRY_COUNT=86400;\nSTART REPLICA;\n" \
		"${donor_host}" "${donor_port}" "${replication_user}" "${donor_secret}" | mysql_target
  status_command='SHOW REPLICA STATUS'
  io_pattern='Replica_IO_Running: Yes'
  sql_pattern='Replica_SQL_Running: Yes'
fi

replication_verify_timeout="${CG_MYSQL_REPLICATION_VERIFY_TIMEOUT:-180}"
[[ "${replication_verify_timeout}" =~ ^[0-9]+$ ]] || { echo "invalid replication verification timeout" >&2; exit 2; }
(( replication_verify_timeout >= 30 && replication_verify_timeout <= 900 )) || { echo "replication verification timeout must be between 30 and 900 seconds" >&2; exit 2; }
verified=false
status=""
verification_deadline=$((SECONDS + replication_verify_timeout))
while (( SECONDS < verification_deadline )); do
  status="$(mysql_target --vertical -e "${status_command}" 2>/dev/null || true)"
  if grep -Fq "${io_pattern}" <<<"${status}" && grep -Fq "${sql_pattern}" <<<"${status}"; then
    verified=true
    break
  fi
  sleep 5
done
if [[ "${verified}" != "true" ]]; then
  diagnostic="$(grep -E '^[[:space:]]*(Replica|Slave)_(IO|SQL)_Running:|^[[:space:]]*Last_(IO|SQL)_(Errno|Error):' <<<"${status}" | tr '\n' ';' | cut -c1-2048 || true)"
  echo "replication did not become healthy within ${replication_verify_timeout} seconds: ${diagnostic}" >&2
  exit 5
fi

read -r server_uuid reported_host reported_port version read_only super_read_only < <(
  mysql_target --batch --skip-column-names -e 'SELECT @@server_uuid, @@hostname, @@port, @@version, @@read_only, @@super_read_only'
)
[[ -n "${server_uuid}" && "${read_only}" == "1" && "${super_read_only}" == "1" ]] || { echo "target identity or read-only verification failed" >&2; exit 5; }
if [[ -n "${vip}" ]] && ip -4 -o addr show | grep -Fq " ${vip}/"; then
  echo "target unexpectedly owns the cluster VIP" >&2
  exit 5
fi

	donor_gtid="$(mysql_donor --batch --raw --skip-column-names -e 'SELECT @@GLOBAL.gtid_executed')"
install -d -m 0750 "${sync_state_dir}"
verified_sync_marker_tmp="$(mktemp "${sync_state_dir}/.last-verified-sync.XXXXXX")"
jq -nc --arg server_uuid "${server_uuid}" --arg donor_gtid "${donor_gtid}" --arg strategy "${sync_strategy}" \
  '{server_uuid:$server_uuid,donor_gtid:$donor_gtid,strategy:$strategy,verified:true}' >"${verified_sync_marker_tmp}"
chmod 0600 "${verified_sync_marker_tmp}"
mv -f "${verified_sync_marker_tmp}" "${verified_sync_marker}"
verified_sync_marker_tmp=""

jq -nc \
  --arg cluster_id "${cluster_id}" --arg node_id "${node_id}" --arg node_name "${node_name}" \
  --arg hostname "${target_host:-${reported_host}}" --arg ip_address "${target_ip}" --argjson port "${reported_port}" \
  --arg server_uuid "${server_uuid}" --arg version "${version}" \
  '{cluster_id:$cluster_id,node_id:$node_id,engine:"mysql",engine_identity:{server_uuid:$server_uuid},display_name:$node_name,hostname:$hostname,ip_address:$ip_address,port:$port,role:"replica",health:{state:"healthy"},replication:{io_thread:"running",sql_thread:"running"},promotion_eligible:true,engine_metadata:{version:$version}}'
