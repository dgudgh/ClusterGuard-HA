#!/usr/bin/env bash
set -euo pipefail

workload="${1:?workload name is required}"
vip="${2:?VIP is required}"
port="${3:?port is required}"
duration_seconds="${4:?duration is required}"
run_id="${5:?run ID is required}"
log_root="${6:-/var/log/clusterguard/qualification}"

[[ "${workload}" =~ ^[A-Za-z0-9._-]+$ ]] || {
  echo "workload name must contain only letters, numbers, dots, underscores, and dashes" >&2
  exit 2
}
[[ "${run_id}" =~ ^[A-Za-z0-9._-]+$ ]] || {
  echo "run ID must contain only letters, numbers, dots, underscores, and dashes" >&2
  exit 2
}
[[ "${port}" =~ ^[0-9]+$ ]] || {
  echo "port must be numeric" >&2
  exit 2
}
[[ "${duration_seconds}" =~ ^[0-9]+$ ]] || {
  echo "duration must be numeric" >&2
  exit 2
}
port_number=$((10#${port}))
duration_number=$((10#${duration_seconds}))
((port_number > 0 && port_number <= 65535)) || {
  echo "port must be between 1 and 65535" >&2
  exit 2
}
((duration_number > 0)) || {
  echo "duration must be greater than zero" >&2
  exit 2
}

environment_file="${CG_ENV_FILE:-/etc/clusterguard/clusterguard.env}"
[[ -r "${environment_file}" ]] || {
  echo "ClusterGuard environment file is not readable: ${environment_file}" >&2
  exit 3
}
set -a
source "${environment_file}"
set +a

mysql_password="${CG_NODE_MYSQL_ROOT_PASSWORD:?CG_NODE_MYSQL_ROOT_PASSWORD is required}"
mysql_client="${CG_MYSQL_CLIENT:-}"
if [[ -z "${mysql_client}" ]]; then
  for candidate in /usr/local/mysql/bin/mysql /usr/bin/mysql; do
    if [[ -x "${candidate}" ]]; then
      mysql_client="${candidate}"
      break
    fi
  done
fi
[[ -x "${mysql_client:-}" ]] || {
  echo "mysql client is unavailable" >&2
  exit 3
}
jq_binary="${CG_JQ_BIN:-$(command -v jq || true)}"
[[ -x "${jq_binary:-}" ]] || {
  echo "jq is unavailable" >&2
  exit 3
}
timeout_binary="${CG_TIMEOUT_BIN:-}"
if [[ -z "${timeout_binary}" ]]; then
  timeout_binary="$(command -v timeout || command -v gtimeout || true)"
fi
[[ -x "${timeout_binary:-}" ]] || {
  echo "timeout is unavailable" >&2
  exit 3
}

install -d -m 0750 "${log_root}"
events_file="${log_root}/${workload}-${run_id}.tsv"
summary_file="${log_root}/${workload}-${run_id}.json"
stop_file="${log_root}/${workload}-${run_id}.stop"
summary_tmp="$(mktemp "${summary_file}.tmp.XXXXXX")"
cleanup() {
  rm -f "${summary_tmp}"
}
trap cleanup EXIT
rm -f "${stop_file}"
printf 'timestamp_ms\tsequence\tstatus\tlatency_ms\n' >"${events_file}"
chmod 0640 "${events_file}"

mysql_args=(
  --batch
  --skip-column-names
  --connect-timeout=3
  --protocol=TCP
  --host="${vip}"
  --port="${port_number}"
  --user=root
)

run_sql() {
  local sql="$1"
  MYSQL_PWD="${mysql_password}" "${timeout_binary}" 8 "${mysql_client}" "${mysql_args[@]}" --execute "${sql}"
}

now_ms() {
  local value
  value="$(date +%s%3N)"
  if [[ "${value}" =~ ^[0-9]+$ ]]; then
    printf '%s\n' "${value}"
    return
  fi
  if command -v perl >/dev/null 2>&1; then
    perl -MTime::HiRes=time -e 'printf "%.0f\n", time() * 1000'
    return
  fi
  printf '%s000\n' "$(date +%s)"
}

setup_deadline=$(( $(date +%s) + duration_number ))
setup_sql="
CREATE DATABASE IF NOT EXISTS cg_prodqual CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
CREATE TABLE IF NOT EXISTS cg_prodqual.cg_switch_writes (
  run_id VARCHAR(80) NOT NULL,
  sequence_no BIGINT UNSIGNED NOT NULL,
  payload CHAR(64) NOT NULL,
  client_label VARCHAR(64) NOT NULL,
  committed_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (run_id, sequence_no)
) ENGINE=InnoDB;"

while ! run_sql "${setup_sql}" >/dev/null 2>&1; do
  if (( $(date +%s) >= setup_deadline )); then
    echo "schema setup did not converge before deadline" >&2
    exit 4
  fi
  sleep 1
done

deadline=$(( $(date +%s) + duration_number ))
sequence=1
attempts=0
acknowledged=0
failures=0
max_outage_ms=0
outage_started_ms=0
started_ms="$(now_ms)"
started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

while (( $(date +%s) < deadline )) && [[ ! -e "${stop_file}" ]]; do
  attempts=$((attempts + 1))
  attempt_started_ms="$(now_ms)"
  payload="$(printf '%s:%s' "${run_id}" "${sequence}" | sha256sum | awk '{print $1}')"
  insert_sql="
INSERT INTO cg_prodqual.cg_switch_writes
  (run_id, sequence_no, payload, client_label)
VALUES
  ('${run_id}', ${sequence}, '${payload}', '${workload}')
ON DUPLICATE KEY UPDATE
  payload=VALUES(payload),
  client_label=VALUES(client_label);"
  if run_sql "${insert_sql}" >/dev/null 2>&1; then
    completed_ms="$(now_ms)"
    latency_ms=$((completed_ms - attempt_started_ms))
    if ((outage_started_ms > 0)); then
      outage_ms=$((completed_ms - outage_started_ms))
      if ((outage_ms > max_outage_ms)); then
        max_outage_ms=${outage_ms}
      fi
      outage_started_ms=0
    fi
    printf '%s\t%s\tok\t%s\n' "${completed_ms}" "${sequence}" "${latency_ms}" >>"${events_file}"
    acknowledged=$((acknowledged + 1))
    sequence=$((sequence + 1))
    sleep 0.05
  else
    failed_ms="$(now_ms)"
    latency_ms=$((failed_ms - attempt_started_ms))
    failures=$((failures + 1))
    if ((outage_started_ms == 0)); then
      outage_started_ms=${attempt_started_ms}
    fi
    printf '%s\t%s\tfail\t%s\n' "${failed_ms}" "${sequence}" "${latency_ms}" >>"${events_file}"
    sleep 0.2
  fi
done

finished_ms="$(now_ms)"
finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
if ((outage_started_ms > 0)); then
  outage_ms=$((finished_ms - outage_started_ms))
  if ((outage_ms > max_outage_ms)); then
    max_outage_ms=${outage_ms}
  fi
fi

observed_raw="unavailable"
for _ in $(seq 1 30); do
  if observed_raw="$(run_sql "SELECT CONCAT(COUNT(*),':',COALESCE(MIN(sequence_no),0),':',COALESCE(MAX(sequence_no),0),':',COALESCE(SUM(CRC32(CONCAT(run_id,':',sequence_no,':',payload))),0)) FROM cg_prodqual.cg_switch_writes WHERE run_id='${run_id}';" 2>/dev/null)"; then
    break
  fi
  sleep 1
done

observed_count=0
observed_min=0
observed_max=0
observed_checksum=""
integrity_status="fail"
if [[ "${observed_raw}" =~ ^([0-9]+):([0-9]+):([0-9]+):([0-9]+)$ ]]; then
  observed_count=$((10#${BASH_REMATCH[1]}))
  observed_min=$((10#${BASH_REMATCH[2]}))
  observed_max=$((10#${BASH_REMATCH[3]}))
  observed_checksum="${BASH_REMATCH[4]}"
  if ((acknowledged > 0 &&
       observed_count == acknowledged &&
       observed_min == 1 &&
       observed_max == acknowledged)); then
    integrity_status="pass"
  fi
fi

if ! "${jq_binary}" -e -n \
  --arg workload "${workload}" \
  --arg vip "${vip}" \
  --argjson port "${port_number}" \
  --arg run_id "${run_id}" \
  --arg started_at "${started_at}" \
  --arg finished_at "${finished_at}" \
  --argjson started_ms "${started_ms}" \
  --argjson finished_ms "${finished_ms}" \
  --argjson attempts "${attempts}" \
  --argjson acknowledged "${acknowledged}" \
  --argjson failures "${failures}" \
  --argjson max_outage_ms "${max_outage_ms}" \
  --arg integrity_status "${integrity_status}" \
  --argjson observed_count "${observed_count}" \
  --argjson observed_min "${observed_min}" \
  --argjson observed_max "${observed_max}" \
  --arg observed_checksum "${observed_checksum}" \
  '{
    report_version:1,
    workload:$workload,
    vip:$vip,
    port:$port,
    run_id:$run_id,
    started_at:$started_at,
    finished_at:$finished_at,
    started_ms:$started_ms,
    finished_ms:$finished_ms,
    elapsed_ms:($finished_ms-$started_ms),
    attempts:$attempts,
    acknowledged:$acknowledged,
    failures:$failures,
    max_outage_ms:$max_outage_ms,
    integrity_status:$integrity_status,
    observed:{
      count:$observed_count,
      min_sequence:$observed_min,
      max_sequence:$observed_max,
      checksum:$observed_checksum
    }
  }' >"${summary_tmp}"; then
  echo "qualification report rendering failed" >&2
  exit 6
fi

chmod 0640 "${summary_tmp}"
mv -f "${summary_tmp}" "${summary_file}"
trap - EXIT
cat "${summary_file}"
[[ "${integrity_status}" == "pass" ]] || exit 7
