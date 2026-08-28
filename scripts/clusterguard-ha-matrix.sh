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
candidate_attempts="${CG_MATRIX_CANDIDATE_ATTEMPTS:-30}"
candidate_interval="${CG_MATRIX_CANDIDATE_INTERVAL:-2}"
stable_observations="${CG_MATRIX_STABLE_OBSERVATIONS:-3}"
control_token_environment="CG_CONTROL_TOKEN"
platform_session=false
platform_username_environment="CG_PLATFORM_USERNAME"
platform_password_environment="CG_PLATFORM_PASSWORD"
platform_new_password_environment="CG_PLATFORM_NEW_PASSWORD"
platform_cookie_jar=""
platform_username=""
platform_password=""
script_dir="${script_dir:-$(cd "$(dirname "$0")" && pwd)}"
insecure=false
total=0
passed=0
failed=0

finish() {
  local status=$?
  trap - EXIT
  if [[ -n "${platform_cookie_jar}" ]]; then
    rm -f "${platform_cookie_jar}"
  fi
  printf 'matrix_summary total=%s passed=%s failed=%s seed=%s\n' "${total}" "${passed}" "${failed}" "${matrix_seed}"
  exit "${status}"
}
trap finish EXIT

usage() {
  echo "usage: $0 --clusters UUID[,UUID...] [--api URL] [--round-robin N] [--random N] [--seed N] [--parallel N] [--smoke-attempts N] [--smoke-interval SECONDS] [--candidate-attempts N] [--candidate-interval SECONDS] [--stable-observations N] [--api-timeout SECONDS] [--platform-session]"
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
    --candidate-attempts) candidate_attempts="${2:-}"; shift 2 ;;
    --candidate-interval) candidate_interval="${2:-}"; shift 2 ;;
    --stable-observations) stable_observations="${2:-}"; shift 2 ;;
    --api-timeout) api_timeout="${2:-}"; shift 2 ;;
    --token-env) control_token_environment="${2:-}"; shift 2 ;;
    --platform-session) platform_session=true; shift ;;
    --platform-user-env) platform_username_environment="${2:-}"; shift 2 ;;
    --platform-password-env) platform_password_environment="${2:-}"; shift 2 ;;
    --platform-new-password-env) platform_new_password_environment="${2:-}"; shift 2 ;;
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
[[ "${candidate_attempts}" =~ ^[1-9][0-9]*$ ]] || { echo "candidate attempts must be a positive integer" >&2; exit 2; }
[[ "${candidate_interval}" =~ ^[0-9]+$ ]] || { echo "candidate interval must be a non-negative integer" >&2; exit 2; }
[[ "${stable_observations}" =~ ^[1-9][0-9]*$ ]] || { echo "stable observations must be a positive integer" >&2; exit 2; }
((stable_observations <= candidate_attempts)) || { echo "stable observations cannot exceed candidate attempts" >&2; exit 2; }

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
if [[ "${platform_session}" == true ]]; then
  platform_username="${!platform_username_environment:-}"
  platform_password="${!platform_password_environment:-}"
  [[ -n "${platform_username}" ]] || platform_username="admin"
  [[ -n "${platform_password}" ]] || { echo "platform password environment is required for --platform-session" >&2; exit 2; }
  platform_cookie_jar="$(mktemp "${TMPDIR:-/tmp}/clusterguard-platform-session.XXXXXX")"
else
  [[ -n "${control_token}" ]] || { echo "control token environment is required" >&2; exit 2; }
fi
RANDOM=$((matrix_seed % 32768))

