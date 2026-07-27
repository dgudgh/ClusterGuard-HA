#!/usr/bin/env bash
set -euo pipefail

method="${1:-}"
case "${method}" in pg_basebackup|pg_rewind) ;; *) echo "unsupported PostgreSQL sync method" >&2; exit 2 ;; esac
command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }

payload="$(mktemp /tmp/clusterguard-postgresql-sync.XXXXXX)"
donor_admin_pass="$(mktemp /tmp/clusterguard-postgresql-admin.XXXXXX)"
donor_replication_pass="$(mktemp /tmp/clusterguard-postgresql-repl.XXXXXX)"
replication_sql="$(mktemp /tmp/clusterguard-postgresql-role.XXXXXX)"
chmod 0600 "${payload}" "${donor_admin_pass}" "${donor_replication_pass}" "${replication_sql}"
cleanup() {
  rm -f "${payload}" "${donor_admin_pass}" "${donor_replication_pass}" "${replication_sql}"
}
trap cleanup EXIT
cat >"${payload}"
jq -e '.request.cluster_id and .request.donor.instance_id and .request.donor.system_identifier and .request.donor.port and .target.node_id and .target.postgresql_port and .secrets.postgresql_admin_password and .secrets.postgresql_replication_password' "${payload}" >/dev/null

cluster_id="$(jq -r '.request.cluster_id' "${payload}")"
source_node_id="$(jq -r '.request.donor.native_resource_id // .request.donor.instance_id' "${payload}")"
source_system_id="$(jq -r '.request.donor.system_identifier' "${payload}")"
source_host="$(jq -r '.request.donor.ip_address // .request.donor.hostname' "${payload}")"
source_port="$(jq -r '.request.donor.port' "${payload}")"
node_id="$(jq -r '.target.node_id' "${payload}")"
node_name="$(jq -r '.target.node_name' "${payload}")"
target_host="$(jq -r '.target.hostname' "${payload}")"
target_ip="$(jq -r '.target.ip_address // ""' "${payload}")"
target_port="$(jq -r '.target.postgresql_port' "${payload}")"
target_service="$(jq -r '.target.postgresql_service // ""' "${payload}")"
target_data_directory="$(jq -r '.target.postgresql_data_directory // ""' "${payload}")"
vip="$(jq -r '.request.vip // ""' "${payload}")"
admin_secret="$(jq -r '.secrets.postgresql_admin_password' "${payload}")"
replication_secret="$(jq -r '.secrets.postgresql_replication_password' "${payload}")"
replication_user="clusterguard_repl"

valid_platform_uuid() {
  [[ "$1" =~ ^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$ ]]
}

