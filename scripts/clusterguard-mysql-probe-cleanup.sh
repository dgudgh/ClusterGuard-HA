#!/usr/bin/env bash
set -euo pipefail

mysql_binary="${CG_MYSQL_CLIENT:-$(command -v mysql 2>/dev/null || true)}"
host="${CG_MYSQL_HOST:-127.0.0.1}"
port="${CG_MYSQL_PORT:-3306}"
user="${CG_MYSQL_USER:-root}"
password="${CG_MYSQL_PASSWORD:-}"
defaults_file="${CG_MYSQL_DEFAULTS_FILE:-}"
canonical_probe_schema="${CG_MYSQL_PROBE_SCHEMA:-clusterguard_probe}"
keep_canonical="${CG_MYSQL_KEEP_PROBE_SCHEMA:-true}"

[[ -n "${mysql_binary}" && -x "${mysql_binary}" ]] || { echo "mysql client is required" >&2; exit 3; }
[[ "${port}" =~ ^[0-9]+$ && "${port}" -ge 1 && "${port}" -le 65535 ]] || { echo "invalid MySQL port" >&2; exit 2; }
[[ "${canonical_probe_schema}" =~ ^[A-Za-z0-9_]+$ ]] || { echo "invalid canonical probe schema" >&2; exit 2; }

legacy_probe_schemas=(
  cg_rc28_test
  clusterguard_ha_test
  clusterguard_matrix_probe
  clusterguard_validation
)

mysql_args=(--protocol=tcp --host="${host}" --port="${port}" --batch --skip-column-names)
if [[ -n "${defaults_file}" ]]; then
  [[ -f "${defaults_file}" ]] || { echo "defaults file does not exist" >&2; exit 2; }
  mysql_args=(--defaults-extra-file="${defaults_file}" "${mysql_args[@]}")
else
  [[ -n "${user}" ]] || { echo "MySQL user is required" >&2; exit 2; }
  mysql_args+=(--user="${user}")
fi

run_mysql() {
  if [[ -n "${password}" && -z "${defaults_file}" ]]; then
    MYSQL_PWD="${password}" "${mysql_binary}" "${mysql_args[@]}" "$@"
    return
  fi
  "${mysql_binary}" "${mysql_args[@]}" "$@"
}

sql_identifier() {
  local value="$1"
  value="${value//\`/\`\`}"
  printf '`%s`' "${value}"
}

for schema in "${legacy_probe_schemas[@]}"; do
  [[ "${schema}" =~ ^[A-Za-z0-9_]+$ ]] || exit 2
  run_mysql --execute "DROP DATABASE IF EXISTS $(sql_identifier "${schema}")"
  printf 'removed legacy probe schema: %s\n' "${schema}"
done

if [[ "${keep_canonical}" == "true" ]]; then
  run_mysql --execute "CREATE DATABASE IF NOT EXISTS $(sql_identifier "${canonical_probe_schema}")"
  run_mysql --execute "CREATE TABLE IF NOT EXISTS $(sql_identifier "${canonical_probe_schema}").probe_heartbeat (probe_key VARCHAR(64) PRIMARY KEY, checked_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP) ENGINE=InnoDB"
  printf 'canonical probe schema ready: %s\n' "${canonical_probe_schema}"
fi
