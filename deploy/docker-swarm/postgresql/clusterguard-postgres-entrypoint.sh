#!/usr/bin/env bash
set -Eeuo pipefail

slot="${CG_PG_SLOT:-}"
source_host="${CG_PG_SOURCE_HOST:-}"
source_port="${CG_PG_SOURCE_PORT:-55432}"
replication_user="${CG_PG_REPLICATION_USER:-cg_replication}"
application_name="${CG_PG_APPLICATION_NAME:-}"
primary_slot="${CG_PG_PRIMARY_SLOT:-}"
node_id="${CG_PG_NODE_ID:-}"
primary_node_id="${CG_PG_PRIMARY_NODE_ID:-}"
clusterguard_hostname="${CG_PG_HOSTNAME:-}"
data_directory="${PGDATA:-/var/lib/postgresql/data}"

[[ "${slot}" =~ ^0[123]$ ]] || { echo "CG_PG_SLOT must be 01, 02, or 03" >&2; exit 2; }
if [[ ! -s "${data_directory}/PG_VERSION" ]]; then
[[ "${source_host}" =~ ^[A-Za-z0-9.-]+$ ]] || { echo "CG_PG_SOURCE_HOST is invalid" >&2; exit 2; }
[[ "${source_port}" =~ ^[0-9]+$ ]] && ((source_port >= 1 && source_port <= 65535)) || {
  echo "CG_PG_SOURCE_PORT is invalid" >&2
  exit 2
}
[[ "${replication_user}" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || { echo "replication user is invalid" >&2; exit 2; }
[[ "${application_name}" =~ ^[A-Za-z0-9_.-]+$ ]] || { echo "application name is invalid" >&2; exit 2; }
fi
[[ "${clusterguard_hostname}" =~ ^[A-Za-z0-9]$|^[A-Za-z0-9][A-Za-z0-9.-]*[A-Za-z0-9]$ ]] || {
  echo "CG_PG_HOSTNAME is invalid" >&2
  exit 2
}
[[ "${node_id}" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] || {
  echo "CG_PG_NODE_ID must be a lowercase UUIDv4" >&2
  exit 2
}
if [[ ! -s "${data_directory}/PG_VERSION" && -n "${primary_node_id}" && ! "${primary_node_id}" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]]; then
  echo "CG_PG_PRIMARY_NODE_ID must be empty or a lowercase UUIDv4" >&2
  exit 2
fi

write_identity_configuration() {
  local configuration="${data_directory}/postgresql.auto.conf"
  local replace_primary="${1:-false}"
  local temporary
  [[ -s "${data_directory}/PG_VERSION" ]] || return 0
  temporary="$(mktemp "${data_directory}/.clusterguard-identity.XXXXXX")"
  if [[ -f "${configuration}" ]]; then
    if [[ "${replace_primary}" == "true" ]]; then
      grep -Ev '^(clusterguard\.(node_id|primary_node_id|hostname))[[:space:]]*=' "${configuration}" >"${temporary}" || true
    else
      grep -Ev '^(clusterguard\.(node_id|hostname))[[:space:]]*=' "${configuration}" >"${temporary}" || true
    fi
  fi
  {
    printf "clusterguard.node_id = '%s'\n" "${node_id}"
    if [[ "${replace_primary}" == "true" ]]; then
      printf "clusterguard.primary_node_id = '%s'\n" "${primary_node_id}"
    fi
    printf "clusterguard.hostname = '%s'\n" "${clusterguard_hostname}"
  } >>"${temporary}"
  chown postgres:postgres "${temporary}"
  chmod 0600 "${temporary}"
  mv -f "${temporary}" "${configuration}"
}

if [[ -s "${data_directory}/PG_VERSION" ]]; then
  # Runtime upstream and role are persisted by PostgreSQL/ClusterGuard. The
  # original stack's source host must never overwrite them after promotion.
  # Older stacks used an ephemeral passfile outside PGDATA. Recreate only that
  # missing credential file, without using the bootstrap source as topology.
  passfile=/var/lib/postgresql/.pgpass
  secret_file=/run/secrets/postgres_replication_password
  if [[ ! -s "${passfile}" && -s "${secret_file}" ]]; then
    [[ "${replication_user}" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || { echo "replication user is invalid" >&2; exit 2; }
    temporary_passfile="$(mktemp /var/lib/postgresql/.pgpass.XXXXXX)"
    chmod 0600 "${temporary_passfile}"
    replication_password="$(tr -d '\r\n' <"${secret_file}")"
    replication_password="${replication_password//\\/\\\\}"
    replication_password="${replication_password//:/\\:}"
    printf '*:*:*:%s:%s\n' "${replication_user}" "${replication_password}" >"${temporary_passfile}"
    unset replication_password
    chown postgres:postgres "${temporary_passfile}"
    mv -f "${temporary_passfile}" "${passfile}"
  fi
  write_identity_configuration false
  exec /usr/local/bin/docker-entrypoint.sh "$@"
fi

if [[ "${slot}" == "01" ]]; then
  write_identity_configuration false
  exec /usr/local/bin/docker-entrypoint.sh "$@"
fi

[[ "${primary_slot}" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || { echo "physical replication slot is invalid" >&2; exit 2; }
secret_file=/run/secrets/postgres_replication_password
[[ -s "${secret_file}" ]] || { echo "replication password secret is missing" >&2; exit 3; }

install -d -m 0700 -o postgres -g postgres "${data_directory}"
passfile=/var/lib/postgresql/.pgpass
temporary_passfile="$(mktemp /var/lib/postgresql/.pgpass.XXXXXX)"
chmod 0600 "${temporary_passfile}"
printf '%s:%s:*:%s:%s\n' \
  "${source_host}" "${source_port}" "${replication_user}" "$(tr -d '\r\n' <"${secret_file}")" \
  >"${temporary_passfile}"
chown postgres:postgres "${temporary_passfile}"
mv -f "${temporary_passfile}" "${passfile}"

bootstrapped_now=false
if [[ ! -s "${data_directory}/PG_VERSION" ]]; then
  if find "${data_directory}" -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
    echo "replica data directory is non-empty but has no PG_VERSION; refusing automatic overwrite" >&2
    exit 4
  fi
  deadline=$((SECONDS + 300))
  until [[ "$(gosu postgres env PGPASSFILE="${passfile}" psql \
    --no-psqlrc --tuples-only --no-align --set=ON_ERROR_STOP=1 \
    --host="${source_host}" --port="${source_port}" --username="${replication_user}" --dbname=postgres \
    --command="SELECT count(*) FROM pg_replication_slots WHERE slot_name = '${primary_slot}';" 2>/dev/null || true)" == "1" ]]; do
    ((SECONDS < deadline)) || { echo "primary did not become ready before replica bootstrap timeout" >&2; exit 5; }
    sleep 2
  done
  gosu postgres env PGPASSFILE="${passfile}" pg_basebackup \
    --host="${source_host}" --port="${source_port}" --username="${replication_user}" \
    --pgdata="${data_directory}" --format=plain --wal-method=stream --checkpoint=fast --progress
  touch "${data_directory}/standby.signal"
  cat >>"${data_directory}/postgresql.auto.conf" <<EOF
primary_conninfo = 'host=${source_host} port=${source_port} user=${replication_user} passfile=${passfile} application_name=${application_name} connect_timeout=5'
primary_slot_name = '${primary_slot}'
hot_standby = 'on'
EOF
  chown postgres:postgres "${data_directory}/standby.signal" "${data_directory}/postgresql.auto.conf"
  chmod 0600 "${data_directory}/postgresql.auto.conf"
  bootstrapped_now=true
fi

write_identity_configuration "${bootstrapped_now}"

exec /usr/local/bin/docker-entrypoint.sh "$@"
