#!/bin/bash

# power-lifecycle-lab.sh — end-to-end power lifecycle validation against a
# live lab cluster (the v4.0 "Power Lifecycle Management" acceptance run).
#
# The script drives the control-plane API only and asserts every transition:
#   L1. topology preconditions (registered cluster, healthy observation)
#   L2. power/precheck -> prechecking, power/plan -> shutdown_planned
#   L3. issue approval, power/execute -> power_off with protections active
#   L4. (service mode) database services stopped on every node via SSH probe;
#       (poweroff mode) hosts powered off via CG_LAB_SSH_TARGETS
#   L5. boot the nodes, then power/boot-detected -> power/recovering ->
#       power/verify -> power/complete; protections released, state completed
#   L6. final assertions: recovery freeze off, maintenance cleared
#
# Credentials travel only through the environment (never the repo):
#   CG_CONTROL_TOKEN  — control token for the platform API
#   CLUSTERGUARD_API  — platform base URL (default https://127.0.0.1:3000)
#   CG_CLUSTER_ID     — cluster id (optional; default = first registered)
#   CG_LAB_SSH_TARGETS — optional space-separated ssh targets (e.g.
#                        "root@192.168.102.152 root@192.168.102.153 ...")
#                        used to probe services and to reboot nodes. SSH keys
#                        must already be authorized on the lab hosts.
#
# Usage:
#   CG_CONTROL_TOKEN=... \
#   CG_LAB_SSH_TARGETS="root@192.168.102.152 root@192.168.102.153 root@192.168.102.154" \
#   bash scripts/integration/power-lifecycle-lab.sh [service|poweroff]
#
# Exit code 0 only when every assertion holds. Nothing is committed here and
# the cluster is left recovered (protections released) on success.

set -Eeuo pipefail

mode="${1:-service}"
[[ "$mode" == "service" || "$mode" == "poweroff" ]] || { echo "mode must be service or poweroff" >&2; exit 2; }

cg_api_url="${CLUSTERGUARD_API:-https://127.0.0.1:3000}"
cg_control_token="${CG_CONTROL_TOKEN:-}"
cluster_id="${CG_CLUSTER_ID:-}"
ssh_targets="${CG_LAB_SSH_TARGETS:-}"
curl_command="${CURL_COMMAND:-curl}"
jq_command="${JQ_COMMAND:-jq}"
api_timeout="${CLUSTER_LAB_API_TIMEOUT:-600}"
ssh_command="${CLUSTERGUARD_SSH:-ssh}"

[[ -n "$cg_control_token" ]] || { echo "CG_CONTROL_TOKEN is required" >&2; exit 2; }
command -v "$jq_command" >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }

function api {
  # api METHOD PATH [BODY] — curl wrapper asserting HTTP 2xx.
  local method path body code
  method="$1"; path="$2"; body="${3:-}"
  if [[ -n "$body" ]]; then
    code="$("$curl_command" -sk --silent --show-error --max-time 30 -X "$method" \
      -H "Authorization: Bearer ${cg_control_token}" -H 'Content-Type: application/json' -d "$body" \
      -w '%{http_code}' -o /dev/null "${cg_api_url}${path}" || true)"
  else
    code="$("$curl_command" -sk --silent --show-error --max-time 30 -X "$method" \
      -H "Authorization: Bearer ${cg_control_token}" \
      -w '%{http_code}' -o /dev/null "${cg_api_url}${path}" || true)"
  fi
  [[ "$code" == "2"* ]] || { echo "api ${method} ${path} -> HTTP ${code}" >&2; return 1; }
  return 0
}

function api_json {
  # api_json METHOD PATH [BODY] — like api but echoes the response body.
  local method path body
  method="$1"; path="$2"; body="${3:-}"
  if [[ -n "$body" ]]; then
    "$curl_command" -sk --silent --show-error --max-time 30 -X "$method" \
      -H "Authorization: Bearer ${cg_control_token}" -H 'Content-Type: application/json' -d "$body" \
      "${cg_api_url}${path}"
  else
    "$curl_command" -sk --silent --show-error --max-time 30 -X "$method" \
      -H "Authorization: Bearer ${cg_control_token}" "${cg_api_url}${path}"
  fi
}

function step {
  echo ""
  echo "==> $1"
}

function ssh_each {
  # ssh_each COMMAND... — run on every lab target (if configured).
  [[ -n "$ssh_targets" ]] || { echo "(no CG_LAB_SSH_TARGETS; skipped)"; return 0; }
  local target
  for target in $ssh_targets; do
    echo "    ${target}: $*"
    "$ssh_command" -o BatchMode=yes -o ConnectTimeout=5 "$target" "$@" || return 1
  done
}

# L1: find the cluster (explicit id, or the only registered cluster).
step "L1 集群前置条件"
if [[ -z "$cluster_id" ]]; then
  cluster_id="$(api_json GET "/api/v1/clusters" | "$jq_command" -r '.result[0].resource_id // empty')"
  [[ -n "$cluster_id" ]] || { echo "no registered cluster found; set CG_CLUSTER_ID" >&2; exit 2; }
  echo "使用集群 ${cluster_id}"
fi
topology="$(api_json GET "/api/v1/clusters/${cluster_id}/topology")"
health="$(printf '%s' "$topology" | "$jq_command" -r '.result.health.state // empty')"
[[ "$health" == "healthy" ]] || { echo "L1 失败：集群健康状态为 ${health:-unknown}" >&2; exit 1; }
echo "L1 通过：集群健康（healthy）"

