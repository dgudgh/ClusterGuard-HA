#!/usr/bin/env bash
set -euo pipefail

command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }
payload="$(mktemp /tmp/clusterguard-install.XXXXXX)"
chmod 0600 "${payload}"
production_template=""
initialize_config=""
trap 'rm -f "${payload}"; [[ -z "${production_template}" ]] || rm -f "${production_template}"; [[ -z "${initialize_config}" ]] || rm -f "${initialize_config}"' EXIT
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

source "$(dirname "$0")/clusterguard-mysql-layout.sh"
legacy_config_dir="/etc/clusterguard/mysql"
layout_record="${legacy_config_dir}/${port}-layout.json"
saved_data_root=""; saved_software_dir=""
if [[ -f "${layout_record}" ]]; then
  saved_data_root="$(jq -er '.data_root' "${layout_record}")"
  saved_software_dir="$(jq -er '.software_dir' "${layout_record}")"
fi
data_root="$(jq -r --arg saved "${saved_data_root}" '.target.data_root // (if $saved != "" then $saved else "/data" end)' "${payload}")"
software_dir="$(jq -r --arg saved "${saved_software_dir}" '.target.software_dir // $saved' "${payload}")"
cg_mysql_layout "${data_root}" "${port}" "${software_dir}"
data_root="${data_root%/}"
install_parent="${cg_mysql_instance}"
install_root="${install_parent}"
software_dir="${cg_mysql_software}"
if [[ -n "${saved_data_root}" && ( "${data_root}" != "${saved_data_root}" || "${software_dir}" != "${saved_software_dir}" ) ]]; then
  echo 'existing MySQL paths cannot be changed by reinstall; an offline migration is required' >&2; exit 3
fi
state_root="${data_root}"
log_root="${install_parent}"
config_root="${install_parent}"
data_dir="${cg_mysql_data}"
log_dir="${cg_mysql_log}"
run_dir="${cg_mysql_run}"
config_dir="${cg_mysql_conf}"
config_file="${config_dir}/my.cnf"
client_file="${config_dir}/client.cnf"
system_client_file="${config_dir}/default-client.cnf"
socket="${run_dir}/mysql.sock"
service="clusterguard-mysql-${port}.service"
service_file="/etc/systemd/system/${service}"
managed_marker="${install_parent}/.clusterguard-managed"
installing_marker="${install_parent}/.installing"
installation_state="${config_dir}/installation-state.json"
managed_mysql="${software_dir}/bin/mysql"
managed_mysqld="${software_dir}/bin/mysqld"
managed_mysqladmin="${software_dir}/bin/mysqladmin"
managed_mysqldump="${software_dir}/bin/mysqldump"
if [[ "${server_id}" == 0 ]]; then
  server_id="$(cg_mysql_server_id "$(jq -r '.target.ip_address // .target.hostname' "${payload}")" "${port}")"
fi
[[ "${server_id}" =~ ^[1-9][0-9]*$ ]] && (( server_id <= 4294967295 )) || { echo 'invalid MySQL server_id' >&2; exit 2; }

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

# Reject links before mkdir/chmod or any later instance preparation follows them.
for mysql_directory in "${data_root}" "${install_parent}" "${data_dir}" "${config_dir}" "${log_dir}" "${run_dir}" "${software_dir}"; do
  mysql_component="${mysql_directory}"
  while [[ "${mysql_component}" != / ]]; do
    [[ ! -L "${mysql_component}" ]] || { echo "refusing a symlink MySQL installation path: ${mysql_component}" >&2; exit 3; }
    mysql_component="$(dirname "${mysql_component}")"
  done
done
command -v flock >/dev/null 2>&1 || { echo 'flock is required to serialize MySQL installation attempts' >&2; exit 3; }
mkdir -p "${data_root}/mysql"
[[ -O "${data_root}/mysql" ]] || { echo 'MySQL instance parent must be owned by the installation user' >&2; exit 3; }
installation_lock="${data_root}/mysql/.clusterguard-install-${port}.lock"
if [[ -e "${installation_lock}" || -L "${installation_lock}" ]]; then
  [[ -f "${installation_lock}" && ! -L "${installation_lock}" && -O "${installation_lock}" ]] || {
    echo 'refusing an unrecognized MySQL installation lock file' >&2; exit 3;
  }
