#!/bin/bash

# clusterguard-cluster-finalize.sh — boot-time recovery finalize for a cluster
# shut down by clusterguard-cluster-shutdown.sh.
#
# Runs on every cluster node (systemd oneshot) after clusterguard-cluster-restore:
#   F1. no topology snapshot -> not a planned-shutdown node, exit 0
#       snapshot already finalized (recovered_at) -> no-op, exit 0
#   F2. wait for the ClusterGuard API (up to 60s)
#   F3. poll the cluster topology (every 5s, up to 600s) until the
#       snapshot-designated primary reports healthy
#   F4. primary online -> walk the power lifecycle: power/verify
#       (recovering -> verifying), then power/complete, which re-runs the
#       recovery checks and releases the recovery freeze and every instance's
#       maintenance in one control-plane step (Go side), then stamp
#       recovered_at into the local snapshot
#   F5. poll timed out with the primary still offline (or complete never
#       succeeded) -> keep every protection layer, log CRITICAL and wait for
#       human intervention.
#
# Every API mutation here is idempotent, so all nodes running finalize
# concurrently is safe. Protection is never released while the primary is
# offline or the control-plane recovery checks fail.

set -Eeuo pipefail

script_directory="$(cd "$(dirname "$0")" && pwd)"
script_path="${script_directory}/$(basename "$0")"

if [[ -f /etc/clusterguard/clusterguard.env ]]; then
  # shellcheck disable=SC1091
  source /etc/clusterguard/clusterguard.env
fi
if [[ -f /etc/clusterguard/agent.env ]]; then
  # shellcheck disable=SC1091
  source /etc/clusterguard/agent.env
fi

snapshot_directory="${CLUSTER_SNAPSHOT_DIR:-/etc/clusterguard/power-snapshots}"
snapshot_path="${CLUSTER_SNAPSHOT_PATH:-}"
cg_api_url="${CLUSTERGUARD_API:-https://127.0.0.1:3000}"
cg_control_token="${CG_CONTROL_TOKEN:-}"
curl_command="${CURL_COMMAND:-curl}"
jq_command="${JQ_COMMAND:-jq}"
api_wait_timeout="${CLUSTER_FINALIZE_API_TIMEOUT:-60}"
poll_interval="${CLUSTER_FINALIZE_POLL_INTERVAL:-5}"
poll_timeout="${CLUSTER_FINALIZE_POLL_TIMEOUT:-600}"
report_attempts="${CLUSTER_FINALIZE_REPORT_ATTEMPTS:-5}"
report_retry_interval="${CLUSTER_FINALIZE_REPORT_RETRY_INTERVAL:-2}"

[[ "$report_attempts" =~ ^[1-9][0-9]*$ ]] || report_attempts=5
[[ "$report_retry_interval" =~ ^[0-9]+$ ]] || report_retry_interval=2

function fail {
  echo "clusterguard-cluster-finalize.sh: $1" >&2
  exit "${2:-1}"
}

