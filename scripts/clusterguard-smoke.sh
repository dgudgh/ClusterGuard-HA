#!/usr/bin/env bash
set -euo pipefail

api="http://127.0.0.1:8088"
cluster_id=""
token_environment="CG_CONTROL_TOKEN"
curl_extra=()
while (($#)); do
  case "$1" in
    --api) api="${2:-}"; shift 2 ;;
    --cluster) cluster_id="${2:-}"; shift 2 ;;
    --token-env) token_environment="${2:-}"; shift 2 ;;
    --insecure) curl_extra+=(-k); shift ;;
    -h|--help) echo "usage: $0 --cluster UUID [--api URL] [--token-env ENV] [--insecure]"; exit 0 ;;
    *) echo "unknown smoke argument: $1" >&2; exit 2 ;;
  esac
done
[[ "${cluster_id}" =~ ^[0-9A-Fa-f-]{36}$ ]] || { echo "cluster UUID is required" >&2; exit 2; }
command -v curl >/dev/null 2>&1 && command -v jq >/dev/null 2>&1 || { echo "curl and jq are required" >&2; exit 2; }
token="${!token_environment:-}"
[[ -n "${token}" ]] || { echo "control token environment is empty" >&2; exit 2; }

api_get() {
  curl "${curl_extra[@]}" --fail --silent --show-error --max-time 10 -H "Authorization: Bearer ${token}" -H 'Accept: application/json' "$1"
}

topology="$(api_get "${api%/}/api/v1/clusters/${cluster_id}/topology")"
ha_endpoints="$(api_get "${api%/}/api/v1/clusters/${cluster_id}/ha-endpoints")"
jq -e '.status == "ok" and (.result | type == "object")' <<<"${topology}" >/dev/null
jq -e '.status == "ok" and (.result | type == "array")' <<<"${ha_endpoints}" >/dev/null

primary_id="$(jq -r '.result.instances[] | select(.role == "primary" and .health.state == "healthy") | .resource_id' <<<"${topology}")"
writer_count="$(jq '[.result.instances[] | select(.role == "primary" and .health.state == "healthy")] | length' <<<"${topology}")"
vip_owner_count="$(jq '[.result[] | select(.resource.kind == "vip" and .endpoint.active == true and .resource.healthy == true and (.resource.owner_id | length) > 0)] | length' <<<"${ha_endpoints}")"
vip_owner_id="$(jq -r '.result[] | select(.resource.kind == "vip" and .endpoint.active == true and .resource.healthy == true) | .resource.owner_id' <<<"${ha_endpoints}")"
replica_failures="$(jq '[.result.instances[] | select(.role == "replica" and (.replication.io_thread != "running" or .replication.sql_thread != "running"))] | length' <<<"${topology}")"
observed_at="$(jq -r '.result.observed_at // ""' <<<"${topology}")"

writer_status=fail
vip_count_status=fail
vip_primary_status=fail
replica_status=fail
fresh_status=fail
[[ "${writer_count}" == "1" ]] && writer_status=pass
[[ "${vip_owner_count}" == "1" ]] && vip_count_status=pass
[[ -n "${primary_id}" && "${primary_id}" == "${vip_owner_id}" ]] && vip_primary_status=pass
[[ "${replica_failures}" == "0" ]] && replica_status=pass
[[ -n "${observed_at}" && "$(jq -r '.result.health.state' <<<"${topology}")" != "unknown" ]] && fresh_status=pass

result="$(jq -nc \
  --arg writer_count "${writer_status}" --arg vip_owner_count "${vip_count_status}" \
  --arg vip_owner_is_primary "${vip_primary_status}" --arg replica_threads_healthy "${replica_status}" \
  --arg topology_fresh "${fresh_status}" --arg cluster_id "${cluster_id}" \
  '{cluster_id:$cluster_id,checks:[
    {name:"writer_count",status:$writer_count},
    {name:"vip_owner_count",status:$vip_owner_count},
    {name:"vip_owner_is_primary",status:$vip_owner_is_primary},
    {name:"replica_threads_healthy",status:$replica_threads_healthy},
    {name:"topology_fresh",status:$topology_fresh}
  ]}')"
jq . <<<"${result}"
jq -e 'all(.checks[]; .status == "pass")' <<<"${result}" >/dev/null

