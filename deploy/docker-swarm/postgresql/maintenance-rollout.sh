#!/usr/bin/env bash
set -euo pipefail

cluster_id=""
nodes_csv=""
stack_name="cgpg16"
api_url="https://127.0.0.1:3000"
token_file="/etc/clusterguard/clusterguard.env"
ssh_user="root"
ssh_password="${CG_SSH_PASSWORD:-}"
ssh_key=""
ssh_port=22
timeout_seconds=300
execute=false
insecure=false
accept_host_keys=false

usage() {
  cat <<'EOF'
用法：maintenance-rollout.sh [参数]

安全地逐个重启 ClusterGuard 管理的 Docker Swarm PostgreSQL 服务。
默认只生成计划；追加 --execute 才会执行。

必填参数：
  --cluster-id UUID       ClusterGuard 集群资源 ID
  --nodes HOSTS           Agent 节点，逗号分隔

可选参数：
  --stack NAME            Swarm stack 名称，默认 cgpg16
  --api-url URL           任一控制节点 API，默认 https://127.0.0.1:3000
  --token-file FILE       含 CG_CONTROL_TOKEN 的环境文件
  -u, --ssh-user USER     SSH 用户，默认 root
  -P, --ssh-password PASS 所有节点共用 SSH 密码；也可使用 CG_SSH_PASSWORD
  -i, --ssh-key FILE      SSH 私钥；未提供密码时使用
  --ssh-port PORT         SSH 端口，默认 22
  --timeout SECONDS       单节点等待上限，默认 300
  --insecure              允许控制 API 使用自签名证书
  --accept-host-keys      首次连接时接受并固定 SSH 主机密钥
  --execute               执行维护滚动
  -h, --help              显示帮助

执行期间自动故障切换会被冻结，Agent VIP reconcile 会暂停。只有在数据库
角色、复制、VIP 租约和 Agent reconcile 全部验证通过后才解除冻结。
EOF
}

log() {
  printf '[%s] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"
}

die() {
  log "错误：$*" >&2
  exit 1
}

while (($#)); do
  case "$1" in
    --cluster-id) cluster_id="${2:-}"; shift 2 ;;
    --nodes) nodes_csv="${2:-}"; shift 2 ;;
    --stack) stack_name="${2:-}"; shift 2 ;;
    --api-url) api_url="${2:-}"; shift 2 ;;
    --token-file) token_file="${2:-}"; shift 2 ;;
    -u|--ssh-user) ssh_user="${2:-}"; shift 2 ;;
    -P|--ssh-password) ssh_password="${2:-}"; shift 2 ;;
    -i|--ssh-key) ssh_key="${2:-}"; shift 2 ;;
    --ssh-port) ssh_port="${2:-}"; shift 2 ;;
    --timeout) timeout_seconds="${2:-}"; shift 2 ;;
    --insecure) insecure=true; shift ;;
    --accept-host-keys) accept_host_keys=true; shift ;;
    --execute) execute=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数：$1" ;;
  esac
done

[[ "${cluster_id}" =~ ^[0-9a-fA-F-]{36}$ ]] || die "--cluster-id 必须是 UUID"
[[ -n "${nodes_csv}" ]] || die "必须提供 --nodes"
[[ "${stack_name}" =~ ^[A-Za-z0-9_.-]+$ ]] || die "--stack 格式无效"
[[ "${ssh_user}" =~ ^[A-Za-z_][A-Za-z0-9_.-]*$ ]] || die "SSH 用户格式无效"
[[ "${ssh_port}" =~ ^[0-9]+$ ]] && ((ssh_port > 0 && ssh_port < 65536)) || die "SSH 端口无效"
[[ "${timeout_seconds}" =~ ^[0-9]+$ ]] && ((timeout_seconds >= 30)) || die "--timeout 不能小于 30 秒"
[[ -r "${token_file}" ]] || die "无法读取 token 文件：${token_file}"

for command in curl docker jq ssh; do
  command -v "${command}" >/dev/null 2>&1 || die "缺少命令：${command}"
done
if [[ -n "${ssh_password}" ]]; then
  command -v sshpass >/dev/null 2>&1 || die "使用 SSH 密码时需要 sshpass"
fi
if [[ -n "${ssh_key}" ]]; then
  [[ -r "${ssh_key}" ]] || die "无法读取 SSH 私钥：${ssh_key}"
fi

set -a
# shellcheck disable=SC1090
. "${token_file}"
set +a
[[ -n "${CG_CONTROL_TOKEN:-}" ]] || die "${token_file} 未设置 CG_CONTROL_TOKEN"

