#!/bin/bash

# clusterguard-cluster-restore.sh — boot-time restore bootstrap for a cluster
# shut down by clusterguard-cluster-shutdown.sh.
#
# Runs on every cluster node (systemd oneshot) after boot:
#   B1. no topology snapshot -> this is not a planned-shutdown node, exit 0
#   B2. wait for local MySQL to be active (up to CLUSTER_RESTORE_MYSQL_TIMEOUT)
#   B3. if this node is the snapshot-designated primary: clear the persisted
#       read-only state hardened at shutdown and re-enable write access
#   B4. advance the power lifecycle (power/boot-detected -> power/recovering,
#       idempotent: 409 is tolerated) and trigger a platform topology
#       discovery so the control plane observes the booted topology
#
# This script only prepares the node. Clearing maintenance and unfreezing
# automatic recovery is the job of clusterguard-cluster-finalize.sh, which runs
# only after the platform reports the cluster healthy again.
#
# Failures exit non-zero so systemd (Restart=on-failure) retries; protection
# from the shutdown is never touched here.

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
systemctl_command="${SYSTEMCTL_COMMAND:-systemctl}"
mysql_command="${MYSQL_COMMAND:-mysql}"
mysqladmin_command="${MYSQLADMIN_COMMAND:-mysqladmin}"
api_wait_timeout="${CLUSTER_RESTORE_API_TIMEOUT:-120}"
mysql_wait_timeout="${CLUSTER_RESTORE_MYSQL_TIMEOUT:-120}"
report_attempts="${CLUSTER_RESTORE_REPORT_ATTEMPTS:-5}"
report_retry_interval="${CLUSTER_RESTORE_REPORT_RETRY_INTERVAL:-2}"
state_wait_timeout="${CLUSTER_RESTORE_STATE_TIMEOUT:-60}"
state_poll_interval="${CLUSTER_RESTORE_STATE_POLL_INTERVAL:-2}"
mysql_port="${CLUSTER_MYSQL_PORT:-3306}"
mysql_root_password="${CG_NODE_MYSQL_ROOT_PASSWORD:-}"
mysql_defaults_file="${CG_NODE_MYSQL_DEFAULTS_FILE:-}"

[[ "$report_attempts" =~ ^[1-9][0-9]*$ ]] || report_attempts=5
[[ "$report_retry_interval" =~ ^[0-9]+$ ]] || report_retry_interval=2
[[ "$state_wait_timeout" =~ ^[1-9][0-9]*$ ]] || state_wait_timeout=60
[[ "$state_poll_interval" =~ ^[0-9]+$ ]] || state_poll_interval=2

function fail {
  echo "clusterguard-cluster-restore.sh: $1" >&2
  exit "${2:-1}"
}

# One database host can carry multiple independently managed clusters. Process
# one snapshot per cluster instead of allowing the last shutdown to overwrite
# a single global file. CLUSTER_SNAPSHOT_PATH is set only for the recursive
# single-snapshot invocation and remains available for legacy recovery tests.
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
  restore_rc=0
  for candidate_snapshot in "${snapshots[@]}"; do
    if ! CLUSTER_SNAPSHOT_PATH="$candidate_snapshot" "$script_path"; then
      restore_rc=1
    fi
  done
  exit "$restore_rc"
fi

# B1: not a planned-shutdown node -> no-op.
if [[ ! -f "$snapshot_path" ]]; then
  exit 0
fi
cluster_id="$("$jq_command" -r '.cluster_id // empty' "$snapshot_path" 2>/dev/null || true)"
[[ -n "$cluster_id" ]] || exit 0
engine="$("$jq_command" -r '.engine // .cluster.engine // "mysql"' "$snapshot_path" 2>/dev/null || true)"
case "$engine" in
  mysql|postgresql) ;;
  *) fail "automatic boot recovery is not qualified for engine ${engine:-unknown}" ;;
esac
[[ -n "$cg_control_token" ]] || fail "CG_CONTROL_TOKEN is required to validate the planned power lifecycle"

echo "clusterguard-cluster-restore.sh: restore bootstrap for cluster ${cluster_id}"

# A snapshot alone is never authority to change a database role. Wait for the
# control plane, then require a live planned-shutdown state. This rejects a
# stale copied snapshot during an ordinary reboot before any service or
# read-only setting is changed.
deadline=$((SECONDS + api_wait_timeout))
while :; do
  if "$curl_command" -sk --max-time 5 -o /dev/null -w '%{http_code}' "${cg_api_url}/healthz" 2>/dev/null | grep -q '^200$'; then
    break
  fi
  if [[ $SECONDS -ge $deadline ]]; then
    fail "ClusterGuard API did not become ready within ${api_wait_timeout}s; stale-snapshot protection remains active"
  fi
  sleep 2