else
  (umask 077; set -o noclobber; : >"${installation_lock}") || exit 3
fi
exec 9>>"${installation_lock}"
flock --nonblock 9 || { echo "another MySQL installation attempt owns port ${port}" >&2; exit 3; }

mkdir -p "${config_dir}" "${log_dir}" "${run_dir}" "${data_root}" "${install_parent}"
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
  install -d -m 0750 "${legacy_config_dir}"
  ln -sfn "${client_file}" "${legacy_config_dir}/${port}-client.cnf"
  ln -sfn "${config_file}" "${legacy_config_dir}/${port}.cnf"
  if [[ "${port}" == 3306 ]]; then ln -sfn "${system_client_file}" "${legacy_config_dir}/default-client.cnf"; fi
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
    # The installer owns this marker before extraction, including interrupted
    # extractions. Vendor archive entries must never replace or alias its path.
    for (i=2; i<=count; i++) if (parts[i] == ".") bad=1
    if (count >= 2 && parts[2] == ".clusterguard-managed") bad=1
  }
  END {exit !(root != "" && !bad)}
'; then
  echo "MySQL package must contain one safe top-level directory" >&2
  exit 3
fi
quiesce_managed_mysql_service() {
  local load_state fragment dropins active_state main_pid
  load_state="$(systemctl show --property=LoadState --value "${service}")" || return 3
  if [[ ! -e "${service_file}" && ! -L "${service_file}" && "${load_state}" == "not-found" ]]; then
    return 0
  fi
  # Only the exact unit generated for this port may be retired. A name alone is
  # not ownership evidence, and drop-ins can redirect an otherwise known unit.
  [[ -f "${service_file}" && ! -L "${service_file}" ]] &&
    grep -Fqx "Description=ClusterGuard managed MySQL ${port}" "${service_file}" &&
    grep -Fqx "ExecStart=${managed_mysqld} --defaults-file=${config_file}" "${service_file}" || {
      echo "refusing to stop an unrecognized MySQL service: ${service}" >&2
      return 3
    }
  fragment="$(systemctl show --property=FragmentPath --value "${service}")" || return 3
  dropins="$(systemctl show --property=DropInPaths --value "${service}")" || return 3
  [[ "${fragment}" == "${service_file}" && -z "${dropins}" ]] || {
    echo "refusing to stop an overridden MySQL service: ${service}" >&2
    return 3
  }
  if [[ -e "${config_file}" || -L "${config_file}" ]]; then
    [[ -f "${config_file}" && ! -L "${config_file}" ]] &&
      grep -Fqx "datadir=${data_dir}" "${config_file}" || {
        echo "existing MySQL service belongs to a different data directory" >&2
        return 3
      }
  fi
  # disable --now cancels Restart=on-failure before directories or binaries are
  # replaced. Failure is fatal; leaving a live daemon and continuing is unsafe.
  systemctl disable --now "${service}" || {
    echo "cannot stop and disable previous MySQL service: ${service}" >&2
    return 3
  }
  active_state="$(systemctl show --property=ActiveState --value "${service}")" || return 3
  main_pid="$(systemctl show --property=MainPID --value "${service}")" || return 3
  [[ "${active_state}" == "inactive" && "${main_pid}" == "0" ]] || {
    echo "previous MySQL service did not stop: ${service}" >&2
    return 3
  }
  echo "previous managed MySQL service stopped; reinstall may continue: ${service}"
}

cg_mysql_sha256_stream() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum | awk '{print $1}';
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 | awk '{print $1}';
  else echo 'a SHA-256 implementation is required for MySQL installation state' >&2; return 3; fi
}

cg_mysql_sha256() { cg_mysql_sha256_stream <"$1"; }

cg_mysql_data_identity() {
  [[ -d "$1" && ! -L "$1" ]] || return 3
  stat -c '%d:%i' "$1" 2>/dev/null || stat -f '%d:%i' "$1"
}

