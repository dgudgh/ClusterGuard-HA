#!/usr/bin/env bash
set -euo pipefail

role=""
node_name=""
node_id=""
config_file=""
environment_file=""
agent_config_file=""
assets_dir=""
execute=false
activate_agent_reconcile=false
install_root="${CG_INSTALL_ROOT:-}"
systemctl_binary="${CG_SYSTEMCTL:-systemctl}"

usage() {
  cat <<'EOF'
用法：
  clusterguard-configure --role controller|data|mixed \
    --node-name NAME --node-id UUID --env-file FILE \
    [--config FILE] [--agent-config FILE] [--assets-dir DIR] \
    [--activate-agent-reconcile] [--execute]

默认只执行检查并显示配置计划，不修改系统。
确认计划后追加 --execute 才会写入配置并启动对应服务。
EOF
}

while (($#)); do
  case "$1" in
    --role) role="${2:-}"; shift 2 ;;
    --node-name) node_name="${2:-}"; shift 2 ;;
    --node-id) node_id="${2:-}"; shift 2 ;;
    --config) config_file="${2:-}"; shift 2 ;;
    --env-file) environment_file="${2:-}"; shift 2 ;;
    --agent-config) agent_config_file="${2:-}"; shift 2 ;;
    --assets-dir) assets_dir="${2:-}"; shift 2 ;;
    --activate-agent-reconcile) activate_agent_reconcile=true; shift ;;
    --execute) execute=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数：$1" >&2; usage >&2; exit 2 ;;
  esac
done

target_path() {
  printf '%s%s' "${install_root}" "$1"
}

require_regular_file() {
  local path="$1" message="$2"
  [[ -f "${path}" && ! -L "${path}" ]] || { echo "${message}" >&2; exit 2; }
}

case "${role}" in
  controller|data|mixed) ;;
  *) echo "节点角色必须是 controller、data 或 mixed" >&2; exit 2 ;;
esac
[[ "${node_name}" =~ ^[a-z][a-z0-9-]{2,62}$ ]] ||
  { echo "固定节点名必须是 3 到 63 位小写全局唯一名称" >&2; exit 2; }
[[ "${node_id}" =~ ^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$ ]] ||
  { echo "节点 ID 必须是永久不变的平台 UUID" >&2; exit 2; }
require_regular_file "${environment_file}" "必须提供受保护的环境变量文件"
if find "${environment_file}" -perm /077 -print -quit 2>/dev/null | grep -q .; then
  echo "环境变量文件权限过宽，请执行 chmod 600 ${environment_file}" >&2
  exit 3
fi

