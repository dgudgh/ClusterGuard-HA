#!/usr/bin/env bash
set -euo pipefail

command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }
payload="$(mktemp /tmp/clusterguard-install.XXXXXX)"
chmod 0600 "${payload}"
trap 'rm -f "${payload}"' EXIT
cat >"${payload}"
jq -e '.target.node_name and .target.mysql_port and .secrets.mysql_root_password and .secrets.mysql_discovery_username and .secrets.mysql_discovery_password and .secrets.mysql_operation_username and .secrets.mysql_operation_password and .secrets.mysql_replication_username and .secrets.replication_password' "${payload}" >/dev/null

port="$(jq -r '.target.mysql_port' "${payload}")"
server_id="$(jq -r '.target.server_id // 0' "${payload}")"
package_path="$(jq -r '.target.package_path // ""' "${payload}")"
rebuild="$(jq -r '.target.rebuild // false' "${payload}")"
root_password="$(jq -r '.secrets.mysql_root_password' "${payload}")"
mysql_root_remote_host="$(jq -r '.target.mysql_root_remote_host // ""' "${payload}")"
discovery_username="$(jq -r '.secrets.mysql_discovery_username' "${payload}")"
discovery_password="$(jq -r '.secrets.mysql_discovery_password' "${payload}")"
operation_username="$(jq -r '.secrets.mysql_operation_username' "${payload}")"
operation_password="$(jq -r '.secrets.mysql_operation_password' "${payload}")"
replication_username="$(jq -r '.secrets.mysql_replication_username' "${payload}")"
replication_password="$(jq -r '.secrets.replication_password' "${payload}")"
[[ "${port}" =~ ^[0-9]+$ && "${port}" -ge 1 && "${port}" -le 65535 ]] || { echo "invalid MySQL port" >&2; exit 2; }
if [[ -n "${mysql_root_remote_host}" && ! "${mysql_root_remote_host}" =~ ^[A-Za-z0-9._:%/-]{1,255}$ ]]; then
  echo "invalid MySQL root remote host" >&2
  exit 2
fi
for mysql_username in "${discovery_username}" "${operation_username}" "${replication_username}"; do
  [[ "${mysql_username}" =~ ^[A-Za-z0-9_]{1,32}$ ]] || { echo "invalid managed MySQL username" >&2; exit 2; }
done