# A database host may participate in more than one independently managed
# cluster. Finalize every per-cluster snapshot concurrently so one unhealthy
# cluster cannot prevent another healthy cluster from releasing protection.
if [[ -z "$snapshot_path" ]]; then
  shopt -s nullglob
  snapshots=("${snapshot_directory}"/*.json)
  legacy_snapshot="/etc/clusterguard/cluster-topology.json"
  if [[ -f "$legacy_snapshot" ]]; then
    snapshots+=("$legacy_snapshot")
  fi
  if [[ ${#snapshots[@]} -eq 0 ]]; then
    exit 0
  fi
  finalize_pids=()
  for candidate_snapshot in "${snapshots[@]}"; do
    CLUSTER_SNAPSHOT_PATH="$candidate_snapshot" "$script_path" &
    finalize_pids+=("$!")
  done
  finalize_rc=0
  for finalize_pid in "${finalize_pids[@]}"; do
    if ! wait "$finalize_pid"; then
      finalize_rc=1
    fi
  done
  exit "$finalize_rc"
fi

# F1: not a planned-shutdown node, or already finalized -> no-op.
if [[ ! -f "$snapshot_path" ]]; then
  exit 0
fi
if "$jq_command" -e '.recovered_at // empty' "$snapshot_path" >/dev/null 2>&1; then
  exit 0
fi
cluster_id="$("$jq_command" -r '.cluster_id // empty' "$snapshot_path" 2>/dev/null || true)"
engine="$("$jq_command" -r '.engine // .cluster.engine // "mysql"' "$snapshot_path" 2>/dev/null || true)"
primary_instance_id="$("$jq_command" -r '.primary.instance_id // .cluster.primary.instance_id // empty' "$snapshot_path" 2>/dev/null || true)"
if [[ -z "$cluster_id" || -z "$primary_instance_id" ]]; then
  exit 0
fi
case "$engine" in
  mysql|postgresql) ;;
  *) fail "automatic boot recovery is not qualified for engine ${engine:-unknown}" ;;
esac
[[ -n "$cg_control_token" ]] || fail "CG_CONTROL_TOKEN is required to finalize the planned power lifecycle"

echo "clusterguard-cluster-finalize.sh: recovery finalize for cluster ${cluster_id}"

# F2: wait for the platform API.
deadline=$((SECONDS + api_wait_timeout))
while :; do
  if "$curl_command" -sk --max-time 5 -o /dev/null -w '%{http_code}' "${cg_api_url}/healthz" 2>/dev/null | grep -q '^200$'; then
    break
  fi
  if [[ $SECONDS -ge $deadline ]]; then
    fail "ClusterGuard API did not become ready within ${api_wait_timeout}s (protection remains active)"
  fi
  sleep 2
done

function local_instance_id {
  # The instance in the snapshot whose host or ip matches this node, if any.
  local ip local_ips instance_id
  instance_id="$("$jq_command" -r '.local_instance_id // empty' "$snapshot_path" 2>/dev/null || true)"
  [[ -n "$instance_id" ]] && { printf '%s\n' "$instance_id"; return 0; }
  instance_id="$("$jq_command" -r \
    --arg h "$(hostname -s 2>/dev/null || true)" \
    --arg f "$(hostname -f 2>/dev/null || true)" \
    --arg n "$(hostname 2>/dev/null || true)" \
    '((.replicas // .cluster.replicas // []) + [(.primary // .cluster.primary)])[] | select(((.hostname // .host // "") == $h) or ((.hostname // .host // "") == $f) or ((.hostname // .host // "") == $n)) | .instance_id' \
    "$snapshot_path" 2>/dev/null | head -1 || true)"
  [[ -n "$instance_id" ]] && { printf '%s\n' "$instance_id"; return 0; }
  local_ips="$(hostname -I 2>/dev/null || true)"
  for ip in $local_ips; do
    instance_id="$("$jq_command" -r --arg ip "$ip" \
      '((.replicas // .cluster.replicas // []) + [(.primary // .cluster.primary)])[] | select((.ip_address // .ip // "") == $ip) | .instance_id' \
      "$snapshot_path" 2>/dev/null | head -1 || true)"
    [[ -n "$instance_id" ]] && { printf '%s\n' "$instance_id"; return 0; }
  done
  return 1
}

function fetch_primary_health {
  # Prints the current health state of the snapshot-designated primary
  # instance, or empty when the topology has no observation yet.
  local topology_json
  topology_json="$("$curl_command" -sk --fail --silent --show-error --max-time 15 \
    -H "Authorization: Bearer ${cg_control_token}" \
    "${cg_api_url}/api/v1/clusters/${cluster_id}/topology" 2>/dev/null || true)"
  [[ -n "$topology_json" ]] || return 1
  printf '%s' "$topology_json" | "$jq_command" -r \
    --arg id "$primary_instance_id" \
    '.result.instances[] | select(.resource_id == $id) | .health.state // empty' 2>/dev/null || true
}

function log_replica_states {
  # Informational: current health and lag of every non-primary instance.
  local topology_json host state lag
  topology_json="$("$curl_command" -sk --fail --silent --show-error --max-time 15 \
    -H "Authorization: Bearer ${cg_control_token}" \
    "${cg_api_url}/api/v1/clusters/${cluster_id}/topology" 2>/dev/null || true)"
  [[ -n "$topology_json" ]] || return 0
  while IFS=$'\t' read -r host state lag; do
    [[ -n "$host" ]] || continue
    echo "clusterguard-cluster-finalize.sh: replica ${host} health=${state} lag=${lag}s"
  done < <(printf '%s' "$topology_json" | "$jq_command" -r \
    '.result.instances[] | select(.role != "primary") | [.hostname, (.health.state // "unknown"), ((.replication.lag_seconds // -1) | tostring)] | @tsv' 2>/dev/null || true)
}

# F4: release the planned-shutdown protections through the power lifecycle.
# power/verify walks recovering -> verifying; power/complete re-runs the
# topology checks and, only when they pass, releases the recovery freeze and
# every instance's maintenance in one Go-side step and moves to completed.
# Every call is idempotent across concurrent nodes: 409 means another node
# already advanced the lifecycle or the checks still fail.
function power_verify {
  local code
  code="$(post_power_with_retry "verify" || true)"
  if [[ "$code" == "200" || "$code" == "409" ]]; then
    echo "clusterguard-cluster-finalize.sh: power/verify -> HTTP ${code} (verifying, or already advanced)"
    return 0
  fi
  echo "clusterguard-cluster-finalize.sh: power/verify -> HTTP ${code}" >&2
  return 1
}

function curl_command_post_power {
  local action
  action="$1"
  "$curl_command" -sk --silent --show-error --max-time 15 -o /dev/null -w '%{http_code}' -X POST \
    -H "Authorization: Bearer ${cg_control_token}" \
    -H 'Content-Type: application/json' -d '{}' \
    "${cg_api_url}/api/v1/clusters/${cluster_id}/power/${action}"
}

# Controller leader election and quorum reformation can briefly surface 5xx
# responses after all hosts reboot together. Keep those transient failures
# inside this boot attempt so systemd does not need to restart the whole
# finalize workflow. Authentication and validation errors remain fail-fast.
function post_power_with_retry {
  local action code attempt
  action="$1"
  for ((attempt = 1; attempt <= report_attempts; attempt++)); do
    code="$(curl_command_post_power "$action" 2>/dev/null || true)"
    code="${code:-000}"
    case "$code" in
      200|409)
        printf '%s\n' "$code"
        return 0
        ;;
      000|500|502|503|504)
        if ((attempt < report_attempts)); then
          echo "clusterguard-cluster-finalize.sh: transient HTTP ${code}; retrying power/${action} ($((attempt + 1))/${report_attempts})" >&2
          sleep "$report_retry_interval"
          continue
        fi
        ;;
    esac
    printf '%s\n' "$code"
    return 1
  done
  printf '%s\n' "${code:-000}"
  return 1
}

function latest_power_state {
  # Prints the state of the most recent power operation, or empty.
  local status_json attempt
  for ((attempt = 1; attempt <= report_attempts; attempt++)); do
    status_json="$("$curl_command" -sk --fail --silent --show-error --max-time 15 \
      -H "Authorization: Bearer ${cg_control_token}" \
      "${cg_api_url}/api/v1/clusters/${cluster_id}/power/status" 2>/dev/null || true)"
    if [[ -n "$status_json" ]]; then
      printf '%s' "$status_json" | "$jq_command" -r '.result.power_operation.state // empty' 2>/dev/null || true
      return 0
    fi
    if ((attempt < report_attempts)); then
      echo "clusterguard-cluster-finalize.sh: retrying power/status ($((attempt + 1))/${report_attempts})" >&2
      sleep "$report_retry_interval"
    fi
  done
  return 1
}

function power_complete {
  # Returns 0 when the lifecycle reached completed (protections released),
  # 1 when the checks still fail or the operation is stuck, 2 when there is
  # no active power operation at all.
  local code state
  code="$(post_power_with_retry "complete" || true)"
  case "$code" in
    200)
      echo "clusterguard-cluster-finalize.sh: power/complete -> HTTP 200 (protections released, lifecycle completed)"
      return 0
      ;;
    409)
      state="$(latest_power_state 2>/dev/null || true)"
      if [[ "$state" == "completed" ]]; then
        echo "clusterguard-cluster-finalize.sh: power/complete -> 409 but lifecycle already completed"
        return 0
      fi
      if [[ "$state" == "failed" || -z "$state" ]]; then
        echo "clusterguard-cluster-finalize.sh: power/complete -> lifecycle is ${state:-absent}; protections stay active" >&2
        return 2
      fi
      echo "clusterguard-cluster-finalize.sh: power/complete -> HTTP 409 (state ${state}; recovery checks still running)"
      return 1
      ;;
    *)
      echo "clusterguard-cluster-finalize.sh: power/complete -> HTTP ${code}" >&2
      return 1
      ;;
  esac
}

function stamp_recovered_at {
  local ts tmp
  ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  tmp="${snapshot_path}.tmp.$$"
  "$jq_command" --arg ts "$ts" '. + {recovered_at: $ts}' "$snapshot_path" > "$tmp"
  chmod 600 "$tmp"
  mv "$tmp" "$snapshot_path"
}

# F3: poll until the snapshot-designated primary reports healthy.
primary_healthy=false
deadline=$((SECONDS + poll_timeout))
while :; do
  if [[ "$(fetch_primary_health)" == "healthy" ]]; then
    primary_healthy=true
    break
  fi
  if [[ $SECONDS -ge $deadline ]]; then
    break
  fi
  sleep "$poll_interval"
done

if [[ "$primary_healthy" == "true" ]]; then
  log_replica_states
  # F4: walk the power lifecycle to completion. verify moves to verifying
  # (409 = another node did it); complete then releases the protections in
  # Go only when the recovery checks pass.
  if ! power_verify; then
    fail "power/verify failed while the primary is healthy"
  fi
  complete_ok=false
  deadline=$((SECONDS + poll_timeout))
  while :; do
    if power_complete; then
      complete_rc=0
    else
      complete_rc=$?
    fi
    # 0 = completed (protections released), 2 = lifecycle failed/absent
    # (fail-closed: give up waiting), 1 = checks still running, keep polling.
    if [[ $complete_rc -eq 0 ]]; then
      complete_ok=true
      break
    fi
    if [[ $complete_rc -eq 2 ]]; then
      break
    fi
    if [[ $SECONDS -ge $deadline ]]; then
      break
    fi
    sleep "$poll_interval"
  done
  if [[ "$complete_ok" == "true" ]]; then
    stamp_recovered_at
    echo "clusterguard-cluster-finalize.sh: recovery finalized for cluster ${cluster_id}"
    exit 0
  fi
fi

# F5 timeout path: the primary never reported healthy, or the power lifecycle
# never reached completed. Fail-closed: every protection layer from the
# shutdown stays active and a human must decide.
echo "clusterguard-cluster-finalize.sh: CRITICAL — primary of cluster ${cluster_id} did not recover within ${poll_timeout}s" >&2
echo "clusterguard-cluster-finalize.sh: automatic recovery remains FROZEN and maintenance remains ACTIVE (fail-closed)" >&2
echo "clusterguard-cluster-finalize.sh: manual steps:" >&2
echo "  - fix the primary node; after it restarts, restore/finalize resume automatically" >&2
echo "  - or, only after explicit operator review, unfreeze manually:" >&2
echo "    curl -sk -X POST -H \"Authorization: Bearer \${CG_CONTROL_TOKEN}\" -H 'Content-Type: application/json'" >&2
echo "      -d '{\"freeze\":false}' ${cg_api_url}/api/v1/clusters/${cluster_id}/recovery-freeze" >&2
echo "clusterguard-cluster-finalize.sh: protection intentionally left active" >&2
exit 0
