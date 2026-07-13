#!/usr/bin/env bash
set -euo pipefail

api="http://127.0.0.1:8088"
clusters=""
round_robin=20
random_cycles=30
control_token_environment="CG_CONTROL_TOKEN"
approval_token_environment="CG_APPROVAL_TOKEN"
curl_extra=()
while (($#)); do
  case "$1" in
    --api) api="${2:-}"; shift 2 ;;
    --clusters) clusters="${2:-}"; shift 2 ;;
    --round-robin) round_robin="${2:-}"; shift 2 ;;
    --random) random_cycles="${2:-}"; shift 2 ;;
    --token-env) control_token_environment="${2:-}"; shift 2 ;;
    --approval-env) approval_token_environment="${2:-}"; shift 2 ;;
    --insecure) curl_extra+=(-k); shift ;;
    -h|--help) echo "usage: $0 --clusters UUID[,UUID...] [--api URL] [--round-robin N] [--random N]"; exit 0 ;;
    *) echo "unknown matrix argument: $1" >&2; exit 2 ;;
  esac
done
IFS=',' read -r -a cluster_ids <<<"${clusters}"
[[ "${#cluster_ids[@]}" -gt 0 && -n "${cluster_ids[0]}" ]] || { echo "at least one cluster UUID is required" >&2; exit 2; }
control_token="${!control_token_environment:-}"
approval_token="${!approval_token_environment:-}"
[[ -n "${control_token}" && -n "${approval_token}" ]] || { echo "control and approval token environments are required" >&2; exit 2; }

api_get() {
  curl "${curl_extra[@]}" --fail --silent --show-error --max-time 15 -H "Authorization: Bearer ${control_token}" -H 'Accept: application/json' "$1"
}
api_post() {
  local url="$1" payload="$2"
  curl "${curl_extra[@]}" --fail --silent --show-error --max-time 180 -H "Authorization: Bearer ${control_token}" -H 'Content-Type: application/json' -d "${payload}" "${url}"
}

execute_switch() {
  local cluster_id="$1" ordinal="$2"
  local candidates target payload response operation_id
  candidates="$(api_get "${api%/}/api/v1/clusters/${cluster_id}/candidates")"
  target="$(jq -r '[.result[] | select(.eligible == true)] | sort_by(.rank) | .[0].instance_id // ""' <<<"${candidates}")"
  [[ -n "${target}" ]] || { echo "cluster ${cluster_id} has no eligible candidate" >&2; return 1; }
  payload="$(jq -nc --arg cluster_id "${cluster_id}" --arg target_id "${target}" --arg approval_token "${approval_token}" --arg key "matrix-${cluster_id}-${ordinal}-$(date +%s%N)" \
    '{operation:{cluster_id:$cluster_id,engine:"mysql",kind:"switchover",requested_by:"cg-ha-matrix"},target_id:$target_id,idempotency_key:$key,approval_token:$approval_token}')"
  response="$(api_post "${api%/}/api/v1/operations/execute" "${payload}")"
  operation_id="$(jq -r '.result.resource_id // ""' <<<"${response}")"
  jq -e '.status == "ok" and .result.status == "succeeded"' <<<"${response}" >/dev/null || {
    echo "switch ${operation_id:-unknown} was not verified" >&2
    return 1
  }
  "${script_dir:-$(cd "$(dirname "$0")" && pwd)}/clusterguard-smoke.sh" --api "${api}" --cluster "${cluster_id}" --token-env "${control_token_environment}" ${curl_extra:+--insecure} >/dev/null
  printf 'verified switch %s cluster=%s target=%s\n' "${operation_id}" "${cluster_id}" "${target}"
}

ordinal=0
for ((index=0; index<round_robin; index++)); do
  cluster_id="${cluster_ids[$((index % ${#cluster_ids[@]}))]}"
  execute_switch "${cluster_id}" "${ordinal}"
  ordinal=$((ordinal + 1))
done
for ((index=0; index<random_cycles; index++)); do
  cluster_id="${cluster_ids[$((RANDOM % ${#cluster_ids[@]}))]}"
  execute_switch "${cluster_id}" "${ordinal}"
  ordinal=$((ordinal + 1))
done