api_get() {
  local url="$1" attempt csrf_token session_token
  local -a command
  for ((attempt=1; attempt<=read_attempts; attempt++)); do
    command=(curl --fail --silent --show-error --max-time 15 -H 'Accept: application/json')
    if [[ "${insecure}" == true ]]; then
      command+=(-k)
    fi
    if [[ "${platform_session}" == true ]]; then
      session_token="$(awk '$6 == "clusterguard_session" { value=$7 } END { print value }' "${platform_cookie_jar}")"
      csrf_token="$(awk '$6 == "clusterguard_csrf" { value=$7 } END { print value }' "${platform_cookie_jar}")"
      [[ -n "${session_token}" && -n "${csrf_token}" ]] || { echo "platform session cookies are unavailable" >&2; return 1; }
      command+=(-H "Cookie: clusterguard_session=${session_token}; clusterguard_csrf=${csrf_token}")
    else
      command+=(-H "Authorization: Bearer ${control_token}")
    fi
    command+=("${url}")
    if "${command[@]}"; then
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
  local csrf_token session_token
  local -a command=(curl --silent --show-error --max-time "${api_timeout}" --output "${body_file}" --dump-header "${header_file}" --write-out '%{http_code}')
  if [[ "${insecure}" == true ]]; then
    command+=(-k)
  fi
  command+=(-H 'Content-Type: application/json')
  case "${authorization_mode}" in
    admin)
      command+=(-H "Authorization: Bearer ${control_token}")
      ;;
    login)
      command+=(-c "${platform_cookie_jar}")
      ;;
    session)
      session_token="$(awk '$6 == "clusterguard_session" { value=$7 } END { print value }' "${platform_cookie_jar}")"
      csrf_token="$(awk '$6 == "clusterguard_csrf" { value=$7 } END { print value }' "${platform_cookie_jar}")"
      [[ -n "${session_token}" && -n "${csrf_token}" ]] || { echo "platform session cookies are unavailable" >&2; return 1; }
      command+=(-H "Cookie: clusterguard_session=${session_token}; clusterguard_csrf=${csrf_token}" -H "X-CSRF-Token: ${csrf_token}")
      ;;
    grant)
      ;;
    *)
      echo "unknown authorization_mode: ${authorization_mode}" >&2
      return 2
      ;;
  esac
  command+=(-d "${payload}" "${url}")
  "${command[@]}"
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
    local failure_class="" blocking_check_count=0
    if jq -e . "${body_file}" >/dev/null 2>&1; then
      failure_class="$(jq -r '.result.failure_class // .result.execution.failure_class // ""' "${body_file}")"
      blocking_check_count="$(jq -r '[
        .result.precheck[]?,
        .result.plan.checks[]?,
        .result.verification.checks[]?
        | select(.status == "fail")
      ] | length' "${body_file}")"
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
            $root.result.precheck[]?,
            $root.result.plan.checks[]?,
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
    if [[ "${http_status}" == "409" && "${blocking_check_count}" =~ ^[1-9][0-9]*$ ]]; then
      return 76
    fi
    return 22
  fi
  cat "${body_file}"
  rm -f "${body_file}" "${header_file}"
}

issue_approval() {
  local cluster_id="$1" target_id="$2" ordinal="$3" execute_attempt="$4" idempotency_key="$5"
  local payload response approval_token api_status
  payload="$(jq -nc \
    --arg cluster_id "${cluster_id}" \
    --arg target_id "${target_id}" \
    --arg key "${idempotency_key}" \
    '{cluster_id:$cluster_id,engine:"mysql",operation_kind:"switchover",target_id:$target_id,issued_by:"cg-ha-matrix",ttl_seconds:300,idempotency_key:$key}')"
  if response="$(api_post "${api%/}/api/v1/approvals" "${payload}" admin)"; then
    :
  else
    api_status=$?
    echo "approval_issue_failed ordinal=${ordinal} cluster=${cluster_id} target=${target_id} attempt=${execute_attempt}" >&2
    return "${api_status}"
  fi
  approval_token="$(jq -er '.result.approval_token | select(type == "string" and length > 0)' <<<"${response}")" || {
    echo "approval_issue_failed ordinal=${ordinal} cluster=${cluster_id} target=${target_id} attempt=${execute_attempt} reason=missing_token" >&2
    return 1
  }
  printf '%s\n' "${approval_token}"
}