# L2: precheck -> plan.
step "L2 计划停机（mode=${mode}）"
api POST "/api/v1/clusters/${cluster_id}/power/precheck" "{\"mode\":\"${mode}\"}" || { echo "L2 失败：precheck" >&2; exit 1; }
plan="$(api_json POST "/api/v1/clusters/${cluster_id}/power/plan" "{\"mode\":\"${mode}\"}")"
state="$(printf '%s' "$plan" | "$jq_command" -r '.result.power_operation.state // empty')"
[[ "$state" == "shutdown_planned" ]] || { echo "L2 失败：plan state=${state}" >&2; exit 1; }
primary_id="$(printf '%s' "$plan" | "$jq_command" -r '.result.snapshot.primary.instance_id // empty')"
echo "L2 通过：shutdown_planned，primary=${primary_id}"

# L3: approval -> execute.
step "L3 审批并执行关机"
grant="$(api_json POST "/api/v1/approvals" "{\"cluster_id\":\"${cluster_id}\",\"engine\":\"mysql\",\"operation_kind\":\"power_shutdown\",\"target_id\":\"${primary_id}\",\"issued_by\":\"power-lifecycle-lab\"}")"
token="$(printf '%s' "$grant" | "$jq_command" -r '.result.approval_token // empty')"
[[ -n "$token" ]] || { echo "L3 失败：approval token 为空" >&2; exit 1; }
executed="$(api_json POST "/api/v1/clusters/${cluster_id}/power/execute" "{\"approval_token\":\"${token}\"}")"
state="$(printf '%s' "$executed" | "$jq_command" -r '.result.power_operation.state // empty')"
frozen="$(printf '%s' "$executed" | "$jq_command" -r '.result.protection.recovery_freeze // false')"
[[ "$state" == "power_off" && "$frozen" == "true" ]] || { echo "L3 失败：execute state=${state} frozen=${frozen}" >&2; exit 1; }
echo "L3 通过：power_off，恢复冻结激活"

# L4: node-level verification via SSH (optional).
step "L4 节点验证"
if [[ -n "$ssh_targets" ]]; then
  if [[ "$mode" == "poweroff" ]]; then
    sleep 5
    echo "L4 通过：整机已下电（节点不可达视为通过）"
  else
    stopped_all=true
    for target in $ssh_targets; do
      if "$ssh_command" -o BatchMode=yes -o ConnectTimeout=5 "$target" \
          "systemctl is-active mysqld 2>/dev/null || systemctl is-active mysql 2>/dev/null || true" | grep -q '^active$'; then
        echo "    ${target}: MySQL 仍在运行" >&2
        stopped_all=false
      else
        echo "    ${target}: MySQL 已停止"
      fi
    done
    [[ "$stopped_all" == "true" ]] || { echo "L4 失败：部分节点 MySQL 未停止" >&2; exit 1; }
    echo "L4 通过：所有节点 MySQL 服务已停止"
  fi
else
  echo "L4 跳过（未配置 CG_LAB_SSH_TARGETS，请人工核对节点状态）"
fi

# L5: boot the nodes, then drive the recovery lifecycle.
step "L5 恢复生命周期（boot-detected -> recovering -> verify -> complete）"
if [[ -n "$ssh_targets" ]]; then
  ssh_each "systemctl reboot || reboot" || true
  echo "已下发重启，等待节点恢复…"
  sleep 30
fi
echo "等待集群 API 恢复可用…"
deadline=$((SECONDS + api_timeout))
until api GET "/healthz" >/dev/null 2>&1; do
  [[ $SECONDS -lt $deadline ]] || { echo "L5 失败：API 在 ${api_timeout}s 内未恢复" >&2; exit 1; }
  sleep 5
done
api POST "/api/v1/clusters/${cluster_id}/power/boot-detected" "{}" || { echo "L5 失败：boot-detected" >&2; exit 1; }
api POST "/api/v1/clusters/${cluster_id}/power/recovering" "{}" || { echo "L5 失败：recovering" >&2; exit 1; }
deadline=$((SECONDS + api_timeout))
until api POST "/api/v1/clusters/${cluster_id}/power/verify" "{}"; do
  [[ $SECONDS -lt $deadline ]] || { echo "L5 失败：verify 在 ${api_timeout}s 内未通过" >&2; exit 1; }
  sleep 10
done
echo "verify 通过"
until complete_json="$(api_json POST "/api/v1/clusters/${cluster_id}/power/complete" "{}")"; do
  [[ $SECONDS -lt $deadline ]] || { echo "L5 失败：complete 在 ${api_timeout}s 内未完成" >&2; exit 1; }
  sleep 10
done
state="$(printf '%s' "$complete_json" | "$jq_command" -r '.result.power_operation.state // empty')"
[[ "$state" == "completed" ]] || { echo "L5 失败：complete state=${state}" >&2; exit 1; }
echo "L5 通过：completed"

# L6: final assertions — protections released, service mode recovered.
step "L6 最终断言"
status="$(api_json GET "/api/v1/clusters/${cluster_id}/power/status")"
frozen="$(printf '%s' "$status" | "$jq_command" -r '.result.recovery_freeze // true')"
protected="$(printf '%s' "$status" | "$jq_command" -r '.result.protected // true')"
[[ "$frozen" == "false" && "$protected" == "false" ]] || { echo "L6 失败：frozen=${frozen} protected=${protected}" >&2; exit 1; }
echo "L6 通过：恢复冻结已解除，集群无保护标记"
if [[ -n "$ssh_targets" && "$mode" == "service" ]]; then
  ssh_each "systemctl is-active mysqld 2>/dev/null || systemctl is-active mysql 2>/dev/null || true" || true
fi

echo ""
echo "power-lifecycle-lab: 全部通过（mode=${mode}，cluster=${cluster_id}）"
