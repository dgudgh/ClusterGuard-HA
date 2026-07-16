#!/usr/bin/env bash
set -euo pipefail

api="http://127.0.0.1:8088"
clusters=""
round_robin=20
random_cycles=30
matrix_seed=20260713
parallel=1
smoke_attempts=15
smoke_interval=2
api_timeout=180
read_attempts="${CG_MATRIX_READ_ATTEMPTS:-3}"
read_retry_interval="${CG_MATRIX_READ_RETRY_INTERVAL:-1}"
transport_reconcile_attempts="${CG_MATRIX_RECONCILE_ATTEMPTS:-45}"
transport_reconcile_interval="${CG_MATRIX_RECONCILE_INTERVAL:-2}"
control_token_environment="CG_CONTROL_TOKEN"
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
  echo "usage: $0 --clusters UUID[,UUID...] [--api URL] [--round-robin N] [--random N] [--seed N] [--parallel N] [--smoke-attempts N] [--smoke-interval SECONDS] [--api-timeout SECONDS]"
}

while (($#)); do
  case "$1" in
    --api) api="${2:-}"; shift 2 ;;
    --clusters) clusters="${2:-}"; shift 2 ;;
    --round-robin) round_robin="${2:-}"; shift 2 ;;
    --random) random_cycles="${2:-}"; shift 2 ;;
    --seed) matrix_seed="${2:-}"; shift 2 ;;
    --parallel) parallel="${2:-}"; shift 2 ;;
    --smoke-attempts) smoke_attempts="${2:-}"; shift 2 ;;
    --smoke-interval) smoke_interval="${2:-}"; shift 2 ;;
    --api-timeout) api_timeout="${2:-}"; shift 2 ;;
    --token-env) control_token_environment="${2:-}"; shift 2 ;;
    --insecure) insecure=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown matrix argument: $1" >&2; exit 2 ;;
  esac
done

for value in "${round_robin}" "${random_cycles}" "${matrix_seed}" "${smoke_interval}"; do
  [[ "${value}" =~ ^[0-9]+$ ]] || { echo "matrix counts and seed must be non-negative integers" >&2; exit 2; }
done
[[ "${parallel}" =~ ^[1-9][0-9]*$ ]] || { echo "parallel must be a positive integer" >&2; exit 2; }
[[ "${smoke_attempts}" =~ ^[1-9][0-9]*$ ]] || { echo "smoke attempts must be a positive integer" >&2; exit 2; }
[[ "${api_timeout}" =~ ^[1-9][0-9]*$ ]] || { echo "API timeout must be a positive integer" >&2; exit 2; }
[[ "${read_attempts}" =~ ^[1-9][0-9]*$ ]] || { echo "read attempts must be a positive integer" >&2; exit 2; }
[[ "${read_retry_interval}" =~ ^[0-9]+$ ]] || { echo "read retry interval must be a non-negative integer" >&2; exit 2; }
[[ "${transport_reconcile_attempts}" =~ ^[1-9][0-9]*$ ]] || { echo "transport reconcile attempts must be a positive integer" >&2; exit 2; }
[[ "${transport_reconcile_interval}" =~ ^[0-9]+$ ]] || { echo "transport reconcile interval must be a non-negative integer" >&2; exit 2; }

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
[[ -n "${control_token}" ]] || { echo "control token environment is required" >&2; exit 2; }
RANDOM=$((matrix_seed % 32768))

api_get() {
  local url="$1" attempt
  for ((attempt=1; attempt<=read_attempts; attempt++)); do
    if [[ "${insecure}" == true ]]; then
      if curl -k --fail --silent --show-error --max-time 15 \
        -H "Authorization: Bearer ${control_token}" -H 'Accept: application/json' "${url}"; then
        return 0
      fi
    elif curl --fail --silent --show-error --max-time 15 \
      -H "Authorization: Bearer ${control_token}" -H 'Accept: application/json' "${url}"; then
      return 0
    fi
    if ((attempt < read_attempts)); then
      echo "api_read_retry attempt=${attempt} max_attempts=${read_attempts}" >&2
      if ((read_retry_interval > 0)); then
        sleep "${read_retry_interval}"
      fi
    fi
  done
  return 1
}