cg_mysql_root_file() {
  local owner mode
  [[ -f "$1" && ! -L "$1" ]] || return 3
  owner="$(stat -c '%u' "$1" 2>/dev/null || stat -f '%u' "$1")" || return 3
  mode="$(stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1")" || return 3
  [[ "${owner}" == 0 && "${mode}" =~ ^[0-7]{3,4}$ ]] && (( (8#${mode} & 0022) == 0 ))
}

cg_mysql_data_sha256() {
  local entry relative links started="${SECONDS}"
  [[ -d "${data_dir}" && ! -L "${data_dir}" ]] || return 3
  if ! find "${data_dir}" -print0 | LC_ALL=C sort -z |
    while IFS= read -r -d '' entry; do
      relative="${entry#"${data_dir}"}"
      if [[ -L "${entry}" ]]; then return 3
      elif [[ -d "${entry}" ]]; then printf 'directory:%s\0' "${relative}"
      elif [[ -f "${entry}" ]]; then
        links="$(stat -c '%h' "${entry}" 2>/dev/null || stat -f '%l' "${entry}")" || return 3
        [[ "${links}" == 1 ]] || return 3
        printf 'file:%s\0' "${relative}"
        cg_mysql_sha256 "${entry}" || return 3
      else return 3; fi
    done | cg_mysql_sha256_stream; then return 3; fi
  printf 'MySQL failed-initialization data SHA-256 check took %s seconds\n' "$((SECONDS - started))" >&2
}

cg_mysql_template_sha256() {
  local template_dir
  if jq -e '.target.mysql_cnf | type == "string" and length > 0' "${payload}" >/dev/null; then
    jq -jr '.target.mysql_cnf' "${payload}" | cg_mysql_sha256_stream
  else
    template_dir=/usr/share/clusterguard/mysql
    [[ -d "${template_dir}" ]] || template_dir="$(dirname "$0")/../configs/mysql"
    if [[ -f "${template_dir}/my-slave.cnf" && ! -L "${template_dir}/my-slave.cnf" ]]; then
      cg_mysql_sha256 "${template_dir}/my-slave.cnf"
    else
      printf '' | cg_mysql_sha256_stream
    fi
  fi
}

cg_mysql_assert_quiet() {
  local processes listeners
  command -v ps >/dev/null 2>&1 && command -v ss >/dev/null 2>&1 || {
    echo 'ps and ss are required to prove that failed MySQL initialization is offline' >&2; return 3;
  }
  processes="$(ps -eo comm=)" || return 3
  if printf '%s\n' "${processes}" | awk '/(^|\/)mysqld(-debug)?$/ {found=1} END {exit !found}'; then
    echo 'a MySQL process is still present (any port); automatic failed-initialization isolation is unavailable and data was preserved' >&2; return 3
  fi
  listeners="$(ss -H -ltn "sport = :${port}")" || return 3
  [[ -z "${listeners}" ]] || { echo "port ${port} is still listening; failed initialization data was preserved" >&2; return 3; }
}

cg_mysql_write_install_state() {
  local phase="$1" native_exit="${2:-0}" temporary config_sha="" error_sha="" data_sha="" software_identity
  case "${phase}" in preparing|initializing|initialize-failed|initialized|starting|complete) ;; *) return 3 ;; esac
  [[ ! -e "${installation_state}" && ! -L "${installation_state}" ]] || cg_mysql_root_file "${installation_state}" || {
    echo 'refusing an unrecognized MySQL installation state file' >&2; return 3;
  }
  if [[ -e "${config_file}" || -L "${config_file}" ]]; then
    cg_mysql_root_file "${config_file}" || return 3
    config_sha="$(cg_mysql_sha256 "${config_file}")" || return 3
  fi
  if [[ -f "${log_dir}/error.log" && ! -L "${log_dir}/error.log" ]]; then
    error_sha="$(cg_mysql_sha256 "${log_dir}/error.log")" || return 3
  fi
  if [[ "${phase}" == initialize-failed ]]; then
    cg_mysql_assert_quiet || return 3
    data_sha="$(cg_mysql_data_sha256)" || return 3
  fi
  software_identity="$(cg_mysql_data_identity "${software_dir}")" || return 3
  temporary="$(mktemp "${config_dir}/.installation-state.XXXXXXXX")" || return 3
  if jq -n --arg phase "${phase}" --argjson port "${port}" --arg node_id "${installation_node_id}" \
    --arg instance "${install_parent}" --arg software_dir "${software_dir}" \
    --arg config_sha "${config_sha}" --arg package_sha "${installation_package_sha256}" \
    --arg template_sha "${installation_template_sha256}" --arg data_identity "${installation_data_identity}" --arg software_identity "${software_identity}" \
    --argjson fresh_empty "${installation_fresh_empty}" --argjson native_exit "${native_exit}" --arg error_sha "${error_sha}" --arg data_sha "${data_sha}" \
    '{schema_version:1,phase:$phase,port:$port,node_id:$node_id,instance:$instance,software_dir:$software_dir,software_identity:$software_identity,config_sha256:$config_sha,package_sha256:$package_sha,template_sha256:$template_sha,data_identity:$data_identity,data_sha256:$data_sha,fresh_empty:$fresh_empty,native_exit:$native_exit,error_log_sha256:$error_sha}' >"${temporary}" &&
    chmod 0600 "${temporary}" && chown root:root "${temporary}" && mv -f "${temporary}" "${installation_state}"; then
    return 0
  fi
  rm -f "${temporary}"
  echo 'could not persist MySQL installation state; existing data must be preserved' >&2
  return 3
}

