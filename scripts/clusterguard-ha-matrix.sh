#!/usr/bin/env bash
set -euo pipefail

api="http://127.0.0.1:8088"
clusters=""
round_robin=20
random_cycles=30
matrix_seed=20260713
parallel=1
control_token_environment="CG_CONTROL_TOKEN"
approval_token_environment="CG_APPROVAL_TOKEN"
script_dir="${script_dir:-$(cd "$(dirname "$0")" && pwd)}"
insecure=false
total=0
passed=0
failed=0

finish() {
  local status=$?
  trap - EXIT
  printf 'matrix_summary total=%s passed=%s failed=%s seed=%s\n' "${total}" "${passed}" "${failed}" "${matrix_seed}"
  exit "${status}"
}
trap finish EXIT

usage() {
  echo "usage: $0 --clusters UUID[,UUID...] [--api URL] [--round-robin N] [--random N] [--seed N] [--parallel N]"
}

while (($#)); do
  case "$1" in
    --api) api="${2:-}"; shift 2 ;;
    --clusters) clusters="${2:-}"; shift 2 ;;
    --round-robin) round_robin="${2:-}"; shift 2 ;;
    --random) random_cycles="${2:-}"; shift 2 ;;
    --seed) matrix_seed="${2:-}"; shift 2 ;;
    --parallel) parallel="${2:-}"; shift 2 ;;
    --token-env) control_token_environment="${2:-}"; shift 2 ;;
    --approval-env) approval_token_environment="${2:-}"; shift 2 ;;
    --insecure) insecure=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown matrix argument: $1" >&2; exit 2 ;;
  esac
done

for value in "${round_robin}" "${random_cycles}" "${matrix_seed}"; do
  [[ "${value}" =~ ^[0-9]+$ ]] || { echo "matrix counts and seed must be non-negative integers" >&2; exit 2; }
done
[[ "${parallel}" =~ ^[1-9][0-9]*$ ]] || { echo "parallel must be a positive integer" >&2; exit 2; }