IFS=',' read -r -a nodes <<<"${nodes_csv}"
((${#nodes[@]} > 0)) || die "节点列表为空"
for node in "${nodes[@]}"; do
  [[ "${node}" =~ ^[A-Za-z0-9][A-Za-z0-9_.:-]*$ ]] || die "节点地址格式无效：${node}"
done

curl_options=(-sS --connect-timeout 5 --max-time 120)
${insecure} && curl_options+=(-k)

api_request() {
  local method="$1" path="$2" body="${3:-}" response
  if [[ "${method}" == GET ]]; then
    response="$(curl "${curl_options[@]}" -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" "${api_url}${path}")"
  else
    response="$(curl "${curl_options[@]}" -X "${method}" \
      -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
      -H 'Content-Type: application/json' "${api_url}${path}" -d "${body}")"
  fi
  jq -e '.status == "ok"' >/dev/null <<<"${response}" || {
    jq -c '{status,error,message}' <<<"${response}" >&2 || printf '%s\n' "${response}" >&2
    return 1
  }
  printf '%s' "${response}"
}

ssh_options=(-p "${ssh_port}" -o ConnectTimeout=8 -o ServerAliveInterval=15)
[[ -z "${ssh_key}" ]] || ssh_options+=(-i "${ssh_key}" -o IdentitiesOnly=yes)
if ${accept_host_keys}; then
  ssh_options+=(-o StrictHostKeyChecking=accept-new)
else
  ssh_options+=(-o StrictHostKeyChecking=yes)
fi

remote_run() {
  local node="$1" command="$2"
  if [[ -n "${ssh_password}" ]]; then
    SSHPASS="${ssh_password}" sshpass -e ssh "${ssh_options[@]}" \
      -o PreferredAuthentications=password,keyboard-interactive \
      -o PubkeyAuthentication=no "${ssh_user}@${node}" "${command}"
  else
    ssh "${ssh_options[@]}" "${ssh_user}@${node}" "${command}"
  fi
}

status="$(api_request GET /api/v1/control-plane/status)"
leader_api="$(jq -r '.result.leader_api_address // empty' <<<"${status}")"
[[ -n "${leader_api}" ]] || die "控制面未确认 Raft Leader"
api_url="${leader_api%/}"
status="$(api_request GET /api/v1/control-plane/status)"
jq -e '.result.ready == true and .result.quorum_confirmed == true and .result.voter_count >= 3 and .result.active_operations == 0 and .result.indeterminate_operations == 0' \
  >/dev/null <<<"${status}" || die "控制面未就绪、有活动操作或存在待复核操作"

clusters="$(api_request GET /api/v1/clusters)"
cluster_frozen="$(jq -r --arg id "${cluster_id}" '.result[] | select(.resource_id == $id) | .recovery_freeze' <<<"${clusters}")"
[[ "${cluster_frozen}" == false ]] || die "集群不存在或已经处于恢复冻结状态，请先人工确认"

topology="$(api_request POST "/api/v1/clusters/${cluster_id}/discover" '{}')"
jq -e '.result.health.state == "healthy" and ([.result.instances[] | select(.role == "primary" and .health.state == "healthy")] | length) == 1 and ([.result.instances[] | select(.role == "standby" and .health.state == "healthy" and .health.replication == "streaming")] | length) >= 1' \
  >/dev/null <<<"${topology}" || die "维护前拓扑不是一主至少一健康流复制 standby"

declare -A service_by_instance=()
while IFS= read -r service_id; do
  [[ -n "${service_id}" ]] || continue
  instance_id="$(docker service inspect "${service_id}" --format '{{index .Spec.TaskTemplate.ContainerSpec.Labels "clusterguard.instance_id"}}')"
  [[ -n "${instance_id}" ]] || continue
  service_by_instance["${instance_id}"]="$(docker service inspect "${service_id}" --format '{{.Spec.Name}}')"
done < <(docker stack services --quiet "${stack_name}")

mapfile -t rollout_instances < <(jq -r '
  ([.result.instances[] | select(.role == "standby")] | sort_by(.hostname)) +
  [.result.instances[] | select(.role == "primary")] |
  .[] | [.resource_id, .role, .hostname] | @tsv
' <<<"${topology}")
((${#rollout_instances[@]} >= 2)) || die "未找到可维护的一主一从服务"

printf '\nDocker Swarm PostgreSQL 维护计划\n'
printf '  集群资源 ID : %s\n' "${cluster_id}"
printf '  Stack        : %s\n' "${stack_name}"
printf '  控制面 Leader: %s\n' "${api_url}"
printf '  Agent 节点   : %s\n' "${nodes_csv}"
printf '  执行模式     : %s\n\n' "$(${execute} && printf '真实维护' || printf '只读计划')"
for row in "${rollout_instances[@]}"; do
  IFS=$'\t' read -r instance_id role hostname <<<"${row}"
  service="${service_by_instance[${instance_id}]:-}"
  [[ -n "${service}" ]] || die "实例 ${instance_id} 未映射到 stack ${stack_name} 的服务"
  printf '  %-24s %-8s %s\n' "${service}" "${role}" "${hostname}"
done

${execute} || {
  log "只读计划完成；确认后追加 --execute"
  exit 0
}

log "警告：系统维护期间无法进行自动故障切换，请持续关注数据库主从、VIP、Raft 和 Agent 状态"
frozen=false
timers_paused=false

fail_closed() {
  local code=$?
  trap - ERR INT TERM
  if ${frozen}; then
    for node in "${nodes[@]}"; do
      remote_run "${node}" 'systemctl stop clusterguard-agent-reconcile.timer' >/dev/null 2>&1 || true
    done
  fi
  log "维护失败：恢复冻结保持启用，Agent reconcile 保持停止；修复并验证后再人工恢复" >&2
  exit "${code}"
}
trap fail_closed ERR INT TERM

freeze_response="$(api_request POST "/api/v1/clusters/${cluster_id}/recovery-freeze" '{"freeze":true}')"
jq -e '.recovery_freeze == true' >/dev/null <<<"${freeze_response}"
frozen=true
log "已冻结自动恢复"

for node in "${nodes[@]}"; do
  remote_run "${node}" 'systemctl stop clusterguard-agent-reconcile.timer; ! systemctl is-active --quiet clusterguard-agent-reconcile.timer'
done
timers_paused=true
log "已暂停全部 Agent reconcile timer"

wait_service() {
  local service="$1" deadline=$((SECONDS + timeout_seconds)) desired running update_state
  while ((SECONDS < deadline)); do
    desired="$(docker service inspect "${service}" --format '{{.Spec.Mode.Replicated.Replicas}}')"
    running="$(docker service ps "${service}" --filter desired-state=running --format '{{.CurrentState}}' | grep -c '^Running' || true)"
    update_state="$(docker service inspect "${service}" --format '{{if .UpdateStatus}}{{.UpdateStatus.State}}{{end}}')"
    if [[ "${running}" == "${desired}" && "${update_state}" != paused && "${update_state}" != rollback_paused ]]; then
      return 0
    fi
    sleep 3
  done
  return 1
}

wait_instance() {
  local instance_id="$1" role="$2" deadline=$((SECONDS + timeout_seconds)) current
  while ((SECONDS < deadline)); do
    current="$(api_request POST "/api/v1/clusters/${cluster_id}/discover" '{}')"
    if jq -e --arg id "${instance_id}" --arg role "${role}" '
      .result.instances[] | select(.resource_id == $id) |
      .role == $role and .health.state == "healthy" and
      ($role == "primary" or .health.replication == "streaming")
    ' >/dev/null <<<"${current}"; then
      return 0
    fi
    sleep 3
  done
  return 1
}

for row in "${rollout_instances[@]}"; do
  IFS=$'\t' read -r instance_id role hostname <<<"${row}"
  service="${service_by_instance[${instance_id}]}"
  log "滚动重启 ${service} (${role}, ${hostname})"
  docker service update --force --detach=true "${service}" >/dev/null
  wait_service "${service}" || die "${service} 未在时限内恢复 1/1"
  wait_instance "${instance_id}" "${role}" || die "${service} 数据库角色或复制未在时限内恢复"
  log "${service} 已恢复并通过角色验证"
done

topology="$(api_request POST "/api/v1/clusters/${cluster_id}/discover" '{}')"
primary_id="$(jq -r '.result.instances[] | select(.role == "primary" and .health.state == "healthy") | .resource_id' <<<"${topology}")"
[[ -n "${primary_id}" ]] || die "维护后未确认唯一健康主库"
ownership="$(api_request GET "/api/v1/clusters/${cluster_id}/ha-ownership")"
jq -e --arg owner "${primary_id}" '.result.resource.healthy == true and .result.resource.owner_id == $owner and .result.active_lease.active == true' \
  >/dev/null <<<"${ownership}" || die "维护后 VIP 所有权租约未恢复"

for node in "${nodes[@]}"; do
  remote_run "${node}" 'systemctl start clusterguard-agent-reconcile.timer; systemctl start clusterguard-agent-reconcile.service; systemctl is-active --quiet clusterguard-agent-reconcile.timer; test "$(systemctl show -p Result --value clusterguard-agent-reconcile.service)" = success'
done
timers_paused=false
log "Agent reconcile 已恢复并完成一轮验证"

topology="$(api_request POST "/api/v1/clusters/${cluster_id}/discover" '{}')"
jq -e '.result.health.state == "healthy" and ([.result.instances[] | select(.role == "primary" and .health.state == "healthy")] | length) == 1 and ([.result.instances[] | select(.role == "standby" and .health.state == "healthy" and .health.replication == "streaming")] | length) >= 1' \
  >/dev/null <<<"${topology}" || die "恢复 Agent 后拓扑验证失败"
ownership="$(api_request GET "/api/v1/clusters/${cluster_id}/ha-ownership")"
jq -e --arg owner "${primary_id}" '.result.resource.healthy == true and .result.resource.owner_id == $owner and .result.active_lease.active == true' \
  >/dev/null <<<"${ownership}" || die "恢复 Agent 后 VIP 租约验证失败"

unfreeze_response="$(api_request POST "/api/v1/clusters/${cluster_id}/recovery-freeze" '{"freeze":false}')"
jq -e '.recovery_freeze == false' >/dev/null <<<"${unfreeze_response}"
frozen=false
trap - ERR INT TERM
log "维护滚动完成：数据库角色、流复制、VIP 租约和 Agent reconcile 均已验证，自动恢复已重新启用"