reconcile_operation_after_transport() {
  local idempotency_key="$1"
  local encoded_key response operation_status operation_id failure_class attempt last_status
  encoded_key="$(jq -rn --arg value "${idempotency_key}" '$value | @uri')"
  last_status="not_found"
  for ((attempt=1; attempt<=transport_reconcile_attempts; attempt++)); do
    if response="$(api_get "${api%/}/api/v1/operations?idempotency_key=${encoded_key}" 2>/dev/null)" && jq -e '.status == "ok" and (.result.status | type == "string")' <<<"${response}" >/dev/null 2>&1; then
      operation_status="$(jq -r '.result.status' <<<"${response}")"
      operation_id="$(jq -r '.result.resource_id // "unknown"' <<<"${response}")"
      last_status="${operation_status}"
      case "${operation_status}" in
        succeeded)
          echo "api_transport_reconciled operation=${operation_id} status=succeeded attempt=${attempt}" >&2
          printf '%s\n' "${response}"
          return 0
          ;;
        blocked|failed|indeterminate|unsupported)
          failure_class="$(jq -r '.result.failure_class // .result.execution.failure_class // "unknown"' <<<"${response}")"
          echo "api_transport_reconciled operation=${operation_id} status=${operation_status} failure_class=${failure_class} attempt=${attempt}" >&2
          if [[ "${failure_class}" == "stale_plan" ]]; then
            return 75
          fi
          return 22
          ;;
      esac
    fi
    if ((attempt < transport_reconcile_attempts && transport_reconcile_interval > 0)); then
      sleep "${transport_reconcile_interval}"
    fi
  done
  echo "api_transport_ambiguous status=${last_status} attempts=${transport_reconcile_attempts}" >&2
  return 1
}

post_request() {
  local url="$1" payload="$2" body_file="$3" header_file="$4" authorization_mode="$5"
  if [[ "${insecure}" == true ]]; then
    if [[ "${authorization_mode}" == "admin" ]]; then
      curl -k --silent --show-error --max-time "${api_timeout}" --output "${body_file}" --dump-header "${header_file}" --write-out '%{http_code}' \
        -H "Authorization: Bearer ${control_token}" -H 'Content-Type: application/json' -d "${payload}" "${url}"
    else
      curl -k --silent --show-error --max-time "${api_timeout}" --output "${body_file}" --dump-header "${header_file}" --write-out '%{http_code}' \
        -H 'Content-Type: application/json' -d "${payload}" "${url}"
    fi
  else
    if [[ "${authorization_mode}" == "admin" ]]; then
      curl --silent --show-error --max-time "${api_timeout}" --output "${body_file}" --dump-header "${header_file}" --write-out '%{http_code}' \
        -H "Authorization: Bearer ${control_token}" -H 'Content-Type: application/json' -d "${payload}" "${url}"
    else
      curl --silent --show-error --max-time "${api_timeout}" --output "${body_file}" --dump-header "${header_file}" --write-out '%{http_code}' \
        -H 'Content-Type: application/json' -d "${payload}" "${url}"
    fi
  fi
}

