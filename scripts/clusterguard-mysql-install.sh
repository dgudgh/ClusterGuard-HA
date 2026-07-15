#!/usr/bin/env bash
set -euo pipefail

command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }
payload="$(mktemp /tmp/clusterguard-install.XXXXXX)"
chmod 0600 "${payload}"
trap 'rm -f "${payload}"' EXIT
cat >"${payload}"
jq -e '.target.node_name and .target.mysql_port and .secrets.mysql_root_password' "${payload}" >/dev/null

port="$(jq -r '.target.mysql_port' "${payload}")"
server_id="$(jq -r '.target.server_id // 0' "${payload}")"
package_path="$(jq -r '.target.package_path // ""' "${payload}")"
rebuild="$(jq -r '.target.rebuild // false' "${payload}")"
root_password="$(jq -r '.secrets.mysql_root_password' "${payload}")"
[[ "${port}" =~ ^[0-9]+$ && "${port}" -ge 1 && "${port}" -le 65535 ]] || { echo "invalid MySQL port" >&2; exit 2; }

install_root="/opt/clusterguard/mysql/${port}"
data_dir="/var/lib/clusterguard/mysql/${port}/data"
log_dir="/var/log/clusterguard/mysql/${port}"
run_dir="/run/clusterguard/mysql/${port}"
config_dir="/etc/clusterguard/mysql"
config_file="${config_dir}/${port}.cnf"
client_file="${config_dir}/${port}-client.cnf"
socket="${run_dir}/mysql.sock"
service="clusterguard-mysql-${port}.service"
managed_mysql="${install_root}/software/bin/mysql"
mysql_client=""
for candidate in "${managed_mysql}" "${CG_MYSQL_CLIENT:-}" "$(command -v mysql 2>/dev/null || true)"; do
  if [[ -n "${candidate}" && -x "${candidate}" ]]; then
    mysql_client="${candidate}"
    break
  fi
done

mkdir -p "${config_dir}" "${log_dir}" "${run_dir}" "$(dirname "${data_dir}")" "${install_root}"
chmod 0750 "${config_dir}" "${log_dir}" "$(dirname "${data_dir}")"
if ! getent group mysql >/dev/null 2>&1; then groupadd --system mysql; fi
if ! id mysql >/dev/null 2>&1; then useradd --system --gid mysql --home-dir /nonexistent --shell /sbin/nologin mysql; fi

if [[ -n "${mysql_client}" && -f "${client_file}" ]] && "${mysql_client}" --defaults-extra-file="${client_file}" --protocol=tcp --host=127.0.0.1 --port="${port}" --batch --skip-column-names -e 'SELECT @@server_uuid' >/dev/null 2>&1; then
  "${mysql_client}" --defaults-extra-file="${client_file}" --protocol=tcp --host=127.0.0.1 --port="${port}" <<'SQL'
SET GLOBAL super_read_only=ON;
SET GLOBAL read_only=ON;
SQL
  printf 'existing MySQL instance accepted for synchronization\n'
  exit 0
fi

[[ -n "${package_path}" && -f "${package_path}" ]] || { echo "a local MySQL binary package is required" >&2; exit 3; }
if [[ -d "${data_dir}/mysql" ]]; then
  [[ "${rebuild}" == "true" ]] || { echo "existing unmanaged MySQL data directory blocks installation" >&2; exit 3; }
  [[ -f "/etc/systemd/system/${service}" ]] || { echo "refusing to rebuild an unmanaged MySQL instance" >&2; exit 3; }
  systemctl stop "${service}" || true
  rm -rf "${data_dir}"
fi

rm -rf "${install_root}/software"
mkdir -p "${install_root}/software" "${data_dir}"
tar -xf "${package_path}" -C "${install_root}/software" --strip-components=1
mysqld="${install_root}/software/bin/mysqld"
mysql="${install_root}/software/bin/mysql"
[[ -x "${mysqld}" && -x "${mysql}" ]] || { echo "MySQL package does not contain required binaries" >&2; exit 3; }
mysql_version="$(${mysqld} --version | awk '{for(i=1;i<=NF;i++) if ($i ~ /^[0-9]+\.[0-9]+\./) {print $i; exit}}')"
release_family="$(awk -F. '{print $1"."$2}' <<<"${mysql_version}")"
if [[ "${server_id}" == "0" ]]; then
  server_id="$(( ($(printf '%s:%s' "$(hostname)" "${port}" | cksum | awk '{print $1}') % 4294967294) + 1 ))"
fi

replica_update_option="log_replica_updates=ON"
if [[ "${release_family}" == "5.7" ]]; then
  replica_update_option="log_slave_updates=ON"
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
binlog_format=ROW
gtid_mode=ON
enforce_gtid_consistency=ON
${replica_update_option}
read_only=ON
super_read_only=ON
skip_name_resolve=ON
max_connect_errors=10000
pid_file=${run_dir}/mysqld.pid
log_error=${log_dir}/error.log
EOF
chmod 0640 "${config_file}"
chown -R mysql:mysql "${data_dir}" "${log_dir}" "${run_dir}"
"${mysqld}" --defaults-file="${config_file}" --initialize-insecure --user=mysql

cat >"/etc/systemd/system/${service}" <<EOF
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
"${mysql}" --no-defaults --protocol=socket --socket="${socket}" -uroot <<SQL
SET sql_log_bin=0;
SET @cg_password=CONVERT(0x${password_hex} USING utf8mb4);
SET @cg_statement=CONCAT("ALTER USER 'root'@'localhost' IDENTIFIED BY ", QUOTE(@cg_password));
PREPARE cg_alter FROM @cg_statement;
EXECUTE cg_alter;
DEALLOCATE PREPARE cg_alter;
SQL

umask 077
cat >"${client_file}" <<EOF
[client]
user=root
password=${root_password}
EOF
chmod 0600 "${client_file}"
"${mysql}" --defaults-extra-file="${client_file}" --protocol=socket --socket="${socket}" <<'SQL'
SET GLOBAL super_read_only=ON;
SET GLOBAL read_only=ON;
SQL