done

function fetch_power_state {
  local response code body state
  response="$("$curl_command" -sk --silent --show-error --max-time 15 \
    -H "Authorization: Bearer ${cg_control_token}" \
    -w $'\n%{http_code}' \
    "${cg_api_url}/api/v1/clusters/${cluster_id}/power/status" 2>/dev/null || true)"
  code="${response##*$'\n'}"
  body="${response%$'\n'*}"
  code="${code:-000}"
  if [[ "$code" == "200" ]]; then
    state="$(printf '%s' "$body" | "$jq_command" -r '.result.power_operation.state // "absent"' 2>/dev/null || true)"
    printf '%s\n' "${state:-absent}"
    return 0
  fi
  case "$code" in
    000|500|502|503|504) printf 'transient:%s\n' "$code" ;;
    *) printf 'error:%s\n' "$code" ;;
  esac
  return 0
}

# A follower can become HTTP-ready a few seconds before it has applied the
# leader's final power_off entry. Wait without mutating anything while that
# replicated state catches up. This is not a permission bypass: absent,
# completed, and non-transient invalid states retain their fail-closed paths.
deadline=$((SECONDS + state_wait_timeout))
while :; do
  power_state="$(fetch_power_state)"
  case "$power_state" in
    power_off|boot_detected|recovering|verifying)
      echo "clusterguard-cluster-restore.sh: control plane confirms recovery state ${power_state}"
      break
      ;;
    completed)
      echo "clusterguard-cluster-restore.sh: lifecycle already completed; stale snapshot will not change local role state"
      exit 0
      ;;
    prechecking|shutdown_planned|shutting_down|transient:*)
      if [[ $SECONDS -ge $deadline ]]; then
        fail "replicated planned recovery did not converge within ${state_wait_timeout}s (power state ${power_state}); refusing role changes"
      fi
      echo "clusterguard-cluster-restore.sh: waiting for replicated lifecycle state (currently ${power_state})"
      sleep "$state_poll_interval"
      ;;
    absent)
      fail "snapshot is not backed by an active planned recovery (power state absent); refusing role changes"
      ;;
    *)
      fail "snapshot is not backed by an active planned recovery (power state ${power_state}); refusing role changes"
      ;;
  esac
done

function local_snapshot_reference {
  local local_id reference local_host local_fqdn local_name local_ips ip
  local_id="$("$jq_command" -r '.local_instance_id // empty' "$snapshot_path" 2>/dev/null || true)"
  if [[ -n "$local_id" ]]; then
    reference="$("$jq_command" -c --arg id "$local_id" \
      '((.replicas // .cluster.replicas // []) + [(.primary // .cluster.primary)])[] | select((.instance_id // "") == $id)' \
      "$snapshot_path" 2>/dev/null | head -1 || true)"
    [[ -n "$reference" ]] && { printf '%s\n' "$reference"; return 0; }
  fi
  local_host="$(hostname -s 2>/dev/null || true)"
  local_fqdn="$(hostname -f 2>/dev/null || true)"
  local_name="$(hostname 2>/dev/null || true)"
  reference="$("$jq_command" -c --arg h "$local_host" --arg f "$local_fqdn" --arg n "$local_name" \
    '((.replicas // .cluster.replicas // []) + [(.primary // .cluster.primary)])[] | select(((.hostname // .host // "") == $h) or ((.hostname // .host // "") == $f) or ((.hostname // .host // "") == $n))' \
    "$snapshot_path" 2>/dev/null | head -1 || true)"
  [[ -n "$reference" ]] && { printf '%s\n' "$reference"; return 0; }
  local_ips="$(hostname -I 2>/dev/null || true)"
  for ip in $local_ips; do
    reference="$("$jq_command" -c --arg ip "$ip" \
      '((.replicas // .cluster.replicas // []) + [(.primary // .cluster.primary)])[] | select((.ip_address // .ip // "") == $ip)' \
      "$snapshot_path" 2>/dev/null | head -1 || true)"
    [[ -n "$reference" ]] && { printf '%s\n' "$reference"; return 0; }
  done
  return 1
}