valid_platform_uuid "${cluster_id}" || { echo "invalid PostgreSQL cluster identity" >&2; exit 2; }
[[ "${source_host}" =~ ^[A-Za-z0-9._:-]+$ ]] || { echo "invalid PostgreSQL donor endpoint" >&2; exit 2; }
[[ "${source_port}" =~ ^[0-9]+$ && "${source_port}" -ge 1 && "${source_port}" -le 65535 ]] || { echo "invalid PostgreSQL donor port" >&2; exit 2; }
[[ "${target_port}" =~ ^[0-9]+$ && "${target_port}" -ge 1 && "${target_port}" -le 65535 ]] || { echo "invalid PostgreSQL target port" >&2; exit 2; }
[[ "${source_system_id}" =~ ^[0-9]+$ && "${source_system_id}" != "0" ]] || { echo "invalid PostgreSQL donor system_identifier" >&2; exit 2; }
valid_platform_uuid "${node_id}" && valid_platform_uuid "${source_node_id}" || { echo "invalid PostgreSQL platform identity" >&2; exit 2; }
[[ "${source_node_id}" != "${node_id}" ]] || { echo "PostgreSQL donor and target must be distinct platform resources" >&2; exit 2; }
[[ "${node_name}" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "invalid fixed PostgreSQL node name" >&2; exit 2; }
[[ "${target_host}" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "invalid PostgreSQL target hostname" >&2; exit 2; }
[[ -z "${target_ip}" || "${target_ip}" =~ ^[A-Za-z0-9._:-]+$ ]] || { echo "invalid PostgreSQL target IP address" >&2; exit 2; }
[[ -n "${admin_secret}" && -n "${replication_secret}" ]] || { echo "PostgreSQL synchronization credentials are required" >&2; exit 2; }
if [[ "${source_port}" == "${target_port}" ]] && { [[ "${source_host}" == "${target_host}" ]] || [[ -n "${target_ip}" && "${source_host}" == "${target_ip}" ]]; }; then
  echo "PostgreSQL donor endpoint cannot equal the target endpoint" >&2
  exit 2
fi

software_root="/opt/clusterguard/postgresql/${target_port}/software"
data_directory="${target_data_directory:-/var/lib/clusterguard/postgresql/${target_port}/data}"
service="${target_service:-clusterguard-postgresql-${target_port}.service}"
target_pass="/etc/clusterguard/postgresql/${target_port}.pass"
replication_pass="${target_pass}"
stage_directory="${data_directory}.clusterguard-stage"
backup_directory="${data_directory}.clusterguard-backup"

safe_data_directory() {
  local path="$1"
  [[ "${path}" == /* && "${path}" =~ ^/[A-Za-z0-9._/-]+$ ]] || return 1
  [[ "${path}" != "/" && "${path}" != "/var" && "${path}" != "/var/lib" && "${path}" != "/opt" && "${path}" != "/etc" ]] || return 1
  [[ "${path}" != *"/../"* && "${path}" != */.. && "${path}" != *"/./"* && "${path}" != */. && "${path}" != *"//"* ]] || return 1
  [[ "$(awk -F/ '{print NF-1}' <<<"${path}")" -ge 3 ]] || return 1
  [[ ! -L "${path}" ]] || return 1
}
safe_data_directory "${data_directory}" || { echo "unsafe PostgreSQL data directory" >&2; exit 2; }
[[ "${service}" =~ ^[A-Za-z0-9@_.-]+[.]service$ ]] || { echo "invalid PostgreSQL service name" >&2; exit 2; }

pgpass_escape() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//:/\\:}"
  printf '%s' "${value}"
}

printf '%s:%s:*:postgres:%s\n' "$(pgpass_escape "${source_host}")" "${source_port}" "$(pgpass_escape "${admin_secret}")" >"${donor_admin_pass}"
printf '%s:%s:*:%s:%s\n' "$(pgpass_escape "${source_host}")" "${source_port}" "${replication_user}" "$(pgpass_escape "${replication_secret}")" >"${donor_replication_pass}"
printf '127.0.0.1:%s:*:postgres:%s\n' "${target_port}" "$(pgpass_escape "${admin_secret}")" >"${target_pass}"
printf '*:*:*:postgres:%s\n' "$(pgpass_escape "${admin_secret}")" >>"${target_pass}"
printf '%s:%s:*:%s:%s\n' "$(pgpass_escape "${source_host}")" "${source_port}" "${replication_user}" "$(pgpass_escape "${replication_secret}")" >>"${replication_pass}"
printf '*:*:*:%s:%s\n' "${replication_user}" "$(pgpass_escape "${replication_secret}")" >>"${replication_pass}"
chmod 0600 "${donor_admin_pass}" "${donor_replication_pass}" "${target_pass}" "${replication_pass}"
chown postgres:postgres "${target_pass}"

psql_binary=""
for candidate in "${software_root}/bin/psql" "${CG_POSTGRESQL_PSQL:-}" "$(command -v psql 2>/dev/null || true)"; do
  if [[ -n "${candidate}" && -x "${candidate}" ]]; then psql_binary="${candidate}"; break; fi
done
pg_basebackup_binary="${software_root}/bin/pg_basebackup"
pg_rewind_binary="${software_root}/bin/pg_rewind"
pg_controldata_binary="${software_root}/bin/pg_controldata"
[[ -n "${psql_binary}" && -x "${pg_basebackup_binary}" && -x "${pg_rewind_binary}" && -x "${pg_controldata_binary}" ]] || { echo "matching PostgreSQL client tools are unavailable" >&2; exit 3; }

psql_donor() {
  PGPASSFILE="${donor_admin_pass}" "${psql_binary}" --no-password --no-psqlrc --quiet --tuples-only --no-align \
    --host "${source_host}" --port "${source_port}" --username postgres --dbname postgres "$@"
}
psql_target() {
  PGPASSFILE="${target_pass}" "${psql_binary}" --no-password --no-psqlrc --quiet --tuples-only --no-align \
    --host 127.0.0.1 --port "${target_port}" --username postgres --dbname postgres "$@"
}

source_reported_system_id="$(psql_donor --command "SELECT system_identifier::text FROM pg_control_system()")"
source_reported_system_id="$(xargs <<<"${source_reported_system_id}")"
[[ "${source_reported_system_id}" == "${source_system_id}" ]] || { echo "PostgreSQL donor system_identifier changed after planning" >&2; exit 3; }

secret_hex="$(printf '%s' "${replication_secret}" | od -An -tx1 | tr -d ' \n')"
cat >"${replication_sql}" <<EOF
SET password_encryption = 'scram-sha-256';
DO \$clusterguard\$
DECLARE cg_secret text := convert_from(decode('${secret_hex}', 'hex'), 'UTF8');
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '${replication_user}') THEN
    EXECUTE format('CREATE ROLE ${replication_user} WITH LOGIN REPLICATION PASSWORD %L', cg_secret);
  ELSE
    EXECUTE format('ALTER ROLE ${replication_user} WITH LOGIN REPLICATION PASSWORD %L', cg_secret);
  END IF;
