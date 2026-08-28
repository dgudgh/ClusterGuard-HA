#!/usr/bin/env bash
set -Eeuo pipefail

[[ "${CG_PG_SLOT:-}" == "01" ]] || exit 0
replication_cidr="${CG_PG_REPLICATION_CIDR:-192.168.102.0/24}"
[[ "${replication_cidr}" =~ ^[0-9a-fA-F:.]+/[0-9]+$ ]] || { echo "replication CIDR is invalid" >&2; exit 2; }

read_secret() {
  local path="$1"
  [[ -s "${path}" ]] || { echo "required PostgreSQL secret is missing: ${path}" >&2; exit 3; }
  tr -d '\r\n' <"${path}"
}

discovery_password="$(read_secret /run/secrets/postgres_discovery_password)"
operator_password="$(read_secret /run/secrets/postgres_operator_password)"
replication_password="$(read_secret /run/secrets/postgres_replication_password)"
node_id="${CG_PG_NODE_ID:-}"
hostname="${CG_PG_HOSTNAME:-}"

psql -v ON_ERROR_STOP=1 --username "${POSTGRES_USER}" --dbname postgres \
  --set=discovery_password="${discovery_password}" \
  --set=operator_password="${operator_password}" \
  --set=replication_password="${replication_password}" \
  --set=node_id="${node_id}" \
  --set=hostname="${hostname}" <<'SQL'
SELECT format('CREATE ROLE cg_discovery LOGIN PASSWORD %L', :'discovery_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'cg_discovery')\gexec
ALTER ROLE cg_discovery LOGIN PASSWORD :'discovery_password';
GRANT pg_monitor TO cg_discovery;

SELECT format('CREATE ROLE cg_operator LOGIN SUPERUSER PASSWORD %L', :'operator_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'cg_operator')\gexec
ALTER ROLE cg_operator LOGIN SUPERUSER PASSWORD :'operator_password';

SELECT format('CREATE ROLE cg_replication LOGIN REPLICATION PASSWORD %L', :'replication_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'cg_replication')\gexec
ALTER ROLE cg_replication LOGIN REPLICATION PASSWORD :'replication_password';
GRANT pg_monitor TO cg_replication;

SELECT pg_create_physical_replication_slot('cg_slot_02')
WHERE NOT EXISTS (SELECT 1 FROM pg_replication_slots WHERE slot_name = 'cg_slot_02');
SELECT pg_create_physical_replication_slot('cg_slot_03')
WHERE NOT EXISTS (SELECT 1 FROM pg_replication_slots WHERE slot_name = 'cg_slot_03');

ALTER SYSTEM SET clusterguard.node_id = :'node_id';
ALTER SYSTEM SET clusterguard.primary_node_id = '';
ALTER SYSTEM SET clusterguard.hostname = :'hostname';

SELECT 'CREATE DATABASE clusterguard_validation'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'clusterguard_validation')\gexec
SQL

psql -v ON_ERROR_STOP=1 --username "${POSTGRES_USER}" --dbname clusterguard_validation <<'SQL'
CREATE TABLE IF NOT EXISTS swarm_probe (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  marker text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO swarm_probe(marker)
SELECT 'docker-swarm-bootstrap'
WHERE NOT EXISTS (SELECT 1 FROM swarm_probe WHERE marker = 'docker-swarm-bootstrap');
SQL

hba_line="host replication cg_replication ${replication_cidr} scram-sha-256"
if ! grep -Fqx "${hba_line}" "${PGDATA}/pg_hba.conf"; then
  printf '%s\n' "${hba_line}" >>"${PGDATA}/pg_hba.conf"
fi