local_reference="$(local_snapshot_reference || true)"
[[ -n "$local_reference" ]] || fail "snapshot does not contain this local Agent instance"
mysql_port="$(printf '%s' "$local_reference" | "$jq_command" -r --arg fallback "$mysql_port" '.port // ($fallback | tonumber)')"
mysql_service="$("$jq_command" -r '.local_service_name // empty' "$snapshot_path" 2>/dev/null || true)"
mysql_service="${mysql_service:-${CLUSTER_MYSQL_SERVICE:-mysqld}}"

echo "clusterguard-cluster-restore.sh: ensuring ${mysql_service} is started for local port ${mysql_port}"
"$systemctl_command" start "$mysql_service"

# B2: wait for the local database service and engine endpoint.
deadline=$((SECONDS + mysql_wait_timeout))
while :; do
  if [[ "$engine" == "postgresql" ]]; then
    if "$systemctl_command" is-active --quiet "$mysql_service"; then
      break
    fi
  else
    mysqladmin_args=(--no-defaults --protocol=tcp --host=127.0.0.1 --port="${mysql_port}" -uroot ping)
    if [[ -n "$mysql_defaults_file" && -f "$mysql_defaults_file" ]]; then
      mysqladmin_args=(--defaults-file="$mysql_defaults_file" --protocol=tcp --host=127.0.0.1 --port="${mysql_port}" ping)
    fi
    if [[ -n "$mysql_root_password" ]]; then
      MYSQL_PWD="$mysql_root_password" "$mysqladmin_command" "${mysqladmin_args[@]}" >/dev/null 2>&1 && break
    elif "$mysqladmin_command" "${mysqladmin_args[@]}" >/dev/null 2>&1; then
      break
    fi
  fi
  if [[ $SECONDS -ge $deadline ]]; then
    fail "local MySQL did not become ready within ${mysql_wait_timeout}s"
  fi
  sleep 2
done

function local_is_primary {
  local primary_id local_id primary_host primary_ip local_ips h
  primary_id="$("$jq_command" -r '.primary.instance_id // .cluster.primary.instance_id // ""' "$snapshot_path")"
  local_id="$("$jq_command" -r '.local_instance_id // ""' "$snapshot_path")"
  [[ -n "$local_id" && "$local_id" == "$primary_id" ]] && return 0
  primary_host="$("$jq_command" -r '.primary.hostname // .cluster.primary.host // ""' "$snapshot_path")"
  primary_ip="$("$jq_command" -r '.primary.ip_address // .cluster.primary.ip // ""' "$snapshot_path")"
  for h in "$(hostname -s 2>/dev/null || true)" "$(hostname -f 2>/dev/null || true)" "$(hostname 2>/dev/null || true)"; do
    [[ -n "$h" && "$h" == "$primary_host" ]] && return 0
  done
  if [[ -n "$primary_ip" ]]; then
    local_ips="$(hostname -I 2>/dev/null || true)"
    for h in $local_ips; do
      [[ "$h" == "$primary_ip" ]] && return 0
    done
  fi
  return 1
}

# B3: the snapshot-designated primary re-enables write access; every other
# node stays read-only (the platform enforces role-based read-only state).
if [[ "$engine" == "mysql" ]] && local_is_primary; then
  echo "clusterguard-cluster-restore.sh: this node is the snapshot primary — re-enabling write access"
  if [[ -z "$mysql_root_password" && ( -z "$mysql_defaults_file" || ! -f "$mysql_defaults_file" ) ]]; then
    fail "CG_NODE_MYSQL_ROOT_PASSWORD or CG_NODE_MYSQL_DEFAULTS_FILE is required to clear persisted read-only state"
  fi
  mysql_args=(--no-defaults --protocol=tcp --host=127.0.0.1 --port="${mysql_port}" -uroot --batch --skip-column-names)
  if [[ -n "$mysql_defaults_file" && -f "$mysql_defaults_file" ]]; then
    mysql_args=(--defaults-file="$mysql_defaults_file" --protocol=tcp --host=127.0.0.1 --port="${mysql_port}" --batch --skip-column-names)
  fi
  function execute_mysql_restore_sql {
    local statement="$1"
    if [[ -n "$mysql_root_password" ]]; then
      MYSQL_PWD="$mysql_root_password" "$mysql_command" "${mysql_args[@]}" --execute "$statement"
    else
      "$mysql_command" "${mysql_args[@]}" --execute "$statement"
    fi
  }
  execute_mysql_restore_sql "SET GLOBAL super_read_only = OFF; SET GLOBAL read_only = OFF"
  mysql_version="$(execute_mysql_restore_sql "SELECT VERSION()" | head -1)"
  mysql_major="${mysql_version%%.*}"
  if [[ "$mysql_major" =~ ^[0-9]+$ ]] && ((mysql_major >= 8)); then
    execute_mysql_restore_sql "SET PERSIST_ONLY super_read_only = OFF; SET PERSIST_ONLY read_only = OFF"
  elif [[ "$mysql_major" =~ ^[0-9]+$ ]] && ((mysql_major < 8)); then
    echo "clusterguard-cluster-restore.sh: MySQL ${mysql_version} has no PERSIST_ONLY; runtime write access restored"
  else
    fail "cannot determine MySQL major version from ${mysql_version:-empty}; persisted read-only state was not cleared"
  fi