while IFS= read -r line || [[ -n "${line}" ]]; do
  line="${line#"${line%%[![:space:]]*}"}"
  [[ -z "${line}" || "${line}" == \#* ]] && continue
  [[ "${line}" =~ ^[A-Za-z_][A-Za-z0-9_]*= ]] ||
    { echo "环境变量文件包含无效行；只允许 NAME=value" >&2; exit 3; }
done <"${environment_file}"

if [[ "${role}" == "controller" || "${role}" == "mixed" ]]; then
  require_regular_file "${config_file}" "控制节点必须提供 --config"
fi
if [[ "${role}" == "data" || "${role}" == "mixed" ]]; then
  require_regular_file "${agent_config_file}" "数据节点必须提供 --agent-config"
fi
if [[ -n "${assets_dir}" ]]; then
  [[ -d "${assets_dir}" && ! -L "${assets_dir}" ]] ||
    { echo "运行资产目录无效" >&2; exit 2; }
  [[ -z "$(find "${assets_dir}" -mindepth 1 -type l -print -quit)" ]] ||
    { echo "运行资产目录不能包含符号链接" >&2; exit 3; }
  [[ -z "$(find "${assets_dir}" -mindepth 3 -print -quit)" ]] ||
    { echo "运行资产目录只允许一层分类目录" >&2; exit 3; }
  while IFS= read -r -d '' asset; do
    relative="${asset#"${assets_dir}"/}"
    case "${relative}" in
      ._*|*/._*|.DS_Store|*/.DS_Store) continue ;;
      tls/*.crt|tls/*.key|ssh/*known_hosts|ssh/*_ed25519|mysql/*-client.cnf|postgresql/*.pass) ;;
      *) echo "不支持的运行资产：${relative}" >&2; exit 3 ;;
    esac
  done < <(find "${assets_dir}" -mindepth 2 -maxdepth 2 -type f -print0 | sort -z)
fi

jq_binary="${CG_JQ_BINARY:-/usr/local/libexec/jq-linux-amd64}"
if [[ ! -x "${jq_binary}" ]]; then
  jq_binary="$(command -v jq 2>/dev/null || true)"
fi
[[ -x "${jq_binary}" ]] || { echo "缺少 JSON 校验工具 jq" >&2; exit 3; }
[[ -z "${config_file}" ]] || "${jq_binary}" -e 'type == "object"' "${config_file}" >/dev/null
[[ -z "${agent_config_file}" ]] || "${jq_binary}" -e 'type == "object"' "${agent_config_file}" >/dev/null

if [[ "${CG_CONFIGURE_SKIP_RUNTIME:-0}" != "1" ]]; then
  [[ -x /usr/local/bin/clusterguard ]] || { echo "未安装 clusterguard RPM" >&2; exit 3; }
  [[ -x /usr/local/bin/cgctl ]] || { echo "未安装 cgctl" >&2; exit 3; }
  [[ -x /usr/local/bin/clusterguard-agent ]] || { echo "未安装 clusterguard-agent" >&2; exit 3; }
  if [[ "${role}" == "controller" || "${role}" == "mixed" ]]; then
    if "${jq_binary}" -e '.mysql.enabled == true' "${config_file}" >/dev/null; then
      command -v mysql >/dev/null 2>&1 || [[ -x /usr/local/mysql/bin/mysql ]] ||
        { echo "启用 MySQL 前必须离线安装 mysql 客户端" >&2; exit 3; }
    fi
    if "${jq_binary}" -e '.postgresql.enabled == true' "${config_file}" >/dev/null; then
      command -v psql >/dev/null 2>&1 || [[ -x /usr/pgsql-16/bin/psql || -x /usr/lib/postgresql/16/bin/psql ]] ||
        { echo "启用 PostgreSQL 前必须离线安装 psql 客户端" >&2; exit 3; }
    fi
    if "${jq_binary}" -e '.oracle.enabled == true and .agent.enabled != true' "${config_file}" >/dev/null; then
      command -v sqlplus >/dev/null 2>&1 && command -v dgmgrl >/dev/null 2>&1 ||
        { echo "Oracle 直连模式必须离线安装 sqlplus 和 dgmgrl" >&2; exit 3; }
    fi
    if "${jq_binary}" -e '.sqlserver.enabled == true' "${config_file}" >/dev/null; then
      command -v sqlcmd >/dev/null 2>&1 || [[ -x /opt/mssql-tools18/bin/sqlcmd || -x /opt/mssql-tools/bin/sqlcmd ]] ||
        { echo "启用 SQL Server 前必须离线安装 sqlcmd" >&2; exit 3; }
    fi
  fi
  if [[ "${role}" == "data" || "${role}" == "mixed" ]]; then
    ip_binary="$("${jq_binary}" -r '.ip_binary // "/sbin/ip"' "${agent_config_file}")"
    arping_binary="$("${jq_binary}" -r '.arping_binary // "/usr/sbin/arping"' "${agent_config_file}")"
    [[ -x "${ip_binary}" ]] || { echo "找不到配置的 ip 命令：${ip_binary}" >&2; exit 3; }
    [[ -x "${arping_binary}" ]] || { echo "找不到配置的 arping 命令：${arping_binary}" >&2; exit 3; }
  fi
fi

cat <<EOF
ClusterGuard HA RPM 配置计划
节点：${node_name}
节点 ID：${node_id}
角色：${role}
控制配置：${config_file:-不需要}
Agent 配置：${agent_config_file:-不需要}
安全资产：${assets_dir:-未提供}
VIP 自动收敛：$([[ "${activate_agent_reconcile}" == "true" ]] && printf '安装后启用' || printf '保持禁用')
执行模式：$([[ "${execute}" == "true" ]] && printf '写入并启动' || printf '仅检查')
EOF

if [[ "${execute}" != "true" ]]; then
  printf '\n检查通过，系统未发生变化。确认后使用相同参数追加 --execute。\n'
  exit 0
fi
if [[ -z "${install_root}" && "$(id -u)" -ne 0 ]]; then
  echo "正式配置必须使用 root 执行" >&2
  exit 4
fi

install_file() {
  local mode="$1" source="$2" destination="$3"
  local target
  target="$(target_path "${destination}")"
  mkdir -p "$(dirname "${target}")"
  install -m "${mode}" "${source}" "${target}"
}

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup_root="$(target_path "/var/lib/clusterguard/config-backups/${timestamp}")"
mkdir -p "${backup_root}"
chmod 0700 "${backup_root}"
configuration_paths=(
  /etc/clusterguard/clusterguard.json \
  /etc/clusterguard/clusterguard.env \
  /etc/clusterguard/agent.json \
  /etc/clusterguard/agent.env \
  /etc/clusterguard/node.json
)
for path in "${configuration_paths[@]}"; do
  source_path="$(target_path "${path}")"
  if [[ -f "${source_path}" ]]; then
    cp -p "${source_path}" "${backup_root}/$(basename "${path}")"
  fi
done
installed_asset_paths=()

restore_configuration() {
  local path target backup relative
  for path in "${configuration_paths[@]}"; do
    target="$(target_path "${path}")"
    backup="${backup_root}/$(basename "${path}")"
    if [[ -f "${backup}" ]]; then
      mkdir -p "$(dirname "${target}")"
      cp -p "${backup}" "${target}"
    else
      rm -f "${target}"
    fi
  done
  if ((${#installed_asset_paths[@]} > 0)); then
    for relative in "${installed_asset_paths[@]}"; do
      target="$(target_path "/etc/clusterguard/${relative}")"
      backup="${backup_root}/assets/${relative}"
      if [[ -f "${backup}" ]]; then
        mkdir -p "$(dirname "${target}")"
        cp -p "${backup}" "${target}"
      else
        rm -f "${target}"
      fi
    done
  fi
}

recover_services_after_rollback() {
  "${systemctl_binary}" daemon-reload >/dev/null 2>&1 || :
  if [[ "${role}" == "controller" || "${role}" == "mixed" ]]; then
    "${systemctl_binary}" try-restart clusterguard-ha.service >/dev/null 2>&1 || :
  fi
  if [[ "${role}" == "data" || "${role}" == "mixed" ]]; then
    "${systemctl_binary}" try-restart clusterguard-agent.service >/dev/null 2>&1 || :
  fi
}

install -d -m 0750 "$(target_path /etc/clusterguard)"
install -d -m 0750 "$(target_path /var/lib/clusterguard)" "$(target_path /var/log/clusterguard)"
install -d -m 0700 "$(target_path /var/lib/clusterguard-agent)"

if [[ "${role}" == "controller" || "${role}" == "mixed" ]]; then
  install_file 0640 "${config_file}" /etc/clusterguard/clusterguard.json
  install_file 0640 "${environment_file}" /etc/clusterguard/clusterguard.env
fi
if [[ "${role}" == "data" || "${role}" == "mixed" ]]; then
  install_file 0640 "${agent_config_file}" /etc/clusterguard/agent.json
  install_file 0600 "${environment_file}" /etc/clusterguard/agent.env
fi
printf '{"resource_id":"%s","node_name":"%s","role":"%s"}\n' "${node_id}" "${node_name}" "${role}" \
  >"$(target_path /etc/clusterguard/node.json)"
chmod 0640 "$(target_path /etc/clusterguard/node.json)"

if [[ -n "${assets_dir}" ]]; then
  while IFS= read -r -d '' asset; do
    relative="${asset#"${assets_dir}"/}"
    case "${relative}" in
      ._*|*/._*|.DS_Store|*/.DS_Store) continue ;;
      tls/*.crt|ssh/*known_hosts) mode=0644 ;;
      tls/*.key) mode=0640 ;;
      ssh/*_ed25519|mysql/*-client.cnf|postgresql/*.pass) mode=0600 ;;
      *) echo "不支持的运行资产：${relative}" >&2; exit 3 ;;
    esac
    installed_asset_paths+=("${relative}")
    existing_asset="$(target_path "/etc/clusterguard/${relative}")"
    if [[ -f "${existing_asset}" ]]; then
      mkdir -p "${backup_root}/assets/$(dirname "${relative}")"
      cp -p "${existing_asset}" "${backup_root}/assets/${relative}"
    fi
    install_file "${mode}" "${asset}" "/etc/clusterguard/${relative}"
  done < <(find "${assets_dir}" -mindepth 2 -maxdepth 2 -type f -print0 | sort -z)
fi

if [[ -z "${install_root}" ]]; then
  chown root:clusterguard /etc/clusterguard
  [[ ! -f /etc/clusterguard/clusterguard.json ]] || chown root:clusterguard /etc/clusterguard/clusterguard.json
  [[ ! -f /etc/clusterguard/clusterguard.env ]] || chown root:clusterguard /etc/clusterguard/clusterguard.env
  chown root:clusterguard /etc/clusterguard/node.json
  [[ ! -d /etc/clusterguard/tls ]] || chown -R root:clusterguard /etc/clusterguard/tls
  [[ ! -d /etc/clusterguard/ssh ]] || chown -R root:clusterguard /etc/clusterguard/ssh
  if [[ -d /etc/clusterguard/ssh ]]; then
    find /etc/clusterguard/ssh -maxdepth 1 -type f -name '*_ed25519' \
      -exec chown clusterguard:clusterguard {} + \
      -exec chmod 0600 {} +
  fi
  chown -R clusterguard:clusterguard /var/lib/clusterguard /var/log/clusterguard
fi

activate_services() {
  "${systemctl_binary}" daemon-reload || return 1
  if [[ "${role}" == "controller" || "${role}" == "mixed" ]]; then
    "${systemctl_binary}" restart clusterguard-ha.service || return 1
    "${systemctl_binary}" enable clusterguard-ha.service || return 1
  fi
  if [[ "${role}" == "data" || "${role}" == "mixed" ]]; then
    "${systemctl_binary}" restart clusterguard-agent.service || return 1
    "${systemctl_binary}" enable clusterguard-agent.service || return 1
    if [[ "${activate_agent_reconcile}" == "true" ]]; then
      "${systemctl_binary}" enable --now clusterguard-agent-reconcile.timer || return 1
    else
      "${systemctl_binary}" disable --now clusterguard-agent-reconcile.timer || return 1
    fi
  fi
}

if ! activate_services; then
  restore_configuration
  recover_services_after_rollback
  echo "服务激活失败，已自动恢复原配置；失败版本保留在 ${backup_root}" >&2
  exit 5
fi

printf '\n配置完成。备份目录：%s\n' "${backup_root}"