api_post() {
  local url="$1" payload="$2" authorization_mode="${3:-admin}"
  local body_file header_file http_status current_url leader_api leader_raft leader_api_host leader_raft_host attempt
  local idempotency_key reconcile_response reconcile_status
  body_file="$(mktemp "${TMPDIR:-/tmp}/clusterguard-api.XXXXXX")" || return 1
  header_file="$(mktemp "${TMPDIR:-/tmp}/clusterguard-api-headers.XXXXXX")" || { rm -f "${body_file}"; return 1; }
  current_url="${url}"
  idempotency_key="$(jq -r '.idempotency_key // ""' <<<"${payload}")"
  for attempt in 1 2; do
    : >"${body_file}"
    : >"${header_file}"
    if ! http_status="$(post_request "${current_url}" "${payload}" "${body_file}" "${header_file}" "${authorization_mode}")"; then
      echo "api_transport_error method=POST resolution=idempotency_lookup" >&2
      if [[ -n "${idempotency_key}" ]] && reconcile_response="$(reconcile_operation_after_transport "${idempotency_key}")"; then
        rm -f "${body_file}" "${header_file}"
        printf '%s\n' "${reconcile_response}"
        return 0
      else
        reconcile_status=$?
        rm -f "${body_file}" "${header_file}"
        return "${reconcile_status}"
      fi
    fi
    if [[ "${http_status}" == "503" && "${attempt}" -eq 1 ]]; then
      leader_api="$(awk 'tolower($0) ~ /^x-clusterguard-leader-api-address:/ { sub(/^[^:]+:[[:space:]]*/, ""); sub(/\r$/, ""); value=$0 } END { print value }' "${header_file}")"
      leader_raft="$(awk 'tolower($0) ~ /^x-clusterguard-leader-address:/ { sub(/^[^:]+:[[:space:]]*/, ""); sub(/\r$/, ""); value=$0 } END { print value }' "${header_file}")"
      if [[ "${leader_api}" =~ ^https?://[^/?#]+/?$ && -n "${leader_raft}" ]]; then
        leader_api_host="${leader_api#*://}"
        leader_api_host="${leader_api_host%%/*}"
        leader_api_host="${leader_api_host%%:*}"
        leader_api_host="${leader_api_host#[}"
        leader_api_host="${leader_api_host%]}"
        leader_raft_host="${leader_raft%%:*}"
        leader_raft_host="${leader_raft_host#[}"
        leader_raft_host="${leader_raft_host%]}"
        if [[ "${leader_api_host}" == "${leader_raft_host}" ]]; then
          printf 'api_leader_retry from=%s to=%s\n' "${api%/}" "${leader_api%/}" >&2
          current_url="${leader_api%/}${url#${api%/}}"
          continue
        fi
      fi
    fi
    break
  done
  if [[ ! "${http_status}" =~ ^2[0-9][0-9]$ ]]; then
    local failure_class=""
    if jq -e . "${body_file}" >/dev/null 2>&1; then
      failure_class="$(jq -r '.result.failure_class // .result.execution.failure_class // ""' "${body_file}")"
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
    rm -f "${body_file}" "${header_file}"
    if [[ "${failure_class}" == "stale_plan" ]]; then
      return 75
    fi
    return 22
  fi
  cat "${body_file}"
  rm -f "${body_file}" "${header_file}"
}

issue_approval() {
  local cluster_id="$1" target_id="$2" ordinal="$3" execute_attempt="$4" idempotency_key="$5"
  local payload response approval_token
  payload="$(jq -nc \
    --arg cluster_id "${cluster_id}" \
    --arg target_id "${target_id}" \
    --arg key "${idempotency_key}" \
    '{cluster_id:$cluster_id,engine:"mysql",operation_kind:"switchover",target_id:$target_id,issued_by:"cg-ha-matrix",ttl_seconds:300,idempotency_key:$key}')"
  if ! response="$(api_post "${api%/}/api/v1/approvals" "${payload}" admin)"; then
    echo "approval_issue_failed ordinal=${ordinal} cluster=${cluster_id} target=${target_id} attempt=${execute_attempt}" >&2
    return 1
  fi
  approval_token="$(jq -er '.result.approval_token | select(type == "string" and length > 0)' <<<"${response}")" || {
    echo "approval_issue_failed ordinal=${ordinal} cluster=${cluster_id} target=${target_id} attempt=${execute_attempt} reason=missing_token" >&2
    return 1
  }
  printf '%s\n' "${approval_token}"
}

wait_for_smoke() {
  local cluster_id="$1" ordinal="$2" operation_id="$3" target="$4"
  local attempt output_file failed_checks
  local -a smoke_command
  output_file="$(mktemp "${TMPDIR:-/tmp}/clusterguard-smoke.XXXXXX")" || return 1
  for ((attempt=1; attempt<=smoke_attempts; attempt++)); do
    if [[ "${insecure}" == true ]]; then
      smoke_command=("${script_dir}/clusterguard-smoke.sh" --api "${api}" --cluster "${cluster_id}" --token-env "${control_token_environment}" --insecure)
    else
      smoke_command=("${script_dir}/clusterguard-smoke.sh" --api "${api}" --cluster "${cluster_id}" --token-env "${control_token_environment}")
    fi
    if "${smoke_command[@]}" >"${output_file}" 2>&1; then
      rm -f "${output_file}"
      if ((attempt > 1)); then
        printf 'smoke_converged ordinal=%s cluster=%s operation=%s attempt=%s\n' "${ordinal}" "${cluster_id}" "${operation_id:-unknown}" "${attempt}"
      fi
      return 0
    fi
    if ((attempt < smoke_attempts && smoke_interval > 0)); then
      sleep "${smoke_interval}"
    fi
  done
  failed_checks="unknown"
  if jq -e . "${output_file}" >/dev/null 2>&1; then
    failed_checks="$(jq -r '[.checks[]? | select(.status != "pass") | .name] | unique | join(",") | if . == "" then "unknown" else . end' "${output_file}")"
  fi
  rm -f "${output_file}"
  echo "smoke_failed ordinal=${ordinal} cluster=${cluster_id} target=${target} operation=${operation_id:-unknown} attempts=${smoke_attempts} failed_checks=${failed_checks}" >&2
  return 1
}

execute_switch() {
  local cluster_id="$1" ordinal="$2" target_offset="$3"
  local candidates eligible_count target payload response operation_id api_status execute_attempt approval_token idempotency_key
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
  response=""
  for ((execute_attempt=1; execute_attempt<=3; execute_attempt++)); do
    idempotency_key="matrix-${cluster_id}-${ordinal}-${execute_attempt}-$(date +%s%N)"
    if ! approval_token="$(issue_approval "${cluster_id}" "${target}" "${ordinal}" "${execute_attempt}" "${idempotency_key}")"; then
      echo "switch_failed ordinal=${ordinal} cluster=${cluster_id} target=${target} stage=approval" >&2
      return 1
    fi
    payload="$(jq -nc --arg cluster_id "${cluster_id}" --arg target_id "${target}" --arg approval_token "${approval_token}" --arg key "${idempotency_key}" \
      '{operation:{cluster_id:$cluster_id,engine:"mysql",kind:"switchover",requested_by:"cg-ha-matrix"},target_id:$target_id,idempotency_key:$key,approval_token:$approval_token}')"
    if response="$(api_post "${api%/}/api/v1/operations/execute" "${payload}" grant)"; then
      break
    else
      api_status=$?
    fi
    if [[ "${api_status}" -eq 75 && "${execute_attempt}" -lt 3 ]]; then
      echo "switch_retry ordinal=${ordinal} cluster=${cluster_id} target=${target} attempt=${execute_attempt} reason=stale_plan" >&2
      sleep 1
      continue
    fi
    echo "switch_failed ordinal=${ordinal} cluster=${cluster_id} target=${target} stage=execute" >&2
    return 1
  done
  operation_id="$(jq -r '.result.resource_id // ""' <<<"${response}")"
  if ! jq -e '.status == "ok" and .result.status == "succeeded"' <<<"${response}" >/dev/null; then
    echo "switch_failed ordinal=${ordinal} cluster=${cluster_id} target=${target} operation=${operation_id:-unknown} stage=verify_response" >&2
    return 1
  fi
  if ! wait_for_smoke "${cluster_id}" "${ordinal}" "${operation_id}" "${target}"; then
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
