#!/usr/bin/env bash
set -euo pipefail

command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }

payload="$(mktemp /tmp/clusterguard-postgresql-install.XXXXXX)"
init_secret_file=""
cleanup() {
  [[ -z "${init_secret_file}" ]] || rm -f "${init_secret_file}"
  rm -f "${payload}"
}
chmod 0600 "${payload}"
trap cleanup EXIT
cat >"${payload}"
jq -e '.target.node_id and .target.node_name and .target.hostname and .target.postgresql_version and .target.postgresql_port and .secrets.postgresql_admin_password' "${payload}" >/dev/null

node_id="$(jq -r '.target.node_id' "${payload}")"
node_name="$(jq -r '.target.node_name' "${payload}")"
hostname_value="$(jq -r '.target.hostname' "${payload}")"
target_ipv4="$(jq -r '.target.ip_address // ""' "${payload}")"
requested_version="$(jq -r '.target.postgresql_version' "${payload}")"
port="$(jq -r '.target.postgresql_port' "${payload}")"
package_path="$(jq -r '.target.package_path // ""' "${payload}")"
rebuild="$(jq -r '.target.rebuild // false' "${payload}")"
admin_secret="$(jq -r '.secrets.postgresql_admin_password' "${payload}")"
requested_service="$(jq -r '.target.postgresql_service // ""' "${payload}")"
requested_data_directory="$(jq -r '.target.postgresql_data_directory // ""' "${payload}")"

valid_platform_uuid() {
  [[ "$1" =~ ^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$ ]]
}

