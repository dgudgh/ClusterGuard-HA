#!/usr/bin/env bash
set -euo pipefail
umask 077
unset CG_UPDATE_BOOTSTRAP_DEPTH

root="${CG_UPDATE_ROOT:-/var/lib/clusterguard/updates}"
config="${CG_UPDATE_CONFIG:-/etc/clusterguard/update.json}"
upgrader="${CG_UPDATE_BINARY:-/usr/local/sbin/clusterguard-upgrade}"
workspace_helper="${CG_UPDATE_WORKSPACE_HELPER:-/usr/local/libexec/clusterguard-update-helper}"
private_root="${CG_UPDATE_PRIVATE_ROOT:-/var/lib/clusterguard-update-private}"
mode=""
patch_id=""

die() { printf '更新任务失败：%s\n' "$*" >&2; exit 1; }
while (($#)); do
  case "$1" in
    --mode) [[ $# -ge 2 ]] || die "--mode 缺少值"; mode="$2"; shift 2 ;;
    --patch-id) [[ $# -ge 2 ]] || die "--patch-id 缺少值"; patch_id="$2"; shift 2 ;;
    *) die "未知参数：$1" ;;
  esac
done
[[ "${mode}" == plan || "${mode}" == execute || "${mode}" == resume || "${mode}" == rollback ]] || die "任务模式无效"
[[ "${patch_id}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || die "升级包 ID 无效"
[[ -f "${config}" && ! -L "${config}" ]] || die "升级配置不存在：${config}"
[[ -x "${upgrader}" && ! -L "${upgrader}" ]] || die "升级器不存在：${upgrader}"
"${workspace_helper}" workspace check-file "${config}"
"${workspace_helper}" workspace check-file "${upgrader}"
"${workspace_helper}" workspace create-directory "${private_root}"
exec 9>"${private_root}/update.lock"
flock -n 9 || die "已有软件更新任务正在运行"

jq_binary="$(command -v jq 2>/dev/null || true)"
tool_dir=""
if [[ -z "${jq_binary}" && -x /usr/local/libexec/jq-linux-amd64 ]]; then
  tool_dir="$(mktemp -d /run/clusterguard/update-tools.XXXXXX)"
  ln -s /usr/local/libexec/jq-linux-amd64 "${tool_dir}/jq"
  export PATH="${tool_dir}:${PATH}"
  jq_binary="${tool_dir}/jq"
fi
[[ -x "${jq_binary}" ]] || die "缺少 jq"

public_dir="${root}/${patch_id}"
job_dir="${private_root}/jobs/${patch_id}"
"${workspace_helper}" workspace create-directory "${job_dir}"
patch="${job_dir}/package.cgpatch"
status_file="${job_dir}/status.json"
# Never import public status/events as recovery authority. Existing private
# records survive plan/resume and can only have been written by root.
"${workspace_helper}" workspace snapshot "${public_dir}/package.cgpatch" "${patch}"
"${workspace_helper}" workspace snapshot "${public_dir}/package.json" "${job_dir}/package.json"

publish_public_file() {
  local source="$1" destination="$2"
  [[ -f "${source}" && ! -L "${source}" ]] || return 0
  [[ "${destination}" == "$(basename -- "${destination}")" ]] || return 1
  command -v runuser >/dev/null 2>&1 || return 1
  runuser -u clusterguard -- bash -c '
    set -euo pipefail
    umask 027
    directory=$1
    name=$2
    temporary=$(mktemp "${directory}/.clusterguard-publish.XXXXXXXX")
    trap '\''rm -f -- "$temporary"'\'' EXIT
    cat >"$temporary"
    chmod 0640 "$temporary"
    mv -fT -- "$temporary" "${directory}/${name}"
    trap - EXIT
  ' bash "${public_dir}" "${destination}" <"${source}"
}

publish_public_artifacts() {
  local artifact
  publish_public_file "${status_file}" status.json || return 1
  publish_public_file "${job_dir}/output.log" output.log || return 1
  shopt -s nullglob
  for artifact in "${job_dir}"/clusterguard-update-*.json "${job_dir}"/clusterguard-update-*.events.jsonl; do
    publish_public_file "${artifact}" "$(basename -- "${artifact}")" || { shopt -u nullglob; return 1; }
  done
  shopt -u nullglob
}

started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
write_status() {
  local status="$1" message="$2" maintenance="$3" finished="${4:-}"
  local temporary="${status_file}.tmp"
  "${jq_binary}" -n \
    --arg patch_id "${patch_id}" --arg mode "${mode}" --arg status "${status}" \
    --arg message "${message}" --arg warning "系统升级期间无法进行自动切换，请注意关注。" \
    --arg started_at "${started_at}" --arg updated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg finished_at "${finished}" --argjson maintenance_active "${maintenance}" \
    '{patch_id:$patch_id,mode:$mode,status:$status,message:$message,
      warning:(if $mode == "plan" then "" else $warning end),
      maintenance_active:$maintenance_active,
      automatic_failover_available:($maintenance_active | not),
      started_at:$started_at,updated_at:$updated_at,
      finished_at:(if $finished_at == "" then null else $finished_at end)}' >"${temporary}"
  mv -f "${temporary}" "${status_file}"
  publish_public_artifacts || printf '警告：私有状态已持久化，但控制台状态发布失败\n' >&2
}

cleanup() { [[ -z "${tool_dir}" || ! -d "${tool_dir}" ]] || rm -rf "${tool_dir}"; }
trap cleanup EXIT

schedule_helper_refresh() {
  [[ "${mode}" != plan ]] || return 0
  local systemd_run systemctl_binary unit
  systemd_run="$(command -v systemd-run 2>/dev/null || true)"
  systemctl_binary="$(command -v systemctl 2>/dev/null || true)"
  if [[ -z "${systemd_run}" || -z "${systemctl_binary}" ]]; then
    printf '警告：升级已记录最终状态，但无法调度软件更新 Helper 自刷新\n' >&2
    return 1
  fi
  unit="clusterguard-update-helper-refresh-$(date +%s)-$$"
  if ! "${systemd_run}" --quiet --unit "${unit}" --on-active=3s \
      "${systemctl_binary}" restart clusterguard-update-helper.service; then
    printf '警告：升级已记录最终状态，但软件更新 Helper 自刷新调度失败\n' >&2
    return 1
  fi
}

trust_key="$("${jq_binary}" -er '.trust_key' "${config}")"
state_file="$("${jq_binary}" -er '.deployment_state' "${config}")"
ssh_user="$("${jq_binary}" -r '.ssh_user // "root"' "${config}")"
ssh_key="$("${jq_binary}" -r '.ssh_key // empty' "${config}")"
known_hosts="$("${jq_binary}" -r '.known_hosts // empty' "${config}")"
ssh_credentials="$("${jq_binary}" -r '.ssh_credentials_file // empty' "${config}")"
ssh_port="$("${jq_binary}" -r '.ssh_port // 22' "${config}")"
api_port="$("${jq_binary}" -r '.api_port // 3000' "${config}")"
retained_versions="$("${jq_binary}" -r '.retained_versions // 3' "${config}")"
controllers="$("${jq_binary}" -r '(.controllers // []) | join(",")' "${config}")"
data_nodes="$("${jq_binary}" -r '(.data_nodes // []) | join(",")' "${config}")"
[[ -f "${trust_key}" && ! -L "${trust_key}" ]] || die "可信签名公钥无效"
[[ -f "${state_file}" && ! -L "${state_file}" ]] || die "部署状态文件无效"
[[ -z "${ssh_key}" || ( -f "${ssh_key}" && ! -L "${ssh_key}" ) ]] || die "SSH 私钥无效"
[[ -z "${known_hosts}" || ( -f "${known_hosts}" && ! -L "${known_hosts}" ) ]] || die "known_hosts 无效"
[[ -z "${ssh_credentials}" || ( -f "${ssh_credentials}" && ! -L "${ssh_credentials}" ) ]] || die "SSH 凭据文件无效"
[[ "${retained_versions}" =~ ^[1-9][0-9]*$ ]] || die "retained_versions 必须为正整数"
for privileged_input in "${trust_key}" "${state_file}" "${ssh_key}" "${known_hosts}" "${ssh_credentials}"; do
  [[ -z "${privileged_input}" ]] || "${workspace_helper}" workspace check-file "${privileged_input}"
done

arguments=(--patch "${patch}" --expected-patch-id "${patch_id}" --private-root "${private_root}" --managed-job-dir "${public_dir}" --trust-key "${trust_key}" --state "${state_file}" --update-root "${root}" --retain-versions "${retained_versions}" -u "${ssh_user}" --ssh-port "${ssh_port}" --api-port "${api_port}")
[[ -z "${controllers}" ]] || arguments+=(--controllers "${controllers}")
[[ -z "${data_nodes}" ]] || arguments+=(--data-nodes "${data_nodes}")
[[ -z "${ssh_key}" ]] || arguments+=(--ssh-key "${ssh_key}")
[[ -z "${known_hosts}" ]] || arguments+=(--known-hosts "${known_hosts}")
[[ -z "${ssh_credentials}" ]] || arguments+=(--ssh-credentials-file "${ssh_credentials}")

case "${mode}" in
  plan) arguments+=(--plan) ;;
  execute) arguments+=(--execute --yes) ;;
  resume) arguments+=(--resume --execute --yes) ;;
  rollback) arguments+=(--rollback --execute --yes) ;;