cg_mysql_retryable_preparation_matches() {
  local data_identity software_identity contents
  cg_mysql_root_file "${installation_state}" && cg_mysql_root_file "${managed_marker}" &&
    cg_mysql_root_file "${installing_marker}" && cg_mysql_root_file "${software_dir}/.clusterguard-managed" || return 3
  grep -Fqx 'managed_by=clusterguard-ha' "${managed_marker}" && grep -Fqx "port=${port}" "${managed_marker}" &&
    grep -Fqx 'state=installing' "${installing_marker}" &&
    grep -Fqx 'managed_by=clusterguard-ha' "${software_dir}/.clusterguard-managed" &&
    grep -Fqx "port=${port}" "${software_dir}/.clusterguard-managed" &&
    grep -Fqx "instance=${install_parent}" "${software_dir}/.clusterguard-managed" || return 3
  jq -e '.schema_version == 1 and .phase == "preparing" and .fresh_empty == true' "${installation_state}" >/dev/null || return 3
  data_identity="$(cg_mysql_data_identity "${data_dir}")" &&
    software_identity="$(cg_mysql_data_identity "${software_dir}")" || return 3
  contents="$(find "${data_dir}" -mindepth 1 -print -quit)" || return 3
  [[ -z "${contents}" ]] || return 3
  if [[ -e "${config_file}" || -L "${config_file}" ]]; then cg_mysql_root_file "${config_file}" || return 3; fi
  if [[ -e "${log_dir}/error.log" || -L "${log_dir}/error.log" ]]; then
    [[ -f "${log_dir}/error.log" && ! -L "${log_dir}/error.log" ]] || return 3
  fi
  # Before "initializing" is durably recorded, extraction, linkage checks and
  # strict validation may write configuration or logs, but cannot initialize
  # data. Preserve those files as-is; only a still-empty, identical data inode
  # and the bound software directory authorize restarting this preparation.
  jq -e --argjson port "${port}" --arg node_id "${installation_node_id}" --arg instance "${install_parent}" \
    --arg software_dir "${software_dir}" --arg software_identity "${software_identity}" \
    --arg package_sha "${installation_package_sha256}" --arg template_sha "${installation_template_sha256}" --arg data_identity "${data_identity}" '
      (keys | sort) == (["schema_version","phase","port","node_id","instance","software_dir","software_identity","config_sha256","package_sha256","template_sha256","data_identity","data_sha256","fresh_empty","native_exit","error_log_sha256"] | sort) and
      .schema_version == 1 and .phase == "preparing" and .port == $port and
      ($node_id | length) > 0 and .node_id == $node_id and .instance == $instance and .software_dir == $software_dir and
      .software_identity == $software_identity and .data_identity == $data_identity and .fresh_empty == true and .native_exit == 0 and .data_sha256 == "" and
      (.config_sha256 | type == "string" and test("^([0-9a-f]{64})?$")) and
      (.error_log_sha256 | type == "string" and test("^([0-9a-f]{64})?$")) and
      .package_sha256 == $package_sha and ($package_sha | test("^[0-9a-f]{64}$")) and
      .template_sha256 == $template_sha and ($template_sha | test("^[0-9a-f]{64}$"))
    ' "${installation_state}" >/dev/null || return 3
  cg_mysql_assert_quiet
}