END
\$clusterguard\$;
EOF
chmod 0600 "${replication_sql}"
psql_donor --file "${replication_sql}" >/dev/null

systemctl stop "${service}"
for _ in $(seq 1 30); do
  if ! systemctl is-active --quiet "${service}"; then break; fi
  sleep 1
done
if systemctl is-active --quiet "${service}"; then
  echo "PostgreSQL target did not stop before synchronization" >&2
  exit 4
fi

write_recovery_identity() {
  local directory="$1" auto_conf="${1}/postgresql.auto.conf"
  touch "${auto_conf}" "${directory}/standby.signal"
  sed -i '/^[[:space:]]*clusterguard[.]node_id[[:space:]]*=/d;/^[[:space:]]*clusterguard[.]primary_node_id[[:space:]]*=/d;/^[[:space:]]*clusterguard[.]hostname[[:space:]]*=/d;/^[[:space:]]*primary_conninfo[[:space:]]*=/d' "${auto_conf}"
  cat >>"${auto_conf}" <<EOF
clusterguard.node_id = '${node_id}'
clusterguard.primary_node_id = '${source_node_id}'
clusterguard.hostname = '${target_host}'
primary_conninfo = 'host=${source_host} port=${source_port} user=${replication_user} dbname=postgres passfile=${replication_pass} application_name=${node_id} connect_timeout=5'
EOF
}

data_system_identifier() {
  LC_ALL=C "${pg_controldata_binary}" "$1" | awk -F: '/Database system identifier/{gsub(/^[[:space:]]+|[[:space:]]+$/, "", $2); print $2; exit}'
}

case "${method}" in
  pg_basebackup)
    rm -rf "${stage_directory}"
    [[ ! -e "${backup_directory}" ]] || { echo "stale PostgreSQL backup quarantine must be reviewed before synchronization" >&2; exit 4; }
    mkdir -p "$(dirname "${stage_directory}")"
    PGPASSFILE="${donor_replication_pass}" "${pg_basebackup_binary}" --no-password \
      --host "${source_host}" --port "${source_port}" --username "${replication_user}" \
      --pgdata "${stage_directory}" --write-recovery-conf --checkpoint=fast --wal-method=stream --progress
    [[ -f "${stage_directory}/PG_VERSION" ]] || { rm -rf "${stage_directory}"; echo "pg_basebackup did not produce PG_VERSION" >&2; exit 4; }
    [[ "$(data_system_identifier "${stage_directory}")" == "${source_system_id}" ]] || { rm -rf "${stage_directory}"; echo "pg_basebackup system_identifier mismatch" >&2; exit 4; }
    write_recovery_identity "${stage_directory}"
    chown -R postgres:postgres "${stage_directory}"
    chmod 0700 "${stage_directory}"
    if [[ -e "${data_directory}" ]]; then mv "${data_directory}" "${backup_directory}"; fi
    if ! mv "${stage_directory}" "${data_directory}"; then
      [[ ! -e "${backup_directory}" ]] || mv "${backup_directory}" "${data_directory}"
      echo "failed to activate PostgreSQL base backup" >&2
      exit 4
    fi
    ;;
  pg_rewind)
    [[ -f "${data_directory}/PG_VERSION" ]] || { echo "pg_rewind requires an existing registered data directory" >&2; exit 4; }
    [[ "$(data_system_identifier "${data_directory}")" == "${source_system_id}" ]] || { echo "pg_rewind requires the donor system_identifier" >&2; exit 4; }
    PGPASSFILE="${donor_admin_pass}" "${pg_rewind_binary}" --target-pgdata="${data_directory}" \
      --source-server="host=${source_host} port=${source_port} user=postgres dbname=postgres passfile=${donor_admin_pass} connect_timeout=5" --progress
    write_recovery_identity "${data_directory}"
    chown -R postgres:postgres "${data_directory}"
    ;;