wait_for_stable_candidate() {
  local cluster_id="$1" ordinal="$2" target_offset="$3"
  local response observation_id eligible_count blocking_count target failed_checks reason attempt
  local last_target="" last_observation="" stable_count=0
  for ((attempt=1; attempt<=candidate_attempts; attempt++)); do
    reason="candidate_read_failed"
    failed_checks="none"
    if response="$(api_get "${api%/}/api/v1/clusters/${cluster_id}/candidates")"; then
      observation_id="$(jq -r '.observation_id // ""' <<<"${response}")"
      eligible_count="$(jq -r '[.result[] | select(.eligible == true)] | length' <<<"${response}")"
      blocking_count="$(jq -r '[
        .result[]
        | select(.eligible != true)
        | select(([.checks[]? | select(.name == "candidate_role" and .status == "fail")] | length) == 0)
      ] | length' <<<"${response}")"
      failed_checks="$(jq -r '[
        .result[]
        | select(.eligible != true)
        | select(([.checks[]? | select(.name == "candidate_role" and .status == "fail")] | length) == 0)
        | .checks[]?
        | select(.status == "fail")
        | .name
      ] | unique | join(",") | if . == "" then "none" else . end' <<<"${response}")"
      target="$(jq -r --argjson offset "${target_offset}" \
        '[.result[] | select(.eligible == true)]
        | sort_by(.rank)
        | if length == 0 then "" else .[$offset % length].instance_id // "" end' <<<"${response}")"
      if [[ -z "${observation_id}" && "${stable_observations}" -eq 1 ]]; then
        observation_id="single-read-${attempt}"
      fi
      if [[ -z "${observation_id}" ]]; then
        reason="missing_observation_id"
      elif [[ ! "${eligible_count}" =~ ^[1-9][0-9]*$ ]]; then
        reason="no_eligible_candidate"
      elif [[ ! "${blocking_count}" =~ ^0$ ]]; then
        reason="follower_not_ready"
      elif [[ -z "${target}" ]]; then
        reason="empty_target"
      else
        reason=""
        if [[ "${target}" != "${last_target}" ]]; then
          stable_count=0
          last_observation=""
        fi
        if [[ "${observation_id}" != "${last_observation}" ]]; then
          stable_count=$((stable_count + 1))
          last_observation="${observation_id}"
        fi
        last_target="${target}"
        if ((stable_count >= stable_observations)); then
          echo "candidate_stable ordinal=${ordinal} cluster=${cluster_id} target=${target} observations=${stable_count} observation_id=${observation_id}" >&2
          printf '%s\n' "${target}"
          return 0
        fi
        reason="stability_window"
      fi
    fi
    if [[ "${reason}" != "stability_window" ]]; then
      stable_count=0
      last_target=""
      last_observation=""
    fi
    echo "candidate_wait ordinal=${ordinal} cluster=${cluster_id} attempt=${attempt}/${candidate_attempts} stable=${stable_count}/${stable_observations} reason=${reason} failed_checks=${failed_checks}" >&2
    if ((attempt < candidate_attempts && candidate_interval > 0)); then
      sleep "${candidate_interval}"
    fi
  done
  echo "candidate_stability_failed ordinal=${ordinal} cluster=${cluster_id} attempts=${candidate_attempts} stable=${stable_count}/${stable_observations} reason=${reason} failed_checks=${failed_checks}" >&2
  return 1
}