cg_mysql_failed_initialization_matches() {
  local config_sha error_sha data_identity data_sha software_identity
  cg_mysql_root_file "${installation_state}" && cg_mysql_root_file "${managed_marker}" &&
    cg_mysql_root_file "${config_file}" && cg_mysql_root_file "${installing_marker}" || return 3
  grep -Fqx 'managed_by=clusterguard-ha' "${managed_marker}" && grep -Fqx "port=${port}" "${managed_marker}" &&
    grep -Fqx 'state=installing' "${installing_marker}" && grep -Fqx "datadir=${data_dir}" "${config_file}" || return 3
  # Do not hash a complete/unknown database. The inexpensive phase check must
  # precede all recursive inspection; ordinary existing instances may be large.
  jq -e '.schema_version == 1 and .phase == "initialize-failed" and .fresh_empty == true' "${installation_state}" >/dev/null || return 3
  cg_mysql_assert_quiet || return 3
  [[ -f "${log_dir}/error.log" && ! -L "${log_dir}/error.log" ]] || return 3
  config_sha="$(cg_mysql_sha256 "${config_file}")" && error_sha="$(cg_mysql_sha256 "${log_dir}/error.log")" &&
    data_identity="$(cg_mysql_data_identity "${data_dir}")" && software_identity="$(cg_mysql_data_identity "${software_dir}")" || return 3
  jq -e --argjson port "${port}" --arg node_id "${installation_node_id}" --arg instance "${install_parent}" \
    --arg software_dir "${software_dir}" --arg software_identity "${software_identity}" --arg config_sha "${config_sha}" --arg package_sha "${installation_package_sha256}" \
    --arg template_sha "${installation_template_sha256}" --arg data_identity "${data_identity}" --arg error_sha "${error_sha}" '
      (keys | sort) == (["schema_version","phase","port","node_id","instance","software_dir","software_identity","config_sha256","package_sha256","template_sha256","data_identity","data_sha256","fresh_empty","native_exit","error_log_sha256"] | sort) and
      .schema_version == 1 and .phase == "initialize-failed" and .port == $port and
      ($node_id | length) > 0 and .node_id == $node_id and .instance == $instance and .software_dir == $software_dir and
      .fresh_empty == true and (.native_exit | type) == "number" and .native_exit == (.native_exit | floor) and .native_exit >= 1 and .native_exit <= 255 and
      .config_sha256 == $config_sha and ($config_sha | test("^[0-9a-f]{64}$")) and
      .package_sha256 == $package_sha and ($package_sha | test("^[0-9a-f]{64}$")) and
      .template_sha256 == $template_sha and ($template_sha | test("^[0-9a-f]{64}$")) and
      .software_identity == $software_identity and .data_identity == $data_identity and (.data_sha256 | type == "string" and test("^[0-9a-f]{64}$")) and
      .error_log_sha256 == $error_sha and ($error_sha | test("^[0-9a-f]{64}$"))
    ' "${installation_state}" >/dev/null || return 3
  data_sha="$(cg_mysql_data_sha256)" || return 3
  [[ "${data_sha}" == "$(jq -r '.data_sha256' "${installation_state}")" ]]
}

cg_mysql_assert_instance_unmounted() {
  local mountinfo_file="${1:-/proc/self/mountinfo}" mountinfo
  [[ -r "${mountinfo_file}" ]] && mountinfo="$(cat "${mountinfo_file}")" || {
    echo 'cannot inspect MySQL instance mount boundaries; automatic isolation is unavailable' >&2; return 3;
  }
  # Renaming an outer directory also relocates its child mounts. Recreating
  # data/ at the old path would then silently select the underlying disk. Device
  # numbers alone cannot detect bind mounts, so inspect the kernel mount table.
  if ! awk -v instance="${install_parent}" '
    NF < 10 || $1 !~ /^[0-9]+$/ || $2 !~ /^[0-9]+$/ || $5 !~ /^\// || index($0," - ") == 0 {bad=1}
    $5 == instance || index($5,instance "/") == 1 {mounted=1}
    END {exit (NR == 0 || bad || mounted)}
  ' <<<"${mountinfo}"; then
    echo 'MySQL instance contains a mount or its mount table is invalid; preserve it for operator review' >&2
    return 3
  fi
}