IFS=',' read -r -a cluster_ids <<<"${clusters}"
[[ "${#cluster_ids[@]}" -gt 0 && -n "${cluster_ids[0]}" ]] || { echo "at least one cluster UUID is required" >&2; exit 2; }
uuid_pattern='^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$'
for ((index=0; index<${#cluster_ids[@]}; index++)); do
  [[ "${cluster_ids[index]}" =~ ${uuid_pattern} ]] || { echo "invalid cluster UUID: ${cluster_ids[index]}" >&2; exit 2; }
  for ((previous=0; previous<index; previous++)); do
    [[ "${cluster_ids[index]}" != "${cluster_ids[previous]}" ]] || { echo "cluster UUIDs must be unique" >&2; exit 2; }
  done
done
if ((parallel > ${#cluster_ids[@]})); then
  parallel=${#cluster_ids[@]}
fi

control_token="${!control_token_environment:-}"
approval_token="${!approval_token_environment:-}"
[[ -n "${control_token}" && -n "${approval_token}" ]] || { echo "control and approval token environments are required" >&2; exit 2; }
RANDOM=$((matrix_seed % 32768))

api_get() {
  if [[ "${insecure}" == true ]]; then
    curl -k --fail --silent --show-error --max-time 15 \
      -H "Authorization: Bearer ${control_token}" -H 'Accept: application/json' "$1"
  else
    curl --fail --silent --show-error --max-time 15 \
      -H "Authorization: Bearer ${control_token}" -H 'Accept: application/json' "$1"
  fi
}

api_post() {
  local url="$1" payload="$2"
  local body_file http_status
  body_file="$(mktemp "${TMPDIR:-/tmp}/clusterguard-api.XXXXXX")" || return 1
  if [[ "${insecure}" == true ]]; then
    if ! http_status="$(curl -k --silent --show-error --max-time 180 --output "${body_file}" --write-out '%{http_code}' \
      -H "Authorization: Bearer ${control_token}" -H 'Content-Type: application/json' -d "${payload}" "${url}")"; then
      rm -f "${body_file}"
      echo "api_transport_error method=POST" >&2
      return 1
    fi
  else
    if ! http_status="$(curl --silent --show-error --max-time 180 --output "${body_file}" --write-out '%{http_code}' \
      -H "Authorization: Bearer ${control_token}" -H 'Content-Type: application/json' -d "${payload}" "${url}")"; then
      rm -f "${body_file}"
      echo "api_transport_error method=POST" >&2
      return 1
    fi
  fi
  if [[ ! "${http_status}" =~ ^2[0-9][0-9]$ ]]; then
    if jq -e . "${body_file}" >/dev/null 2>&1; then
      jq -r --arg http "${http_status}" '
        def compact: tostring | gsub("[\\r\\n\\t]+"; " ") | .[0:240];
        . as $root |
        [
          "api_error",
          "http=" + $http,
          "status=" + (($root.status // "unknown") | compact),
          "message=" + (($root.message // "not_available") | compact),
          "operation=" + (($root.result.resource_id // "unknown") | compact),
          "stage=" + (($root.result.stage // "unknown") | compact),
          "failure_class=" + (($root.result.failure_class // $root.result.execution.failure_class // "unknown") | compact),
          "failed_checks=" + ([
            $root.result.verification.checks[]?
            | select(.status == "fail" or .status == "blocked")
            | .name
          ] | unique | join(",") | if . == "" then "none" else . end)
        ] | join(" ")
      ' "${body_file}" >&2
    else
      echo "api_error http=${http_status} status=invalid_json" >&2
    fi
    rm -f "${body_file}"
    return 22
  fi
  cat "${body_file}"
  rm -f "${body_file}"
}

execute_switch() {
  local cluster_id="$1" ordinal="$2" target_offset="$3"
  local candidates eligible_count target payload response operation_id
  local -a smoke_command
  if ! candidates="$(api_get "${api%/}/api/v1/clusters/${cluster_id}/candidates")"; then
    echo "switch_failed ordinal=${ordinal} cluster=${cluster_id} stage=candidates" >&2
    return 1
  fi
  eligible_count="$(jq -r '[.result[] | select(.eligible == true)] | length' <<<"${candidates}")"
  [[ "${eligible_count}" =~ ^[1-9][0-9]*$ ]] || {
    echo "switch_failed ordinal=${ordinal} cluster=${cluster_id} stage=candidates reason=no_eligible_candidate" >&2
    return 1
  }
  target="$(jq -r --argjson offset "${target_offset}" \
    '[.result[] | select(.eligible == true)] | sort_by(.rank) | .[$offset % length].instance_id // ""' <<<"${candidates}")"
  [[ -n "${target}" ]] || {
    echo "switch_failed ordinal=${ordinal} cluster=${cluster_id} stage=candidates reason=empty_target" >&2
    return 1
  }
  payload="$(jq -nc --arg cluster_id "${cluster_id}" --arg target_id "${target}" --arg approval_token "${approval_token}" --arg key "matrix-${cluster_id}-${ordinal}-$(date +%s%N)" \
    '{operation:{cluster_id:$cluster_id,engine:"mysql",kind:"switchover",requested_by:"cg-ha-matrix"},target_id:$target_id,idempotency_key:$key,approval_token:$approval_token}')"
  if ! response="$(api_post "${api%/}/api/v1/operations/execute" "${payload}")"; then
    echo "switch_failed ordinal=${ordinal} cluster=${cluster_id} target=${target} stage=execute" >&2
    return 1
  fi
  operation_id="$(jq -r '.result.resource_id // ""' <<<"${response}")"
  if ! jq -e '.status == "ok" and .result.status == "succeeded"' <<<"${response}" >/dev/null; then
    echo "switch_failed ordinal=${ordinal} cluster=${cluster_id} target=${target} operation=${operation_id:-unknown} stage=verify_response" >&2
    return 1
  fi
  if [[ "${insecure}" == true ]]; then
    smoke_command=("${script_dir}/clusterguard-smoke.sh" --api "${api}" --cluster "${cluster_id}" --token-env "${control_token_environment}" --insecure)
  else
    smoke_command=("${script_dir}/clusterguard-smoke.sh" --api "${api}" --cluster "${cluster_id}" --token-env "${control_token_environment}")
  fi
  if ! "${smoke_command[@]}" >/dev/null; then
    echo "switch_failed ordinal=${ordinal} cluster=${cluster_id} target=${target} operation=${operation_id:-unknown} stage=smoke" >&2
    return 1
  fi
  printf 'verified switch ordinal=%s operation=%s cluster=%s target=%s candidate_offset=%s\n' \
    "${ordinal}" "${operation_id}" "${cluster_id}" "${target}" "${target_offset}"
}

cluster_attempts=()
for ((index=0; index<${#cluster_ids[@]}; index++)); do
  cluster_attempts[index]=0
done
ordinal=0

run_group() {
  local cluster_index target_offset group_failed index
  local -a pids=()
  local -a labels=()
  for cluster_index in "$@"; do
    target_offset="${cluster_attempts[cluster_index]}"
    cluster_attempts[cluster_index]=$((target_offset + 1))
    labels+=("${cluster_ids[cluster_index]}")
    execute_switch "${cluster_ids[cluster_index]}" "${ordinal}" "${target_offset}" &
    pids+=("$!")
    ordinal=$((ordinal + 1))
    total=$((total + 1))
  done
  group_failed=0
  for ((index=0; index<${#pids[@]}; index++)); do
    if wait "${pids[index]}"; then
      passed=$((passed + 1))
    else
      failed=$((failed + 1))
      group_failed=1
      echo "matrix_group_failure cluster=${labels[index]}" >&2
    fi
  done
  ((group_failed == 0))
}

completed=0
while ((completed < round_robin)); do
  group=()
  while ((${#group[@]} < parallel && completed < round_robin)); do
    group+=("$((completed % ${#cluster_ids[@]}))")
    completed=$((completed + 1))
  done
  run_group "${group[@]}"
done

completed=0
while ((completed < random_cycles)); do
  order=()
  for ((index=0; index<${#cluster_ids[@]}; index++)); do
    order+=("${index}")
  done
  for ((index=${#order[@]}-1; index>0; index--)); do
    swap_index=$((RANDOM % (index + 1)))
    swap_value="${order[index]}"
    order[index]="${order[swap_index]}"
    order[swap_index]="${swap_value}"
  done
  remaining=$((random_cycles - completed))
  batch_size=${#order[@]}
  if ((remaining < batch_size)); then
    batch_size=${remaining}
  fi
  position=0
  while ((position < batch_size)); do
    group=()
    while ((${#group[@]} < parallel && position < batch_size)); do
      group+=("${order[position]}")
      position=$((position + 1))
      completed=$((completed + 1))
    done
    run_group "${group[@]}"
  done
done