platform_login() {
  local password="$1" payload response must_change new_password
  payload="$(jq -nc --arg username "${platform_username}" --arg password "${password}" '{username:$username,password:$password}')"
  if ! response="$(api_post "${api%/}/api/v1/auth/login" "${payload}" login)"; then
    echo "platform_login_failed" >&2
    return 1
  fi
  must_change="$(jq -r '.result.user.must_change_password // false' <<<"${response}")"
  if [[ "${must_change}" == "true" ]]; then
    new_password="${!platform_new_password_environment:-}"
    [[ -n "${new_password}" ]] || { echo "platform new password environment is required for bootstrap password change" >&2; return 1; }
    payload="$(jq -nc --arg current_password "${password}" --arg new_password "${new_password}" '{current_password:$current_password,new_password:$new_password}')"
    if ! api_post "${api%/}/api/v1/auth/password" "${payload}" session >/dev/null; then
      echo "platform_password_change_failed" >&2
      return 1
    fi
    platform_password="${new_password}"
    payload="$(jq -nc --arg username "${platform_username}" --arg password "${platform_password}" '{username:$username,password:$password}')"
    if ! response="$(api_post "${api%/}/api/v1/auth/login" "${payload}" login)"; then
      echo "platform_relogin_failed" >&2
      return 1
    fi
  fi
  if ! jq -e '.status == "ok" and .result.user.must_change_password == false' <<<"${response}" >/dev/null; then
    echo "platform_session_not_ready" >&2
    return 1
  fi
  echo "platform_session_ready user=${platform_username}" >&2
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
  local target payload response operation_id api_status execute_attempt approval_token approval_status idempotency_key requested_by authorization_mode retry_reason
  response=""
  for ((execute_attempt=1; execute_attempt<=3; execute_attempt++)); do
    if ! target="$(wait_for_stable_candidate "${cluster_id}" "${ordinal}" "${target_offset}")"; then
      echo "switch_failed ordinal=${ordinal} cluster=${cluster_id} stage=candidate_stability" >&2
      return 1
    fi
    idempotency_key="matrix-${cluster_id}-${ordinal}-${execute_attempt}-$(date +%s%N)"
    if [[ "${platform_session}" == true ]]; then
      requested_by="${platform_username}"
      authorization_mode="session"
      payload="$(jq -nc --arg cluster_id "${cluster_id}" --arg target_id "${target}" --arg requested_by "${requested_by}" --arg key "${idempotency_key}" \
        '{operation:{cluster_id:$cluster_id,engine:"mysql",kind:"switchover",requested_by:$requested_by},target_id:$target_id,idempotency_key:$key}')"
    else
      if approval_token="$(issue_approval "${cluster_id}" "${target}" "${ordinal}" "${execute_attempt}" "${idempotency_key}")"; then
        :
      else
        approval_status=$?
        if [[ "${approval_status}" -eq 76 && "${execute_attempt}" -lt 3 ]]; then
          echo "switch_retry ordinal=${ordinal} cluster=${cluster_id} target=${target} attempt=${execute_attempt} reason=blocking_precheck" >&2
          continue
        fi
        echo "switch_failed ordinal=${ordinal} cluster=${cluster_id} target=${target} stage=approval" >&2
        return 1
      fi
      authorization_mode="admin"
      payload="$(jq -nc --arg cluster_id "${cluster_id}" --arg target_id "${target}" --arg approval_token "${approval_token}" --arg key "${idempotency_key}" \
        '{operation:{cluster_id:$cluster_id,engine:"mysql",kind:"switchover",requested_by:"cg-ha-matrix"},target_id:$target_id,idempotency_key:$key,approval_token:$approval_token}')"
    fi
    if response="$(api_post "${api%/}/api/v1/operations/execute" "${payload}" "${authorization_mode}")"; then
      break
    else
      api_status=$?
    fi
    if (((api_status == 75 || api_status == 76) && execute_attempt < 3)); then
      retry_reason="stale_plan"
      if [[ "${api_status}" -eq 76 ]]; then
        retry_reason="blocking_precheck"
      fi
      echo "switch_retry ordinal=${ordinal} cluster=${cluster_id} target=${target} attempt=${execute_attempt} reason=${retry_reason}" >&2
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

if [[ "${platform_session}" == true ]]; then
  platform_login "${platform_password}"
fi

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