valid_platform_uuid "${node_id}" || { echo "invalid PostgreSQL platform node identity" >&2; exit 2; }
[[ "${node_name}" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "invalid fixed node name" >&2; exit 2; }
[[ "${hostname_value}" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "invalid PostgreSQL hostname" >&2; exit 2; }
[[ "${requested_version}" =~ ^[0-9]+([.][0-9]+)*$ ]] || { echo "invalid PostgreSQL version" >&2; exit 2; }
[[ "${port}" =~ ^[0-9]+$ && "${port}" -ge 1 && "${port}" -le 65535 ]] || { echo "invalid PostgreSQL port" >&2; exit 2; }
[[ -n "${admin_secret}" ]] || { echo "PostgreSQL administrator credential is required" >&2; exit 2; }

install_root="/opt/clusterguard/postgresql/${port}"
software_root="${install_root}/software"
data_directory="${requested_data_directory:-/var/lib/clusterguard/postgresql/${port}/data}"
log_directory="/var/log/clusterguard/postgresql/${port}"
run_directory="/run/clusterguard/postgresql/${port}"
config_directory="/etc/clusterguard/postgresql"
pass_file="${config_directory}/${port}.pass"
service="${requested_service:-clusterguard-postgresql-${port}.service}"

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

mkdir -p "${config_directory}" "${log_directory}" "${run_directory}" "$(dirname "${data_directory}")" "${install_root}"
chmod 0750 "${config_directory}" "${log_directory}" "$(dirname "${data_directory}")"
if ! getent group postgres >/dev/null 2>&1; then groupadd --system postgres; fi
if ! id postgres >/dev/null 2>&1; then useradd --system --gid postgres --home-dir /var/lib/postgresql --shell /sbin/nologin postgres; fi
chown root:postgres "${config_directory}"

umask 077
printf '127.0.0.1:%s:*:postgres:%s\n' "${port}" "$(pgpass_escape "${admin_secret}")" >"${pass_file}"
printf '*:*:*:postgres:%s\n' "$(pgpass_escape "${admin_secret}")" >>"${pass_file}"
chmod 0600 "${pass_file}"
chown postgres:postgres "${pass_file}"

managed_psql="${software_root}/bin/psql"
psql_binary=""
for candidate in "${managed_psql}" "${CG_POSTGRESQL_PSQL:-}" "$(command -v psql 2>/dev/null || true)"; do
  if [[ -n "${candidate}" && -x "${candidate}" ]]; then
    psql_binary="${candidate}"
    break
  fi
done

if [[ -n "${psql_binary}" && -f "${data_directory}/PG_VERSION" ]] && \
  PGPASSFILE="${pass_file}" "${psql_binary}" --no-password --no-psqlrc --quiet --tuples-only --no-align \
    --host 127.0.0.1 --port "${port}" --username postgres --dbname postgres --command 'SELECT 1' >/dev/null 2>&1; then
  printf 'existing PostgreSQL instance accepted for synchronization\n'
  exit 0
fi

if [[ -z "${package_path}" && "${rebuild}" == "true" && -f "${data_directory}/PG_VERSION" ]] && systemctl cat "${service}" >/dev/null 2>&1; then
  system_postgres="$(command -v postgres 2>/dev/null || true)"
  system_psql="$(command -v psql 2>/dev/null || true)"
  system_pg_basebackup="$(command -v pg_basebackup 2>/dev/null || true)"
  system_pg_rewind="$(command -v pg_rewind 2>/dev/null || true)"
  system_pg_controldata="$(command -v pg_controldata 2>/dev/null || true)"
  if [[ -x "${system_postgres}" && -x "${system_psql}" && -x "${system_pg_basebackup}" && -x "${system_pg_rewind}" && -x "${system_pg_controldata}" ]]; then
    installed_version="$("${system_postgres}" --version | awk '{print $NF}')"
    [[ "${installed_version%%.*}" == "${requested_version%%.*}" ]] || { echo "installed PostgreSQL major release does not match the requested version" >&2; exit 3; }
    mkdir -p "${software_root}/bin"
    ln -sfn "${system_postgres}" "${software_root}/bin/postgres"
    ln -sfn "${system_psql}" "${software_root}/bin/psql"
    ln -sfn "${system_pg_basebackup}" "${software_root}/bin/pg_basebackup"
    ln -sfn "${system_pg_rewind}" "${software_root}/bin/pg_rewind"
    ln -sfn "${system_pg_controldata}" "${software_root}/bin/pg_controldata"
    systemctl stop "${service}" || true
    printf 'existing PostgreSQL toolchain accepted for rebuild synchronization\n'
    exit 0
  fi
fi

[[ -n "${package_path}" && -f "${package_path}" ]] || { echo "a local PostgreSQL binary package is required" >&2; exit 3; }
if tar -tf "${package_path}" | awk '/(^\/|(^|\/)\.\.($|\/))/{bad=1} END{exit !bad}'; then
  echo "PostgreSQL package contains an unsafe archive path" >&2
  exit 3
fi

if [[ -f "${data_directory}/PG_VERSION" ]]; then
  [[ "${rebuild}" == "true" ]] || { echo "existing PostgreSQL data directory requires an explicit rebuild" >&2; exit 3; }
  [[ -f "/etc/systemd/system/${service}" ]] || { echo "refusing to rebuild an unmanaged PostgreSQL instance" >&2; exit 3; }
  systemctl stop "${service}" || true
else
  mkdir -p "${data_directory}"
fi

rm -rf "${software_root}"
mkdir -p "${software_root}"
tar -xf "${package_path}" -C "${software_root}" --strip-components=1
for binary in initdb postgres psql pg_ctl pg_basebackup pg_rewind pg_controldata; do
  [[ -x "${software_root}/bin/${binary}" ]] || { echo "PostgreSQL package does not contain ${binary}" >&2; exit 3; }
done

installed_version="$(${software_root}/bin/postgres --version | awk '{print $NF}')"
[[ "${installed_version%%.*}" == "${requested_version%%.*}" ]] || { echo "PostgreSQL package major release does not match the requested version" >&2; exit 3; }

if [[ ! -f "${data_directory}/PG_VERSION" ]]; then
  init_secret_file="$(mktemp /tmp/clusterguard-postgresql-init.XXXXXX)"
  chmod 0600 "${init_secret_file}"
  printf '%s\n' "${admin_secret}" >"${init_secret_file}"
  chown -R postgres:postgres "${data_directory}" "${log_directory}" "${run_directory}"
  runuser -u postgres -- "${software_root}/bin/initdb" --pgdata="${data_directory}" --username=postgres \
    --pwfile="${init_secret_file}" --auth-local=peer --auth-host=scram-sha-256 --encoding=UTF8 >/dev/null
fi

valid_ipv4() {
  local address="$1" octet
  [[ "${address}" =~ ^([0-9]{1,3}[.]){3}[0-9]{1,3}$ ]] || return 1
  IFS=. read -r -a octets <<<"${address}"
  for octet in "${octets[@]}"; do ((10#${octet} <= 255)) || return 1; done
}

allowed_cidr="${CG_POSTGRESQL_ALLOWED_CIDR:-}"
if [[ -z "${allowed_cidr}" ]]; then
  valid_ipv4 "${target_ipv4}" || { echo "CG_POSTGRESQL_ALLOWED_CIDR is required when target IPv4 is unavailable" >&2; exit 3; }
  allowed_cidr="${target_ipv4%.*}.0/24"
fi
if [[ "${allowed_cidr}" =~ ^([0-9]{1,3}[.]){3}[0-9]{1,3}/([0-9]|[12][0-9]|3[0-2])$ ]]; then
  valid_ipv4 "${allowed_cidr%/*}" || { echo "invalid PostgreSQL allowed CIDR" >&2; exit 3; }
elif [[ ! "${allowed_cidr}" =~ ^[0-9A-Fa-f:]+/([0-9]|[1-9][0-9]|1[01][0-9]|12[0-8])$ ]]; then
  echo "invalid PostgreSQL allowed CIDR" >&2
  exit 3
fi
sed -i '/^# BEGIN ClusterGuard managed PostgreSQL settings$/,/^# END ClusterGuard managed PostgreSQL settings$/d' "${data_directory}/postgresql.conf"
cat >>"${data_directory}/postgresql.conf" <<EOF

# BEGIN ClusterGuard managed PostgreSQL settings
listen_addresses = '*'
port = ${port}
wal_level = replica
hot_standby = on
wal_log_hints = on
max_wal_senders = 16
max_replication_slots = 16
password_encryption = 'scram-sha-256'
unix_socket_directories = '${run_directory}'
logging_collector = on
log_directory = '${log_directory}'
log_filename = 'postgresql-%Y-%m-%d.log'
clusterguard.node_id = '${node_id}'
clusterguard.primary_node_id = ''
clusterguard.hostname = '${hostname_value}'
# END ClusterGuard managed PostgreSQL settings
EOF
sed -i '/^# BEGIN ClusterGuard managed PostgreSQL access$/,/^# END ClusterGuard managed PostgreSQL access$/d' "${data_directory}/pg_hba.conf"
cat >>"${data_directory}/pg_hba.conf" <<EOF
# BEGIN ClusterGuard managed PostgreSQL access
host all all ${allowed_cidr} scram-sha-256
host replication clusterguard_repl ${allowed_cidr} scram-sha-256
# END ClusterGuard managed PostgreSQL access
EOF
chown -R postgres:postgres "${data_directory}" "${log_directory}" "${run_directory}"
chmod 0700 "${data_directory}"

cat >"/etc/systemd/system/${service}" <<EOF
[Unit]
Description=ClusterGuard managed PostgreSQL ${port}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=postgres
Group=postgres
RuntimeDirectory=clusterguard/postgresql/${port}
RuntimeDirectoryMode=0750
ExecStart=${software_root}/bin/postgres -D ${data_directory}
ExecReload=${software_root}/bin/pg_ctl -D ${data_directory} reload
KillSignal=SIGINT
TimeoutStopSec=120
Restart=on-failure
RestartSec=5
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now "${service}"

ready=false
for _ in $(seq 1 60); do
  if PGPASSFILE="${pass_file}" "${software_root}/bin/psql" --no-password --no-psqlrc --quiet --tuples-only --no-align \
    --host 127.0.0.1 --port "${port}" --username postgres --dbname postgres --command 'SELECT 1' >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
[[ "${ready}" == "true" ]] || { echo "PostgreSQL did not become ready within 60 seconds" >&2; exit 4; }

printf 'PostgreSQL installation reconciled\n'