cg_mysql_quarantine_failed_initialization() {
  local parent reservation instance_device parent_device
  parent="$(dirname "${install_parent}")"
  cg_mysql_assert_instance_unmounted || return 3
  instance_device="$(cg_mysql_data_identity "${install_parent}" | cut -d: -f1)" &&
    parent_device="$(cg_mysql_data_identity "${parent}" | cut -d: -f1)" || return 3
  [[ -n "${instance_device}" && "${instance_device}" == "${parent_device}" ]] || {
    echo 'failed MySQL instance is a separate mount; automatic isolation is unavailable' >&2; return 3;
  }
  # The directory reservation prevents overwriting an older backup. Its sibling
  # source and destination are on the same filesystem, so mv uses atomic rename.
  reservation="$(mktemp -d "${parent}/.$(basename "${install_parent}").failed-initialize.XXXXXXXX")" || return 3
  chmod 0700 "${reservation}"
  [[ ! -e "${reservation}/instance" && ! -L "${reservation}/instance" ]] &&
    { cg_mysql_retryable_preparation_matches || cg_mysql_failed_initialization_matches; } &&
    cg_mysql_assert_quiet && cg_mysql_assert_instance_unmounted || return 3
  mv "${install_parent}" "${reservation}/instance" || {
    echo 'failed MySQL initialization could not be isolated; original data was preserved' >&2; return 3;
  }
  printf 'incomplete MySQL installation preserved at: %s/instance\n' "${reservation}"
  mkdir -p "${config_dir}" "${log_dir}" "${run_dir}" "${install_parent}"
  chmod 0750 "${config_dir}" "${log_dir}" "${install_parent}"
}

installation_node_id="$(jq -r '.target.node_id // ""' "${payload}")"
installation_package_sha256="$(cg_mysql_sha256 "${package_path}")"
installation_template_sha256="$(cg_mysql_template_sha256)"
installation_data_identity=""
installation_fresh_empty=false
for installation_file in "${managed_marker}" "${installing_marker}" "${config_file}" "${installation_state}"; do
  if [[ -e "${installation_file}" || -L "${installation_file}" ]]; then
    cg_mysql_root_file "${installation_file}" || { echo "refusing an unrecognized MySQL installation file: ${installation_file}" >&2; exit 3; }
  fi
done
managed_evidence=false
if cg_mysql_root_file "${managed_marker}" && grep -Fqx 'managed_by=clusterguard-ha' "${managed_marker}" && grep -Fqx "port=${port}" "${managed_marker}"; then
  managed_evidence=true
elif [[ -f "${service_file}" && ! -L "${service_file}" ]] ||
  { cg_mysql_root_file "${config_file}" && [[ -x "${managed_mysqld}" ]] && grep -Fqx "datadir=${data_dir}" "${config_file}"; }; then
  # Upgrade compatibility for an installation interrupted before managed markers existed.
  managed_evidence=true
fi
quarantine_failed_initialization=false
if [[ -f "${installation_state}" ]] ||
  { [[ -d "${data_dir}" ]] && [[ -n "$(find "${data_dir}" -mindepth 1 -print -quit 2>/dev/null)" ]]; }; then
  if [[ "${rebuild}" == true ]]; then
    [[ "${managed_evidence}" == true ]] || { echo 'refusing to rebuild an unmanaged MySQL instance' >&2; exit 3; }
  elif cg_mysql_retryable_preparation_matches || cg_mysql_failed_initialization_matches; then
    cg_mysql_assert_quiet
    quarantine_failed_initialization=true
  else
    printf 'existing MySQL data was preserved: initialization is complete or its state cannot be proved; log: %s/error.log; state: %s\n' "${log_dir}" "${installation_state}" >&2
    echo 'do not delete data or deployment state; preserve and review the failed instance before an explicit recovery/rebuild' >&2
    exit 3
  fi
fi

# Validate data protection before stopping anything. Live instances have already
# returned through the synchronization path above; complete data still requires
# an explicit rebuild. This also covers empty/partial interrupted installations.
if [[ -d "${software_dir}" && -n "$(find "${software_dir}" -mindepth 1 -print -quit)" ]]; then
  cg_mysql_root_file "${software_dir}/.clusterguard-managed" &&
    grep -Fqx "port=${port}" "${software_dir}/.clusterguard-managed" &&
    grep -Fqx "instance=${install_parent}" "${software_dir}/.clusterguard-managed" || {
      echo 'refusing to replace an unmanaged or foreign MySQL software directory' >&2; exit 3
    }