esac

start_failed=false
systemctl start "${service}" || start_failed=true
verified=false
verification=""
if [[ "${start_failed}" == "false" ]]; then
  for _ in $(seq 1 60); do
    verification="$(psql_target --field-separator='|' --command "SELECT pg_is_in_recovery(), current_setting('clusterguard.node_id', true), current_setting('clusterguard.primary_node_id', true), system_identifier::text, current_setting('server_version'), COALESCE((SELECT status FROM pg_stat_wal_receiver LIMIT 1), '') FROM pg_control_system()" 2>/dev/null || true)"
    verification="$(xargs <<<"${verification}")"
    IFS='|' read -r in_recovery reported_node_id reported_primary_node_id reported_system_id version receiver_status <<<"${verification}"
    if [[ "${in_recovery}" == "t" && "${reported_node_id}" == "${node_id}" && "${reported_primary_node_id}" == "${source_node_id}" && "${reported_system_id}" == "${source_system_id}" && "${receiver_status}" == "streaming" ]]; then
      verified=true
      break
    fi
    sleep 1
  done
fi

if [[ "${verified}" != "true" ]]; then
  systemctl stop "${service}" || true
  if [[ "${method}" == "pg_basebackup" && -e "${backup_directory}" ]]; then
    mv "${data_directory}" "${stage_directory}.failed" || true
    mv "${backup_directory}" "${data_directory}" || true
  fi
  echo "PostgreSQL synchronization verification failed; target remains stopped" >&2
  exit 5
fi

if [[ -n "${vip}" ]] && ip -4 -o addr show | grep -Fq " ${vip}/"; then
  systemctl stop "${service}" || true
  echo "target unexpectedly owns the cluster VIP" >&2
  exit 5
fi

[[ ! -e "${backup_directory}" ]] || rm -rf "${backup_directory}"
lag_seconds="$(psql_target --command "SELECT COALESCE(GREATEST(0, FLOOR(EXTRACT(EPOCH FROM clock_timestamp() - pg_last_xact_replay_timestamp())))::bigint, 0)")"
lag_seconds="$(xargs <<<"${lag_seconds}")"
jq -nc \
  --arg cluster_id "${cluster_id}" --arg node_id "${node_id}" --arg node_name "${node_name}" \
  --arg hostname "${target_host}" --arg ip_address "${target_ip}" --argjson port "${target_port}" \
  --arg source_node_id "${source_node_id}" --arg system_identifier "${source_system_id}" --arg version "${version}" \
  --argjson lag_seconds "${lag_seconds:-0}" \
  '{cluster_id:$cluster_id,node_id:$node_id,engine:"postgresql",engine_identity:{resource_id:$node_id,system_identifier:$system_identifier},display_name:$node_name,hostname:$hostname,ip_address:$ip_address,port:$port,role:"standby",health:{state:"healthy",summary:"PostgreSQL standby is read-only and streaming WAL"},replication:{source_identity:{resource_id:$source_node_id,system_identifier:$system_identifier},io_thread:"running",sql_thread:"running",lag_seconds:$lag_seconds},promotion_eligible:true,engine_metadata:{version:$version,system_identifier:$system_identifier,in_recovery:"true",transaction_read_only:"true",wal_receiver_status:"streaming"}}'