esac

maintenance=false
[[ "${mode}" == plan ]] || maintenance=true
write_status running "$([[ "${mode}" == plan ]] && printf '正在生成只读滚动升级计划' || printf '正在执行滚动升级；自动故障切换已暂停')" "${maintenance}"
if (cd "${job_dir}" && "${upgrader}" "${arguments[@]}"); then
  finished="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  case "${mode}" in
    plan) write_status planned "升级计划已生成，尚未修改任何节点" false "${finished}" ;;
    rollback) write_status rolled_back "受控回退完成，维护门禁已释放" false "${finished}" ;;
    *) write_status succeeded "滚动升级完成，全部节点与控制面已验证，维护门禁已释放" false "${finished}" ;;
  esac
  schedule_helper_refresh || true
else
  exit_code=$?
  journal_events="${job_dir}/clusterguard-update-${patch_id}.events.jsonl"
  last_event_status="$(tail -n 1 "${journal_events}" 2>/dev/null | "${jq_binary}" -r '.status // empty' 2>/dev/null || true)"
  if [[ "${last_event_status}" == "rolled_back" ]]; then
    write_status rolled_back "升级未完成，已自动回退全部节点并释放维护门禁" false "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    schedule_helper_refresh || true
    exit "${exit_code}"
  fi
  maintenance_after_failure=false
  [[ -f /etc/clusterguard/update-maintenance.json ]] && maintenance_after_failure=true
  # A failed final release can leave only the replicated gate. Preserve the
  # updater's durable maintenance result when no local marker remains.
  if "${jq_binary}" -e '.maintenance_active == true' "${status_file}" >/dev/null 2>&1 &&
      [[ "${last_event_status}" == failed || "${last_event_status}" == rollback_failed || "${last_event_status}" == rollback_lock_release_failed ]]; then
    maintenance_after_failure=true
  fi
  write_status failed "升级任务失败或被阻断；请查看输出和事件记录，确认维护门禁状态后再续跑或回退" "${maintenance_after_failure}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  schedule_helper_refresh || true
  exit "${exit_code}"
fi
