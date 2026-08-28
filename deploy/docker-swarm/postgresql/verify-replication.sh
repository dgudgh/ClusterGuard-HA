#!/usr/bin/env bash
set -euo pipefail

slot="${1:-}"
cluster_id="${CG_CLUSTER_ID:-}"
[[ "${slot}" =~ ^0[123]$ ]] || { echo "usage: $0 01|02|03" >&2; exit 2; }
[[ -n "${cluster_id}" ]] || { echo "CG_CLUSTER_ID is required" >&2; exit 2; }

case "${slot}" in
  01) instance_id="${CG_INSTANCE_01_ID:?CG_INSTANCE_01_ID is required}" ;;
  02) instance_id="${CG_INSTANCE_02_ID:?CG_INSTANCE_02_ID is required}" ;;
  03) instance_id="${CG_INSTANCE_03_ID:?CG_INSTANCE_03_ID is required}" ;;
esac

containers="$(docker ps \
  --filter "label=clusterguard.cluster_id=${cluster_id}" \
  --filter "label=clusterguard.instance_id=${instance_id}" \
  --format '{{.ID}}')"
[[ "$(wc -w <<<"${containers}")" -eq 1 ]] || { echo "expected one running PostgreSQL container for slot ${slot}" >&2; exit 3; }
container="${containers}"

psql_local() {
  docker exec -u postgres "${container}" psql --no-psqlrc --tuples-only --no-align --set=ON_ERROR_STOP=1 "$@"
}

recovery="$(psql_local --dbname postgres --command 'SELECT pg_is_in_recovery();')"
if [[ "${slot}" == "01" ]]; then
  [[ "${recovery}" == "f" ]] || { echo "slot 01 is unexpectedly in recovery" >&2; exit 4; }
  streaming="$(psql_local --dbname postgres --command "SELECT count(*) FROM pg_stat_replication WHERE state = 'streaming';")"
  [[ "${streaming}" == "2" ]] || { echo "expected two streaming replicas, found ${streaming}" >&2; exit 4; }
else
  [[ "${recovery}" == "t" ]] || { echo "slot ${slot} is not in recovery" >&2; exit 4; }
fi

rows="$(psql_local --dbname clusterguard_validation --command 'SELECT count(*) FROM swarm_probe;')"
[[ "${rows}" =~ ^[1-9][0-9]*$ ]] || { echo "replication probe is missing" >&2; exit 4; }
printf 'slot=%s recovery=%s probe_rows=%s\n' "${slot}" "${recovery}" "${rows}"