install_root="/opt/clusterguard/mysql/${port}"
state_root="/var/lib/clusterguard"
log_root="/var/log/clusterguard"
config_root="/etc/clusterguard"
data_root="$(jq -r '.target.data_root // "/var/lib/clusterguard"' "${payload}")"
[[ "${data_root}" == /* && "${data_root}" != "/" && "${data_root}" != *$'\n'* && "${data_root}" != *$'\r'* ]] || {
  echo "MySQL data root must be a safe absolute directory" >&2
  exit 2
}
data_root="${data_root%/}"
case "/${data_root#/}/" in
  */../*|*/./*) echo "MySQL data root cannot contain dot path segments" >&2; exit 2 ;;
esac
install_parent="${data_root}/mysql/${port}"
data_dir="${install_parent}/data"
log_dir="/var/log/clusterguard/mysql/${port}"
run_dir="/run/clusterguard/mysql/${port}"
config_dir="/etc/clusterguard/mysql"
config_file="${config_dir}/${port}.cnf"
client_file="${config_dir}/${port}-client.cnf"
system_client_file="${config_dir}/default-client.cnf"
socket="${run_dir}/mysql.sock"
service="clusterguard-mysql-${port}.service"
service_file="/etc/systemd/system/${service}"
managed_marker="${install_parent}/.clusterguard-managed"
installing_marker="${install_parent}/.installing"
managed_mysql="${install_root}/software/bin/mysql"
managed_mysqld="${install_root}/software/bin/mysqld"
managed_mysqladmin="${install_root}/software/bin/mysqladmin"
managed_mysqldump="${install_root}/software/bin/mysqldump"

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
  done < <(ss -H -ltnp "sport = :${port}" 2>/dev/null | grep -oE 'pid=[0-9]+' | cut -d= -f2 | sort -u)
  return 1
}

running_mysql_client="$(discover_running_mysql_client || true)"
mysql_client=""
for candidate in "${managed_mysql}" "${CG_MYSQL_CLIENT:-}" "$(command -v mysql 2>/dev/null || true)" "${running_mysql_client}"; do
  if [[ -n "${candidate}" && -x "${candidate}" ]]; then
    mysql_client="${candidate}"
    break
  fi
done

mkdir -p "${config_dir}" "${log_dir}" "${run_dir}" "${data_root}" "${install_parent}" "${install_root}"
chmod 0750 "${config_dir}" "${log_dir}" "${install_parent}"
if ! getent group mysql >/dev/null 2>&1; then groupadd --system mysql; fi
if ! id mysql >/dev/null 2>&1; then useradd --system --gid mysql --home-dir /nonexistent --shell /sbin/nologin mysql; fi

ensure_mysql_traverse() {
  local directory="$1"
  if ! runuser -u mysql -- test -x "${directory}"; then
    command -v setfacl >/dev/null 2>&1 || { echo "setfacl is required to isolate database and control-plane state" >&2; exit 3; }
    setfacl -m u:mysql:--x "${directory}"
  fi
  runuser -u mysql -- test -x "${directory}" || { echo "mysql cannot traverse protected ClusterGuard directory ${directory}" >&2; exit 3; }
}

ensure_mysql_traverse "${state_root}"
ensure_mysql_traverse "${log_root}"
ensure_mysql_traverse "${config_root}"
ensure_mysql_traverse "${config_dir}"
ensure_mysql_traverse "${data_root}"

write_managed_private_client() {
  umask 077
  cat >"${client_file}" <<EOF
[client]
user=root
password=${root_password}
socket=${socket}
port=${port}
EOF
  chmod 0600 "${client_file}"
  chown root:root "${client_file}"
}

ensure_option_file_include() {
  local option_file="$1" mode="$2" resolved
  if [[ -L "${option_file}" ]]; then
    resolved="$(readlink -f "${option_file}" 2>/dev/null || true)"
    [[ -n "${resolved}" ]] || { echo "cannot resolve MySQL option-file symlink ${option_file}" >&2; exit 3; }
    option_file="${resolved}"
  fi
  [[ ! -e "${option_file}" || -f "${option_file}" ]] || {
    echo "MySQL option file is not a regular file: ${option_file}" >&2
    exit 3
  }
  install -d -m 0755 "$(dirname "${option_file}")"
  touch "${option_file}"
  if ! grep -Fqx "!include ${system_client_file}" "${option_file}"; then
    cat >>"${option_file}" <<EOF

!include ${system_client_file}
EOF
  fi
  chown root:root "${option_file}"
  chmod "${mode}" "${option_file}"
}

install_local_client_defaults() {
  cat >"${system_client_file}" <<EOF
# Managed by ClusterGuard HA. This file contains no credentials.
[client]
socket=${socket}
port=${port}

[mysql]
socket=${socket}
port=${port}

[mysqladmin]
socket=${socket}
port=${port}

[mysqldump]
socket=${socket}
port=${port}
EOF
  chown root:root "${system_client_file}"
  chmod 0644 "${system_client_file}"
  ensure_option_file_include /etc/my.cnf 0644
  ensure_option_file_include /root/.my.cnf 0600

  install -d -m 0755 /usr/local/bin
  ln -sfn "${managed_mysql}" /usr/local/bin/mysql
  ln -sfn "${managed_mysqladmin}" /usr/local/bin/mysqladmin
  ln -sfn "${managed_mysqldump}" /usr/local/bin/mysqldump

  if [[ "${port}" == "3306" ]]; then
    install -d -m 0755 /etc/tmpfiles.d
    cat >"/etc/tmpfiles.d/clusterguard-mysql-${port}.conf" <<EOF
L+ /tmp/mysql.sock - - - - ${socket}
EOF
    chmod 0644 "/etc/tmpfiles.d/clusterguard-mysql-${port}.conf"
    if command -v systemd-tmpfiles >/dev/null 2>&1; then
      systemd-tmpfiles --create "/etc/tmpfiles.d/clusterguard-mysql-${port}.conf"
    else
      ln -sfn "${socket}" /tmp/mysql.sock
    fi
  fi
}

reconcile_control_accounts() {
  local client="$1"
  local discovery_password_hex operation_password_hex replication_password_hex root_password_hex account_status
  local mysql_root_remote_host_sql="" remote_root_account_sql=""
  discovery_password_hex="$(printf '%s' "${discovery_password}" | od -An -tx1 | tr -d ' \n')"
  operation_password_hex="$(printf '%s' "${operation_password}" | od -An -tx1 | tr -d ' \n')"
  replication_password_hex="$(printf '%s' "${replication_password}" | od -An -tx1 | tr -d ' \n')"
  root_password_hex="$(printf '%s' "${root_password}" | od -An -tx1 | tr -d ' \n')"
  if [[ -n "${mysql_root_remote_host}" ]]; then
    mysql_root_remote_host_sql="${mysql_root_remote_host}"
    remote_root_account_sql="$(cat <<SQL
SET @cg_remote_root_password=CONVERT(0x${root_password_hex} USING utf8mb4);
SET @cg_statement=CONCAT("CREATE USER IF NOT EXISTS 'root'@'${mysql_root_remote_host_sql}'", @cg_authentication_clause, QUOTE(@cg_remote_root_password));
PREPARE cg_account FROM @cg_statement;
EXECUTE cg_account;
DEALLOCATE PREPARE cg_account;
SET @cg_statement=CONCAT("ALTER USER 'root'@'${mysql_root_remote_host_sql}'", @cg_authentication_clause, QUOTE(@cg_remote_root_password));
PREPARE cg_account FROM @cg_statement;
EXECUTE cg_account;
DEALLOCATE PREPARE cg_account;
GRANT ALL PRIVILEGES ON *.* TO 'root'@'${mysql_root_remote_host_sql}' WITH GRANT OPTION;
SQL
)"
  fi
  set +e
  "${client}" --defaults-file="${client_file}" --protocol=tcp --host=127.0.0.1 --port="${port}" <<SQL
SET GLOBAL read_only=ON;
SET GLOBAL super_read_only=OFF;
SET sql_log_bin=0;
SET @cg_native_password_available=(SELECT COUNT(*) > 0 FROM information_schema.plugins WHERE plugin_name='mysql_native_password' AND plugin_status='ACTIVE');
SET @cg_authentication_clause=IF(@cg_native_password_available, ' IDENTIFIED WITH mysql_native_password BY ', ' IDENTIFIED BY ');
SET @cg_discovery_password=CONVERT(0x${discovery_password_hex} USING utf8mb4);
SET @cg_statement=CONCAT("CREATE USER IF NOT EXISTS '${discovery_username}'@'%'", @cg_authentication_clause, QUOTE(@cg_discovery_password));
PREPARE cg_account FROM @cg_statement;
EXECUTE cg_account;
DEALLOCATE PREPARE cg_account;
SET @cg_statement=CONCAT("ALTER USER '${discovery_username}'@'%'", @cg_authentication_clause, QUOTE(@cg_discovery_password));
PREPARE cg_account FROM @cg_statement;
EXECUTE cg_account;
DEALLOCATE PREPARE cg_account;
GRANT SELECT, PROCESS, REPLICATION CLIENT ON *.* TO '${discovery_username}'@'%';
SET @cg_operation_password=CONVERT(0x${operation_password_hex} USING utf8mb4);
SET @cg_statement=CONCAT("CREATE USER IF NOT EXISTS '${operation_username}'@'%'", @cg_authentication_clause, QUOTE(@cg_operation_password));
PREPARE cg_account FROM @cg_statement;
EXECUTE cg_account;
DEALLOCATE PREPARE cg_account;
SET @cg_statement=CONCAT("ALTER USER '${operation_username}'@'%'", @cg_authentication_clause, QUOTE(@cg_operation_password));
PREPARE cg_account FROM @cg_statement;
EXECUTE cg_account;
DEALLOCATE PREPARE cg_account;
GRANT ALL PRIVILEGES ON *.* TO '${operation_username}'@'%' WITH GRANT OPTION;
SET @cg_replication_password=CONVERT(0x${replication_password_hex} USING utf8mb4);
SET @cg_statement=CONCAT("CREATE USER IF NOT EXISTS '${replication_username}'@'%'", @cg_authentication_clause, QUOTE(@cg_replication_password));
PREPARE cg_account FROM @cg_statement;
EXECUTE cg_account;
DEALLOCATE PREPARE cg_account;
SET @cg_statement=CONCAT("ALTER USER '${replication_username}'@'%'", @cg_authentication_clause, QUOTE(@cg_replication_password));
PREPARE cg_account FROM @cg_statement;
EXECUTE cg_account;
DEALLOCATE PREPARE cg_account;
GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO '${replication_username}'@'%';
${remote_root_account_sql}
SET GLOBAL read_only=ON;
SET GLOBAL super_read_only=ON;
SQL
  account_status=$?
  set -e
  if [[ "${account_status}" -ne 0 ]]; then
    "${client}" --defaults-file="${client_file}" --protocol=tcp --host=127.0.0.1 --port="${port}" \
      -e 'SET GLOBAL read_only=ON; SET GLOBAL super_read_only=ON;' >/dev/null 2>&1 || true
    echo "managed MySQL control-plane account reconciliation failed; replica read-only protection was restored" >&2
    return 3
  fi
}

if [[ -n "${mysql_client}" && -f "${client_file}" ]] && "${mysql_client}" --defaults-file="${client_file}" --protocol=tcp --host=127.0.0.1 --port="${port}" --batch --skip-column-names -e 'SELECT @@server_uuid' >/dev/null 2>&1; then
  write_managed_private_client
  install_local_client_defaults
  reconcile_control_accounts "${mysql_client}"
  printf 'existing MySQL instance accepted for synchronization\n'
  exit 0
fi

[[ -n "${package_path}" && -f "${package_path}" ]] || { echo "a local MySQL binary package is required" >&2; exit 3; }
tar -tf "${package_path}" >/dev/null 2>&1 || { echo "MySQL package is not a readable tar archive" >&2; exit 3; }
if ! tar -tf "${package_path}" | awk '
  /(^\/|(^|\/)\.\.($|\/))/ {bad=1}
  {
    original=$0
    path=$0
    sub(/^\.\//, "", path)
    sub(/\/$/, "", path)
    if (path == "") next
    count=split(path, parts, "/")
    if (root == "") root=parts[1]
    if (parts[1] != root) bad=1
    if (count == 1 && original !~ /\/$/) bad=1
  }
  END {exit !(root != "" && !bad)}
'; then
  echo "MySQL package must contain one safe top-level directory" >&2
  exit 3
fi
managed_evidence=false
if [[ -f "${managed_marker}" || -f "${service_file}" ]]; then
  managed_evidence=true
elif [[ -f "${config_file}" && -x "${managed_mysqld}" ]] && grep -Fqx "datadir=${data_dir}" "${config_file}"; then
  # Upgrade compatibility for an installation interrupted before managed markers existed.
  managed_evidence=true
fi
if [[ -d "${data_dir}/mysql" ]]; then
  [[ "${rebuild}" == "true" ]] || { echo "existing unmanaged MySQL data directory blocks installation" >&2; exit 3; }
  [[ "${managed_evidence}" == "true" ]] || { echo "refusing to rebuild an unmanaged MySQL instance" >&2; exit 3; }
  systemctl stop "${service}" || true
  rm -rf "${data_dir}"
elif [[ -d "${data_dir}" ]] && [[ -n "$(find "${data_dir}" -mindepth 1 -print -quit 2>/dev/null)" ]]; then
  [[ "${managed_evidence}" == "true" || -f "${installing_marker}" ]] || {
    echo "refusing to replace an unmanaged partial MySQL data directory" >&2
    exit 3
  }
  rm -rf "${data_dir}"
fi

printf 'managed_by=clusterguard-ha\nport=%s\n' "${port}" >"${managed_marker}"
chmod 0600 "${managed_marker}"
printf 'state=installing\n' >"${installing_marker}"
chmod 0600 "${installing_marker}"
rm -rf "${install_root}/software"
mkdir -p "${install_root}/software" "${data_dir}" "${log_dir}" "${run_dir}"
tar --no-same-owner --no-same-permissions -xf "${package_path}" -C "${install_root}/software" --strip-components=1
mysqld="${install_root}/software/bin/mysqld"
mysql="${install_root}/software/bin/mysql"
mysqladmin="${install_root}/software/bin/mysqladmin"
mysqldump="${install_root}/software/bin/mysqldump"
[[ -x "${mysqld}" && -x "${mysql}" && -x "${mysqladmin}" && -x "${mysqldump}" ]] || {
  echo "MySQL package does not contain required binaries" >&2
  exit 3
}

validate_runtime_linkage() {
  command -v ldd >/dev/null 2>&1 || {
    echo "ldd is required to validate MySQL package runtime dependencies" >&2
    exit 3
  }
  local executable missing=""
  for executable in "$@"; do
    missing+="$(ldd "${executable}" 2>&1 | awk '/not found/ {print $1}' || true)"$'\n'
  done
  missing="$(printf '%s\n' "${missing}" | sed '/^$/d' | sort -u | paste -sd, -)"
  if [[ -n "${missing}" ]]; then
    echo "MySQL package runtime dependencies are missing: ${missing}; provide matching signed RPMs through --dependencies" >&2
    exit 3
  fi
}

validate_runtime_linkage "${mysqld}" "${mysql}" "${mysqladmin}" "${mysqldump}"
mysql_version="$(${mysqld} --version | awk '{for(i=1;i<=NF;i++) if ($i ~ /^[0-9]+\.[0-9]+\./) {print $i; exit}}')"
release_family="$(awk -F. '{print $1"."$2}' <<<"${mysql_version}")"
release_major="$(awk -F. '{print $1}' <<<"${mysql_version}")"
if [[ "${server_id}" == "0" ]]; then
  server_id="$(( ($(printf '%s:%s' "$(hostname)" "${port}" | cksum | awk '{print $1}') % 4294967294) + 1 ))"
fi

detect_total_memory_mb() {
  local detected
  detected="$(awk '/^MemTotal:/ {printf "%d\n", $2 / 1024; found=1; exit} END {if (!found) print 0}' /proc/meminfo 2>/dev/null || true)"
  [[ "${detected}" =~ ^[0-9]+$ ]] || detected=0
  printf '%s\n' "${detected}"
}

calculate_memory_tuning() {
  local total_memory_mb="${1:-0}" seventy_percent_mb eighty_percent_mb rounded_up_mb max_safe_multiple_mb
  [[ "${total_memory_mb}" =~ ^[0-9]+$ && "${total_memory_mb}" -gt 0 ]] || total_memory_mb=2048
  seventy_percent_mb=$(((total_memory_mb * 70 + 99) / 100))
  eighty_percent_mb=$((total_memory_mb * 80 / 100))
  rounded_up_mb=$((((seventy_percent_mb + 4095) / 4096) * 4096))
  max_safe_multiple_mb=$(((eighty_percent_mb / 4096) * 4096))
  if (( max_safe_multiple_mb < 4096 )); then
    echo "MySQL data node requires at least 5 GiB of physical memory for a 4 GiB-aligned buffer pool that does not exceed 80%" >&2
    return 3
  fi
  innodb_buffer_pool_mb="${rounded_up_mb}"
  (( innodb_buffer_pool_mb > max_safe_multiple_mb )) && innodb_buffer_pool_mb="${max_safe_multiple_mb}"
  mysql_max_connections=1000
  if (( total_memory_mb < 1024 )); then
    mysql_table_open_cache=512
    mysql_thread_cache_size=16
    mysql_tmp_table_size_mb=16
    innodb_redo_log_capacity_mb=256
  elif (( total_memory_mb < 2048 )); then
    mysql_table_open_cache=1000
    mysql_thread_cache_size=32
    mysql_tmp_table_size_mb=32
    innodb_redo_log_capacity_mb=512
  elif (( total_memory_mb < 4096 )); then
    mysql_table_open_cache=2000
    mysql_thread_cache_size=64
    mysql_tmp_table_size_mb=64
    innodb_redo_log_capacity_mb=1024
  elif (( total_memory_mb < 8192 )); then
    mysql_table_open_cache=4000
    mysql_thread_cache_size=100
    mysql_tmp_table_size_mb=64
    innodb_redo_log_capacity_mb=2048
  elif (( total_memory_mb < 16384 )); then
    mysql_table_open_cache=6000
    mysql_thread_cache_size=150
    mysql_tmp_table_size_mb=128
    innodb_redo_log_capacity_mb=4096
  else
    mysql_table_open_cache=8000
    mysql_thread_cache_size=200
    mysql_tmp_table_size_mb=128
    innodb_redo_log_capacity_mb=8192
  fi
  mysql_table_definition_cache=$((mysql_table_open_cache / 2))
  printf 'memory_tuning total_mb=%s buffer_pool_mb=%s max_connections=%s table_open_cache=%s tmp_table_mb=%s redo_mb=%s\n' \
    "${total_memory_mb}" "${innodb_buffer_pool_mb}" "${mysql_max_connections}" \
    "${mysql_table_open_cache}" "${mysql_tmp_table_size_mb}" "${innodb_redo_log_capacity_mb}"
}

total_memory_mb="$(detect_total_memory_mb)"
calculate_memory_tuning "${total_memory_mb}"

authentication_options="default_authentication_plugin=mysql_native_password"
managed_authentication_clause="IDENTIFIED WITH mysql_native_password BY"
if [[ "${release_family}" == "8.4" ]]; then
  authentication_options="mysql_native_password=ON"
elif [[ "${release_major}" =~ ^[0-9]+$ ]] && (( release_major >= 9 )); then
  authentication_options=""
  managed_authentication_clause="IDENTIFIED BY"
fi

redo_log_options="innodb_redo_log_capacity=${innodb_redo_log_capacity_mb}M"
if [[ "${release_family}" == "5.7" ]]; then
  redo_log_options="innodb_log_file_size=$((innodb_redo_log_capacity_mb / 2))M
innodb_log_files_in_group=2"
fi

replica_update_option="log_replica_updates=ON"
semi_sync_options="plugin_load_add=semisync_source.so
plugin_load_add=semisync_replica.so
loose-rpl_semi_sync_source_enabled=ON
loose-rpl_semi_sync_replica_enabled=ON
loose-rpl_semi_sync_source_timeout=10000
loose-rpl_semi_sync_source_wait_for_replica_count=1
loose-rpl_semi_sync_source_wait_no_replica=ON
loose-rpl_semi_sync_source_wait_point=AFTER_SYNC"
if [[ "${release_family}" == "5.7" ]]; then
  replica_update_option="log_slave_updates=ON"
  semi_sync_options="plugin_load_add=semisync_master.so
plugin_load_add=semisync_slave.so
loose-rpl_semi_sync_master_enabled=ON
loose-rpl_semi_sync_slave_enabled=ON
loose-rpl_semi_sync_master_timeout=10000
loose-rpl_semi_sync_master_wait_for_slave_count=1
loose-rpl_semi_sync_master_wait_no_slave=ON
loose-rpl_semi_sync_master_wait_point=AFTER_SYNC"
fi
cat >"${config_file}" <<EOF
[mysqld]
basedir=${install_root}/software
datadir=${data_dir}
socket=${socket}
port=${port}
server_id=${server_id}
bind_address=0.0.0.0
log_bin=${log_dir}/mysql-bin
relay_log=${log_dir}/relay-bin
relay_log_recovery=ON
sync_binlog=1
innodb_flush_log_at_trx_commit=1
binlog_format=ROW
gtid_mode=ON
enforce_gtid_consistency=ON
${replica_update_option}
${semi_sync_options}
read_only=ON
super_read_only=ON
skip_name_resolve=ON
max_connect_errors=10000
max_connections=${mysql_max_connections}
table_open_cache=${mysql_table_open_cache}
table_definition_cache=${mysql_table_definition_cache}
thread_cache_size=${mysql_thread_cache_size}
tmp_table_size=${mysql_tmp_table_size_mb}M
max_heap_table_size=${mysql_tmp_table_size_mb}M
sort_buffer_size=2M
join_buffer_size=2M
read_buffer_size=1M
read_rnd_buffer_size=1M
max_allowed_packet=64M
open_files_limit=65535
innodb_buffer_pool_size=${innodb_buffer_pool_mb}M
innodb_buffer_pool_load_at_startup=ON
innodb_buffer_pool_dump_at_shutdown=ON
${redo_log_options}
${authentication_options}
pid_file=${run_dir}/mysqld.pid
log_error=${log_dir}/error.log
EOF
chmod 0640 "${config_file}"
chown root:mysql "${config_file}"
chown -R mysql:mysql "${install_parent}" "${log_dir}" "${run_dir}"
"${mysqld}" --defaults-file="${config_file}" --initialize-insecure --user=mysql

cat >"${service_file}" <<EOF
[Unit]
Description=ClusterGuard managed MySQL ${port}
After=network-online.target
Wants=network-online.target
[Service]
Type=notify
User=mysql
Group=mysql
RuntimeDirectory=clusterguard/mysql/${port}
RuntimeDirectoryMode=0750
ExecStart=${mysqld} --defaults-file=${config_file}
Restart=on-failure
RestartSec=5
LimitNOFILE=65535
[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now "${service}"

for _ in $(seq 1 60); do
  [[ -S "${socket}" ]] && "${mysql}" --no-defaults --protocol=socket --socket="${socket}" -uroot -e 'SELECT 1' >/dev/null 2>&1 && break
  sleep 1
done
"${mysql}" --no-defaults --protocol=socket --socket="${socket}" -uroot -e 'SELECT 1' >/dev/null
password_hex="$(printf '%s' "${root_password}" | od -An -tx1 | tr -d ' \n')"
write_managed_private_client

restore_read_only() {
  local statement='SET GLOBAL read_only=ON; SET GLOBAL super_read_only=ON;'
  "${mysql}" --no-defaults --protocol=socket --socket="${socket}" -uroot -e "${statement}" >/dev/null 2>&1 ||
    "${mysql}" --defaults-file="${client_file}" --protocol=socket --socket="${socket}" -e "${statement}" >/dev/null 2>&1
}

set +e
"${mysql}" --no-defaults --protocol=socket --socket="${socket}" -uroot <<SQL
SET GLOBAL read_only=ON;
SET GLOBAL super_read_only=OFF;
SET sql_log_bin=0;
SET @cg_password=CONVERT(0x${password_hex} USING utf8mb4);
SET @cg_statement=CONCAT("ALTER USER 'root'@'localhost' ${managed_authentication_clause} ", QUOTE(@cg_password));
PREPARE cg_alter FROM @cg_statement;
EXECUTE cg_alter;
DEALLOCATE PREPARE cg_alter;
SET @cg_create_tcp_statement=CONCAT("CREATE USER IF NOT EXISTS 'root'@'127.0.0.1' ${managed_authentication_clause} ", QUOTE(@cg_password));
PREPARE cg_create_tcp FROM @cg_create_tcp_statement;
EXECUTE cg_create_tcp;
DEALLOCATE PREPARE cg_create_tcp;
SET @cg_alter_tcp_statement=CONCAT("ALTER USER 'root'@'127.0.0.1' ${managed_authentication_clause} ", QUOTE(@cg_password));
PREPARE cg_alter_tcp FROM @cg_alter_tcp_statement;
EXECUTE cg_alter_tcp;
DEALLOCATE PREPARE cg_alter_tcp;
GRANT ALL PRIVILEGES ON *.* TO 'root'@'127.0.0.1' WITH GRANT OPTION;
SET GLOBAL read_only=ON;
SET GLOBAL super_read_only=ON;
SQL
bootstrap_status=$?
set -e
if [[ "${bootstrap_status}" -ne 0 ]]; then
  restore_read_only || true
	echo "MySQL credential bootstrap failed; replica read-only protection was restored" >&2
	exit 3
fi
reconcile_control_accounts "${mysql}"
install_local_client_defaults
"${mysql}" --defaults-file="${client_file}" --protocol=tcp --host=127.0.0.1 --port="${port}" -e 'SELECT 1' >/dev/null
"${mysql}" --defaults-file="${client_file}" --protocol=socket --socket="${socket}" <<'SQL'
SET GLOBAL super_read_only=ON;
SET GLOBAL read_only=ON;
SQL
rm -f "${installing_marker}"