elif [[ "$engine" == "mysql" ]]; then
  echo "clusterguard-cluster-restore.sh: this node is not the snapshot primary — leaving read-only state intact"
else
  echo "clusterguard-cluster-restore.sh: PostgreSQL preserves its primary/standby role on disk; no role mutation is required"
fi

# B4: report boot to the platform so the power lifecycle advances
# power_off -> boot_detected -> recovering, and refresh the topology
# observation. Every call is safe from any node and idempotent: a 409 means
# another node already advanced the lifecycle (or it is not in power_off and
# there is nothing to recover), which is not an error. Leader election and
# quorum reformation can briefly return 5xx while all controllers boot; retry
# those responses here before asking systemd to restart the whole unit.
function curl_command_post_boot {
  local action
  action="$1"
  "$curl_command" -sk --silent --show-error --max-time 15 -o /dev/null -w '%{http_code}' -X POST \
    -H "Authorization: Bearer ${cg_control_token}" \
    -H 'Content-Type: application/json' -d '{}' \
    "${cg_api_url}/api/v1/clusters/${cluster_id}/power/${action}"
}

function report_boot {
  local action expected code attempt
  action="$1"
  expected="$2"
  for ((attempt = 1; attempt <= report_attempts; attempt++)); do
    code="$(curl_command_post_boot "$action" 2>/dev/null || true)"
    code="${code:-000}"
    if [[ "$code" == "200" || "$code" == "409" ]]; then
      echo "clusterguard-cluster-restore.sh: power/${action} -> HTTP ${code} (${expected})"
      return 0
    fi
    case "$code" in
      000|500|502|503|504)
        if ((attempt < report_attempts)); then
          echo "clusterguard-cluster-restore.sh: transient HTTP ${code}; retrying power/${action} ($((attempt + 1))/${report_attempts})" >&2
          sleep "$report_retry_interval"
          continue
        fi
        ;;
    esac
    echo "clusterguard-cluster-restore.sh: power/${action} -> HTTP ${code}" >&2
    return 1
  done
  return 1
}

function report_discovery {
  local code attempt
  for ((attempt = 1; attempt <= report_attempts; attempt++)); do
    code="$("$curl_command" -sk --silent --show-error --max-time 15 -o /dev/null -w '%{http_code}' -X POST \
      -H "Authorization: Bearer ${cg_control_token}" \
      -H 'Content-Type: application/json' -d '{}' \
      "${cg_api_url}/api/v1/clusters/${cluster_id}/discover" 2>/dev/null || true)"
    code="${code:-000}"
    if [[ "$code" == "200" || "$code" == "409" ]]; then
      echo "clusterguard-cluster-restore.sh: topology discovery -> HTTP ${code}"
      return 0
    fi
    case "$code" in
      000|500|502|503|504)
        if ((attempt < report_attempts)); then
          echo "clusterguard-cluster-restore.sh: transient HTTP ${code}; retrying topology discovery ($((attempt + 1))/${report_attempts})" >&2
          sleep "$report_retry_interval"
          continue
        fi
        ;;
    esac
    echo "clusterguard-cluster-restore.sh: topology discovery -> HTTP ${code}" >&2
    return 1
  done
  return 1
}

report_boot "boot-detected" "lifecycle advanced to boot_detected, or already reported"
report_boot "recovering" "lifecycle advanced to recovering, or already recovering"
echo "clusterguard-cluster-restore.sh: triggering topology discovery for ${cluster_id}"
report_discovery

echo "clusterguard-cluster-restore.sh: restore bootstrap complete (cluster ${cluster_id})"