fi
[[ ! -L "${software_dir}" ]] || { echo 'refusing a symlink MySQL software directory' >&2; exit 3; }
quiesce_managed_mysql_service
if [[ "${quarantine_failed_initialization}" == true ]]; then
  cg_mysql_quarantine_failed_initialization
elif [[ "${rebuild}" == true && -d "${data_dir}" ]] && [[ -n "$(find "${data_dir}" -mindepth 1 -print -quit 2>/dev/null)" ]]; then
  rm -rf "${data_dir}"
fi

printf 'managed_by=clusterguard-ha\nport=%s\n' "${port}" >"${managed_marker}"
chmod 0600 "${managed_marker}"
printf 'state=installing\n' >"${installing_marker}"
chmod 0600 "${installing_marker}"
rm -rf "${software_dir}"
mkdir -p "${software_dir}" "${data_dir}" "${log_dir}" "${run_dir}" "${install_parent}/binlog" "${install_parent}/relaylog" "${install_parent}/tmp"
[[ -z "$(find "${data_dir}" -mindepth 1 -print -quit)" ]] || { echo 'MySQL initialization requires an empty data directory' >&2; exit 3; }
installation_data_identity="$(cg_mysql_data_identity "${data_dir}")"
installation_fresh_empty=true
printf 'managed_by=clusterguard-ha\nport=%s\ninstance=%s\n' "${port}" "${install_parent}" >"${software_dir}/.clusterguard-managed"
chmod 0600 "${software_dir}/.clusterguard-managed"
cg_mysql_write_install_state preparing
tar --no-same-owner --no-same-permissions -xf "${package_path}" -C "${software_dir}" --strip-components=1
mysqld="${software_dir}/bin/mysqld"
mysql="${software_dir}/bin/mysql"
mysqladmin="${software_dir}/bin/mysqladmin"
mysqldump="${software_dir}/bin/mysqldump"
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

# Production MySQL 8.0 uses the approved cnf verbatim, not memory tuning.
use_production_cnf=false
if [[ "${release_family}" == 8.0 ]] || jq -e '.target.mysql_cnf | type == "string" and length > 0' "${payload}" >/dev/null; then
  use_production_cnf=true
fi
if [[ "${use_production_cnf}" != true ]]; then
  total_memory_mb="$(detect_total_memory_mb)"
  calculate_memory_tuning "${total_memory_mb}"
fi

authentication_options="default_authentication_plugin=mysql_native_password"
managed_authentication_clause="IDENTIFIED WITH mysql_native_password BY"
if [[ "${release_family}" == "8.4" ]]; then
  authentication_options="mysql_native_password=ON"
elif [[ "${release_major}" =~ ^[0-9]+$ ]] && (( release_major >= 9 )); then
  authentication_options=""
  managed_authentication_clause="IDENTIFIED BY"
fi

redo_log_options=""
if [[ "${use_production_cnf}" != true ]]; then
  redo_log_options="innodb_redo_log_capacity=${innodb_redo_log_capacity_mb}M"
  if [[ "${release_family}" == "5.7" ]]; then
    redo_log_options="innodb_log_file_size=$((innodb_redo_log_capacity_mb / 2))M
innodb_log_files_in_group=2"
  fi
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
if [[ "${use_production_cnf}" == true ]]; then
  production_template="$(mktemp /tmp/clusterguard-production-cnf.XXXXXX)"
  chmod 0600 "${production_template}"
  if jq -e '.target.mysql_cnf | type == "string" and length > 0' "${payload}" >/dev/null; then
    jq -jr '.target.mysql_cnf' "${payload}" >"${production_template}"
  else
    template_dir="/usr/share/clusterguard/mysql"
    [[ -d "${template_dir}" ]] || template_dir="$(dirname "$0")/../configs/mysql"
    cat "${template_dir}/my-slave.cnf" >"${production_template}"
  fi
  cg_mysql_render_cnf "${production_template}" "${config_file}" "${data_root}" "${software_dir}" "${port}" "${server_id}"
else
cat >"${config_file}" <<EOF
[mysqld]
basedir=${software_dir}
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
fi
chmod 0640 "${config_file}"
chown root:mysql "${config_file}"
chown root:root "${install_parent}" "${software_dir}"
chmod 0755 "${install_parent}" "${software_dir}"
chown -R mysql:mysql "${data_dir}" "${log_dir}" "${run_dir}" "${install_parent}/binlog" "${install_parent}/relaylog" "${install_parent}/tmp"
chown root:mysql "${config_dir}"

run_mysql_setup_step() {
  local phase="$1" output status secret
  shift
  if output="$("$@" 9>&- 2>&1)"; then
    status=0
  else
    status=$?
    printf 'MySQL %s failed (exit=%s); diagnostic log: %s\n' "${phase}" "${status}" "${log_dir}/error.log" >&2
    if [[ -f "${log_dir}/error.log" && ! -L "${log_dir}/error.log" ]]; then
      output+=$'\n'"$(tail -n 80 "${log_dir}/error.log" || true)"
    fi
  fi
  for secret in "${root_password}" "${discovery_password}" "${operation_password}" "${replication_password}"; do
    [[ -z "${secret}" ]] || output="${output//"${secret}"/<redacted>}"
  done
  if [[ -n "${output}" ]]; then printf '%s\n' "${output}" >&2; fi
  return "${status}"
}

if (( release_major >= 8 )); then run_mysql_setup_step validation "${mysqld}" --defaults-file="${config_file}" --validate-config; fi
# Initialization uses a disposable option file; the service keeps config_file.
initialize_config="$(mktemp /tmp/clusterguard-initialize-cnf.XXXXXX)"
chmod 0600 "${initialize_config}"
cg_mysql_render_initialize_cnf "${config_file}" "${initialize_config}"
[[ "$(cg_mysql_data_identity "${data_dir}")" == "${installation_data_identity}" &&
  -z "$(find "${data_dir}" -mindepth 1 -print -quit)" ]] || {
  echo 'MySQL data changed before initialization; original data was preserved' >&2; exit 3;
}
cg_mysql_write_install_state initializing
if run_mysql_setup_step initialization "${mysqld}" --defaults-file="${initialize_config}" --initialize-insecure --user=mysql; then
  cg_mysql_write_install_state initialized
else
  initialization_status=$?
  # An interrupted or unrecorded call remains "initializing" and is never a
  # license to recreate data. Only this observed nonzero native exit is failed.
  cg_mysql_write_install_state initialize-failed "${initialization_status}" || {
    echo 'initialization failed but failure proof could not be persisted; preserve the instance for review' >&2;
  }
  exit "${initialization_status}"
fi
rm -f "${initialize_config}"
initialize_config=""

# Keep the system registration readable before /data is mounted; the database
# configuration, software and runtime state themselves live under the site paths.
cat >"${service_file}" <<EOF
[Unit]
Description=ClusterGuard managed MySQL ${port}
After=network-online.target
Wants=network-online.target
RequiresMountsFor=${install_parent} ${software_dir}
[Service]
Type=notify
User=mysql
Group=mysql
# Create the runtime directory only after the instance filesystem is mounted.
ExecStartPre=+/usr/bin/install -d -m 0750 -o mysql -g mysql ${run_dir}
ExecStart=${mysqld} --defaults-file=${config_file}
Restart=on-failure
RestartSec=5
LimitNOFILE=65535
[Install]
WantedBy=multi-user.target
EOF
chmod 0644 "${service_file}"
systemctl daemon-reload
jq -n --arg data_root "${data_root}" --arg software_dir "${software_dir}" '{data_root:$data_root,software_dir:$software_dir}' >"${config_dir}/layout.json"
chmod 0600 "${config_dir}/layout.json"
install -d -m 0750 "${legacy_config_dir}"
ln -sfn "${config_dir}/layout.json" "${layout_record}"
cat >"${config_dir}/logrotate.conf" <<EOF
${log_dir}/*.log {
    daily
    rotate 14
    compress
    delaycompress
    missingok
    notifempty
    copytruncate
    su mysql mysql
}
EOF
chmod 0644 "${config_dir}/logrotate.conf"
install -d -m 0755 /etc/logrotate.d
ln -sfn "${config_dir}/logrotate.conf" "/etc/logrotate.d/clusterguard-mysql-${port}"
cg_mysql_write_install_state starting
systemctl enable --now "${service}" 9>&-

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
cg_mysql_write_install_state complete
rm -f "${installing_marker}"
