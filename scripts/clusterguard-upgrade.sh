#!/usr/bin/env bash
set -euo pipefail
umask 077

declare -a original_arguments=("$@")
patch_file=""
trust_key=""
state_file="${PWD}/clusterguard-deployment-state.json"
controllers_raw=""
data_nodes_raw=""
ssh_user="root"
ssh_password=""
ssh_passwords_raw=""
ssh_credentials_file=""
ssh_key=""
ssh_port=22
known_hosts=""
accept_host_keys=false
api_port=3000
inspect_only=false
execute=false
rollback_requested=false
assume_yes=false
resume_requested=false
remote_stage="/var/lib/clusterguard/update-history"
control_status_path="/api/v1/control-plane/status"
maintenance_marker="/etc/clusterguard/update-maintenance.json"
update_locks_acquired=false
retain_update_locks=false

declare -a controllers=()
declare -a data_nodes=()
declare -a all_nodes=()
declare -a ordered_nodes=()
declare -a updated_nodes=()
declare -a locked_nodes=()
declare -a node_passwords=()

work_dir=""
patch_root=""
patch_id=""
source_version=""
target_version=""
source_rpm=""
target_rpm=""
source_sha=""
target_sha=""
rpm_architecture=""
minimum_state_format=""
maximum_state_format=""
source_state_format=""
source_update_protocol=""
target_state_format=""
target_update_protocol=""
source_product_version=""
source_release=""
target_product_version=""
target_release=""
desired_version=""
desired_rpm=""
desired_product_version=""
desired_release=""
desired_state_format=""
desired_update_protocol=""
rollback_version=""
rollback_rpm=""
leader_host=""
current_password=""
known_hosts_file=""
ssh_auth_mode="key"
journal_file=""
journal_events_file=""
upgrade_lock_name=""
configured_data_members=""
configured_data_addresses=""
update_root="/var/lib/clusterguard/updates"
update_mode="execute"
progress_replication_enabled=false
bootstrap_available=false
bootstrap_entrypoint=""
bootstrap_sha=""
bootstrap_protocol=0
bootstrap_depth="${CG_UPDATE_BOOTSTRAP_DEPTH:-0}"
cluster_idle_attempts="${CG_UPDATE_CLUSTER_IDLE_ATTEMPTS:-30}"
cluster_idle_delay_seconds="${CG_UPDATE_CLUSTER_IDLE_DELAY_SECONDS:-2}"

[[ "${cluster_idle_attempts}" =~ ^[1-9][0-9]*$ ]] || { printf 'CG_UPDATE_CLUSTER_IDLE_ATTEMPTS 必须为正整数\n' >&2; exit 1; }
[[ "${cluster_idle_delay_seconds}" =~ ^[0-9]+$ ]] || { printf 'CG_UPDATE_CLUSTER_IDLE_DELAY_SECONDS 必须为非负整数\n' >&2; exit 1; }
[[ "${bootstrap_depth}" == 0 || "${bootstrap_depth}" == 1 ]] || { printf 'CG_UPDATE_BOOTSTRAP_DEPTH 必须为 0 或 1\n' >&2; exit 1; }

timestamp() { date '+%Y-%m-%d %H:%M:%S'; }
log() { printf '[%s] %s\n' "$(timestamp)" "$*"; }
die() { printf '[%s] 升级失败：%s\n' "$(timestamp)" "$*" >&2; exit 1; }
need_value() { (($# >= 2)) && [[ -n "${2:-}" ]] || die "参数 $1 缺少值"; }

usage() {
  cat <<'EOF'
ClusterGuard HA 客户现场签名升级包与滚动升级器

检查升级包：
  clusterguard-upgrade --package FILE --trust-key PUBLIC.pem --inspect

滚动升级：
  clusterguard-upgrade --package FILE --trust-key PUBLIC.pem \
    --state clusterguard-deployment-state.json -u root -P 'SSH密码' \
    --accept-host-keys --execute

选项：
  --package FILE                签名 .cgupgrade 升级包
  --patch FILE                  兼容旧命令的别名
  --trust-key FILE              预先交付并可信保存的升级包签名公钥
  --inspect                     只验证签名、摘要和兼容合同
  --state FILE                  原安装器生成的部署状态 JSON
  --controllers LIST            显式控制节点列表；优先于状态文件
  --data-nodes LIST             显式数据节点列表；优先于状态文件
  -u, --ssh-user USER           SSH 用户，默认 root
  -P, --ssh-password PASS       所有节点共用密码
  -p, --ssh-passwords LIST      按节点顺序提供的逗号分隔密码
  --ssh-credentials-file FILE   每行“节点=密码”，文件权限必须为 0600
  --ssh-key FILE                SSH 私钥
  --ssh-port PORT               SSH 端口，默认 22
  --known-hosts FILE            已审核的 known_hosts
  --accept-host-keys            首次采集当前节点 SSH 主机密钥
  --api-port PORT               控制面 API 端口，默认 3000
  --update-root DIR             控制面升级状态目录，默认 /var/lib/clusterguard/updates
  --plan                        输出升级顺序但不改节点（默认）
  --execute                     真实滚动升级
  --rollback                    使用升级包内回退 RPM 执行受控回退
  --resume                      续跑同一签名升级包留下的维护锁与已升级节点
  -y, --yes                     非交互确认

升级固定顺序为 followers -> data-only -> leader。每一步都重新验证多数派、
就绪状态、活动操作和节点任务；任何失败都会停止并用内置旧 RPM 自动回退。
升级器不安装、停止、启动或修改任何数据库软件与数据目录。
EOF
}

while (($#)); do
  case "$1" in
    --package|--patch) need_value "$@"; patch_file="$2"; shift 2 ;;
    --trust-key) need_value "$@"; trust_key="$2"; shift 2 ;;
    --inspect) inspect_only=true; shift ;;
    --state) need_value "$@"; state_file="$2"; shift 2 ;;
    --controllers) need_value "$@"; controllers_raw="$2"; shift 2 ;;
    --data-nodes) need_value "$@"; data_nodes_raw="$2"; shift 2 ;;
    -u|--ssh-user) need_value "$@"; ssh_user="$2"; shift 2 ;;
    -P|--ssh-password) need_value "$@"; ssh_password="$2"; shift 2 ;;
    -p|--ssh-passwords) need_value "$@"; ssh_passwords_raw="$2"; shift 2 ;;
    --ssh-credentials-file) need_value "$@"; ssh_credentials_file="$2"; shift 2 ;;
    --ssh-key) need_value "$@"; ssh_key="$2"; shift 2 ;;
    --ssh-port) need_value "$@"; ssh_port="$2"; shift 2 ;;
    --known-hosts) need_value "$@"; known_hosts="$2"; shift 2 ;;
    --accept-host-keys) accept_host_keys=true; shift ;;
    --api-port) need_value "$@"; api_port="$2"; shift 2 ;;
    --update-root) need_value "$@"; update_root="$2"; shift 2 ;;
    --remote-stage) need_value "$@"; remote_stage="$2"; shift 2 ;;
    --plan) execute=false; shift ;;
    --execute) execute=true; shift ;;
    --rollback) rollback_requested=true; shift ;;
    --resume) resume_requested=true; shift ;;
    -y|--yes) assume_yes=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数：$1" ;;
  esac
done

command -v jq >/dev/null 2>&1 || die "需要 jq"
command -v openssl >/dev/null 2>&1 || die "需要 openssl"
command -v tar >/dev/null 2>&1 || die "需要 tar"
[[ -f "${patch_file}" && ! -L "${patch_file}" ]] || die "升级包不存在或不是普通文件"
[[ -f "${trust_key}" && ! -L "${trust_key}" ]] || die "可信签名公钥不存在或不是普通文件"
[[ "${ssh_port}" =~ ^[0-9]+$ && "${ssh_port}" -ge 1 && "${ssh_port}" -le 65535 ]] || die "SSH 端口无效"
[[ "${api_port}" =~ ^[0-9]+$ && "${api_port}" -ge 1 && "${api_port}" -le 65535 ]] || die "API 端口无效"
[[ "${ssh_user}" =~ ^[A-Za-z_][A-Za-z0-9_.-]*$ ]] || die "SSH 用户名格式无效"
[[ "${remote_stage}" =~ ^/[A-Za-z0-9._/-]+$ && "${remote_stage}" != *"//"* && "${remote_stage}" != *"/../"* && "${remote_stage}" != */.. ]] ||
  die "远端暂存目录必须是无空格、无相对跳转的绝对路径"
[[ "${update_root}" =~ ^/[A-Za-z0-9._/-]+$ && "${update_root}" != *"//"* && "${update_root}" != *"/../"* && "${update_root}" != */.. ]] ||
  die "升级状态目录必须是无空格、无相对跳转的绝对路径"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

safe_extract_patch() {
  local entry listing
  listing="$(tar -tzf "${patch_file}")" || die "升级包归档无法读取"
  [[ -n "${listing}" ]] || die "升级包归档为空"
  while IFS= read -r entry; do
    [[ "${entry}" == clusterguard-patch || "${entry}" == clusterguard-patch/* ]] || die "升级包包含范围外路径：${entry}"
    [[ "${entry}" != /* && "${entry}" != *"../"* && "${entry}" != *"/.." && "${entry}" != *"//"* ]] ||
      die "升级包包含不安全路径：${entry}"
  done <<<"${listing}"
  if tar -tvzf "${patch_file}" | awk '$1 ~ /^[lh]/ {found=1} END {exit found ? 0 : 1}'; then
    die "升级包禁止包含符号链接或硬链接"
  fi
  work_dir="$(mktemp -d /tmp/clusterguard-upgrade.XXXXXX)"
  chmod 0700 "${work_dir}"
  tar -xzf "${patch_file}" -C "${work_dir}"
  patch_root="${work_dir}/clusterguard-patch"
}

verify_patch() {
  local manifest signature checksums manifest_sha actual_source actual_target actual_bootstrap
  manifest="${patch_root}/PATCH-MANIFEST.json"
  signature="${patch_root}/PATCH-MANIFEST.sig"
  checksums="${patch_root}/SHA256SUMS"
  [[ -f "${manifest}" && ! -L "${manifest}" && -f "${signature}" && ! -L "${signature}" && -f "${checksums}" && ! -L "${checksums}" ]] ||
    die "补丁缺少签名清单"
  openssl dgst -sha256 -verify "${trust_key}" -signature "${signature}" "${manifest}" >/dev/null 2>&1 ||
    die "patch signature verification failed"
  jq -e '
    .schema_version == 1 and .product == "ClusterGuard HA" and
    (.patch_id | type == "string" and length > 0) and
    (.source.version | type == "string" and length > 0) and
    (.target.version | type == "string" and length > 0) and
    (.source.release | type == "string" and length > 0) and
    (.target.release | type == "string" and length > 0) and
    .policy.rolling == true and .policy.rollback_supported == true and
    .policy.database_mutation == false and
    .compatibility.minimum_state_format <= .source.state_format and
    .compatibility.maximum_state_format >= .source.state_format and
    .compatibility.target_state_format == .target.state_format and
    .compatibility.update_protocol == .target.update_protocol
  ' "${manifest}" >/dev/null || die "补丁兼容合同无效"

  patch_id="$(jq -r '.patch_id' "${manifest}")"
  source_version="$(jq -r '.source.version + "-" + .source.release' "${manifest}")"
  target_version="$(jq -r '.target.version + "-" + .target.release' "${manifest}")"
  source_product_version="$(jq -r '.source.version' "${manifest}")"
  source_release="$(jq -r '.source.release' "${manifest}")"
  target_product_version="$(jq -r '.target.version' "${manifest}")"
  target_release="$(jq -r '.target.release' "${manifest}")"
  source_state_format="$(jq -r '.source.state_format' "${manifest}")"
  source_update_protocol="$(jq -r '.source.update_protocol' "${manifest}")"
  target_state_format="$(jq -r '.target.state_format' "${manifest}")"
  target_update_protocol="$(jq -r '.target.update_protocol' "${manifest}")"
  minimum_state_format="$(jq -r '.compatibility.minimum_state_format' "${manifest}")"
  maximum_state_format="$(jq -r '.compatibility.maximum_state_format' "${manifest}")"
  source_rpm="$(jq -r '.source.rpm' "${manifest}")"
  target_rpm="$(jq -r '.target.rpm' "${manifest}")"
  source_sha="$(jq -r '.source.sha256' "${manifest}")"
  target_sha="$(jq -r '.target.sha256' "${manifest}")"
  rpm_architecture="$(jq -r '.target.rpm_architecture' "${manifest}")"
  [[ "${patch_id}" =~ ^[A-Za-z0-9._-]+$ ]] || die "patch id 格式无效"
  [[ "${source_rpm}" == "$(basename "${source_rpm}")" && "${target_rpm}" == "$(basename "${target_rpm}")" ]] ||
    die "RPM 清单路径无效"
  [[ "${source_rpm}" == clusterguard-ha-*.rpm && "${target_rpm}" == clusterguard-ha-*.rpm ]] || die "RPM 文件名无效"
  [[ "${source_sha}" =~ ^[0-9a-f]{64}$ && "${target_sha}" =~ ^[0-9a-f]{64}$ ]] || die "RPM checksum 格式无效"
  [[ -f "${patch_root}/payload/${source_rpm}" && ! -L "${patch_root}/payload/${source_rpm}" ]] || die "补丁缺少回退 RPM"
  [[ -f "${patch_root}/payload/${target_rpm}" && ! -L "${patch_root}/payload/${target_rpm}" ]] || die "补丁缺少目标 RPM"
  actual_source="$(sha256_file "${patch_root}/payload/${source_rpm}")"
  actual_target="$(sha256_file "${patch_root}/payload/${target_rpm}")"
  [[ "${actual_source}" == "${source_sha}" ]] || die "rollback RPM checksum mismatch"
  [[ "${actual_target}" == "${target_sha}" ]] || die "target RPM checksum mismatch"
  manifest_sha="$(sha256_file "${manifest}")"
  grep -Fqx "${manifest_sha}  PATCH-MANIFEST.json" "${checksums}" || die "manifest checksum mismatch"

  if jq -e 'has("bootstrap")' "${manifest}" >/dev/null; then
    jq -e '
      .bootstrap.protocol == 1 and
      .bootstrap.entrypoint == "bootstrap/clusterguard-upgrade.sh" and
      (.bootstrap.sha256 | type == "string" and test("^[0-9a-f]{64}$"))
    ' "${manifest}" >/dev/null || die "补丁引导升级合同无效"
    bootstrap_protocol="$(jq -r '.bootstrap.protocol' "${manifest}")"
    bootstrap_entrypoint="$(jq -r '.bootstrap.entrypoint' "${manifest}")"
    bootstrap_sha="$(jq -r '.bootstrap.sha256' "${manifest}")"
    [[ -f "${patch_root}/${bootstrap_entrypoint}" && ! -L "${patch_root}/${bootstrap_entrypoint}" ]] ||
      die "补丁缺少签名引导升级器"
    actual_bootstrap="$(sha256_file "${patch_root}/${bootstrap_entrypoint}")"
    [[ "${actual_bootstrap}" == "${bootstrap_sha}" ]] || die "bootstrap upgrader checksum mismatch"
    grep -Fqx "${bootstrap_sha}  ${bootstrap_entrypoint}" "${checksums}" || die "bootstrap upgrader checksum manifest mismatch"
    bootstrap_available=true
  fi
}

cleanup() {
  if ${update_locks_acquired} && ! ${retain_update_locks} && declare -F release_update_locks >/dev/null 2>&1; then
    release_update_locks || true
  fi
  [[ -z "${work_dir}" || ! -d "${work_dir}" ]] || rm -rf "${work_dir}"
  [[ -z "${known_hosts_file}" || "${known_hosts_file}" == "${known_hosts}" || ! -f "${known_hosts_file}" ]] || rm -f "${known_hosts_file}"
}
trap cleanup EXIT

safe_extract_patch
verify_patch

if ${inspect_only}; then
  printf 'signature=verified\n'
  printf 'patch_id=%s\n' "${patch_id}"
  printf 'source=%s\n' "${source_version}"
  printf 'target=%s\n' "${target_version}"
  printf 'architecture=%s\n' "${rpm_architecture}"
  printf 'rollback=available\n'
  printf 'rolling=true\n'
  printf 'database_mutation=false\n'
  if ${bootstrap_available}; then
    printf 'bootstrap=available\n'
    printf 'bootstrap_protocol=%s\n' "${bootstrap_protocol}"
  else
    printf 'bootstrap=unavailable\n'
    printf 'bootstrap_protocol=0\n'
  fi
  exit 0
fi

if ${bootstrap_available} && [[ "${bootstrap_depth}" == 0 ]]; then
  log "签名引导升级器校验通过，切换到升级包内执行器"
  set +e
  CG_UPDATE_BOOTSTRAP_DEPTH=1 bash "${patch_root}/${bootstrap_entrypoint}" "${original_arguments[@]}"
  bootstrap_exit=$?
  set -e
  exit "${bootstrap_exit}"
fi

if ${rollback_requested}; then
  update_mode="rollback"
elif ${resume_requested}; then
  update_mode="resume"
fi

append_unique() {
  local value="$1" existing
  [[ -n "${value}" ]] || return 0
  [[ "${value}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || die "节点地址格式无效：${value}"
  for existing in "${all_nodes[@]:-}"; do [[ "${existing}" != "${value}" ]] || return 0; done
  all_nodes[${#all_nodes[@]}]="${value}"
}

csv_to_array() {
  local raw="$1" destination="$2" value
  local -a parsed=()
  IFS=',' read -r -a parsed <<<"${raw}"
  for value in "${parsed[@]}"; do
    value="$(printf '%s' "${value}" | xargs)"
    [[ -n "${value}" ]] || continue
    [[ "${value}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || die "节点地址格式无效：${value}"
    if [[ "${destination}" == "controllers" ]]; then controllers[${#controllers[@]}]="${value}"; else data_nodes[${#data_nodes[@]}]="${value}"; fi
  done
}

load_nodes() {
  local address role
  if [[ -f "${state_file}" ]]; then
    jq -e '.nodes | type == "array" and length > 0' "${state_file}" >/dev/null || die "部署状态文件缺少 nodes"
    while IFS=$'\t' read -r address role; do
      [[ -n "${address}" ]] || continue
      [[ "${address}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || die "部署状态包含无效节点地址：${address}"
      if [[ -z "${controllers_raw}" ]]; then
        case "${role}" in
          controller|mixed) controllers[${#controllers[@]}]="${address}" ;;
        esac
      fi
      if [[ -z "${data_nodes_raw}" ]]; then
        case "${role}" in
          data|mixed) data_nodes[${#data_nodes[@]}]="${address}" ;;
        esac
      fi
    done < <(jq -r '.nodes[] | [.address, .role] | @tsv' "${state_file}")
  fi
  [[ -z "${controllers_raw}" ]] || csv_to_array "${controllers_raw}" controllers
  [[ -z "${data_nodes_raw}" ]] || csv_to_array "${data_nodes_raw}" data
  ((${#controllers[@]} >= 3 && ${#controllers[@]} % 2 == 1)) || die "控制节点必须为至少 3 个的奇数"
  for address in "${controllers[@]}" "${data_nodes[@]}"; do append_unique "${address}"; done
  ((${#all_nodes[@]} > 0)) || die "没有可升级节点"
}

configure_passwords() {
  local permissions index host found line_host line_password
  [[ -z "${ssh_key}" || ( -z "${ssh_password}" && -z "${ssh_passwords_raw}" && -z "${ssh_credentials_file}" ) ]] ||
    die "SSH 私钥不能与密码参数同时使用"
  [[ -z "${ssh_passwords_raw}" || ( -z "${ssh_password}" && -z "${ssh_credentials_file}" ) ]] ||
    die "-p 不能与 -P 或凭据文件同时使用"
  if [[ -n "${ssh_credentials_file}" ]]; then
    [[ -f "${ssh_credentials_file}" && ! -L "${ssh_credentials_file}" ]] || die "SSH 凭据文件无效"
    permissions="$(stat -c '%a' "${ssh_credentials_file}" 2>/dev/null || stat -f '%Lp' "${ssh_credentials_file}")"
    [[ "${permissions}" == "600" ]] || die "SSH 凭据文件权限必须为 0600"
  fi
  if [[ -n "${ssh_passwords_raw}" ]]; then
    IFS=',' read -r -a node_passwords <<<"${ssh_passwords_raw}"
    ((${#node_passwords[@]} == ${#all_nodes[@]})) || die "-p 密码数量必须与去重后的节点数量一致"
  fi
  if [[ -n "${ssh_password}" || -n "${ssh_passwords_raw}" || -n "${ssh_credentials_file}" ]]; then
    command -v sshpass >/dev/null 2>&1 || die "密码 SSH 需要 sshpass；生产环境建议使用 --ssh-key"
    ssh_auth_mode="sshpass"
  fi
  for ((index=0; index<${#all_nodes[@]}; index++)); do
    host="${all_nodes[${index}]}"
    if [[ -n "${ssh_passwords_raw}" ]]; then continue; fi
    if [[ -n "${ssh_credentials_file}" ]]; then
      found=false
      while IFS='=' read -r line_host line_password; do
        [[ "${line_host}" == "${host}" ]] || continue
        node_passwords[${index}]="${line_password}"; found=true; break
      done <"${ssh_credentials_file}"
      ${found} || die "SSH 凭据文件缺少节点 ${host}"
    else
      node_passwords[${index}]="${ssh_password}"
    fi
  done
}

password_for_host() {
  local host="$1" index
  current_password=""
  for ((index=0; index<${#all_nodes[@]}; index++)); do
    if [[ "${all_nodes[${index}]}" == "${host}" ]]; then current_password="${node_passwords[${index}]:-}"; return 0; fi
  done
  return 1
}

configure_known_hosts() {
  local host
  if [[ -n "${known_hosts}" ]]; then
    [[ -f "${known_hosts}" && ! -L "${known_hosts}" ]] || die "known_hosts 文件无效"
    known_hosts_file="${known_hosts}"
    return
  fi
  if ${accept_host_keys}; then
    command -v ssh-keyscan >/dev/null 2>&1 || die "需要 ssh-keyscan"
    known_hosts_file="$(mktemp /tmp/clusterguard-upgrade-known-hosts.XXXXXX)"
    chmod 0600 "${known_hosts_file}"
    for host in "${all_nodes[@]}"; do
      ssh-keyscan -p "${ssh_port}" -T 5 -H "${host}" >>"${known_hosts_file}" 2>/dev/null || die "无法采集 ${host} 的 SSH 主机密钥"
    done
  else
    known_hosts_file="${HOME}/.ssh/known_hosts"
    [[ -f "${known_hosts_file}" ]] || die "请提供 --known-hosts 或显式使用 --accept-host-keys"
  fi
}

remote_run() {
  local host="$1" command="$2"
  local -a options=(-o ConnectTimeout=10 -o ServerAliveInterval=5 -o ServerAliveCountMax=3 -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${known_hosts_file}")
  [[ -z "${ssh_key}" ]] || options+=(-i "${ssh_key}" -o IdentitiesOnly=yes)
  password_for_host "${host}" || die "找不到 ${host} 的 SSH 凭据"
  if [[ "${ssh_auth_mode}" == "sshpass" ]]; then
    SSHPASS="${current_password}" sshpass -e ssh -p "${ssh_port}" "${options[@]}" -o BatchMode=no "${ssh_user}@${host}" "${command}"
  else
    ssh -p "${ssh_port}" "${options[@]}" -o BatchMode=yes "${ssh_user}@${host}" "${command}"
  fi
}

remote_copy() {
  local host="$1" source="$2" destination="$3"
  local -a options=(-o ConnectTimeout=10 -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${known_hosts_file}")
  [[ -z "${ssh_key}" ]] || options+=(-i "${ssh_key}" -o IdentitiesOnly=yes)
  password_for_host "${host}" || die "找不到 ${host} 的 SSH 凭据"
  if [[ "${ssh_auth_mode}" == "sshpass" ]]; then
    SSHPASS="${current_password}" sshpass -e scp -q -P "${ssh_port}" "${options[@]}" -o BatchMode=no -- "${source}" "${ssh_user}@${host}:${destination}"
  else
    scp -q -P "${ssh_port}" "${options[@]}" -o BatchMode=yes -- "${source}" "${ssh_user}@${host}:${destination}"
  fi
}

load_runtime_data_members() {
  local host identity resource_id role expected_id members=""
  if ((${#data_nodes[@]} == 0)); then
    configured_data_members=""
    configured_data_addresses=""
    return 0
  fi
  for host in "${data_nodes[@]}"; do
    identity="$(remote_run "${host}" "test -s /etc/clusterguard/node.json && cat /etc/clusterguard/node.json")" ||
      die "无法读取数据节点不可变身份：${host}"
    jq -e '
      (.resource_id | type == "string") and
      (.node_name | type == "string" and length > 0) and
      (.role == "data" or .role == "mixed")
    ' <<<"${identity}" >/dev/null || die "数据节点 ${host} 的 node.json 缺失有效 data/mixed 身份"
    resource_id="$(jq -r '.resource_id' <<<"${identity}")"
    role="$(jq -r '.role' <<<"${identity}")"
    [[ "${resource_id}" =~ ^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$ ]] ||
      die "数据节点 ${host} 返回了无效 resource_id"
    if is_controller "${host}"; then
      [[ "${role}" == "mixed" ]] || die "节点 ${host} 同时位于控制和数据清单，但固定身份不是 mixed"
    fi
    if [[ -f "${state_file}" ]]; then
      expected_id="$(jq -r --arg address "${host}" 'first(.nodes[]? | select(.address==$address) | .resource_id) // empty' "${state_file}")"
      [[ -z "${expected_id}" || "${expected_id}" == "${resource_id}" ]] ||
        die "数据节点 ${host} 的部署状态 UUID 与远端固定身份不一致"
    fi
    members="${members}${resource_id}"$'\n'
  done
  configured_data_members="$(printf '%s' "${members}" | sed '/^$/d' | LC_ALL=C sort -u)"
  configured_data_addresses="$(printf '%s\n' "${data_nodes[@]}" | sed '/^$/d' | LC_ALL=C sort -u)"
  [[ -n "${configured_data_members}" ]] || die "数据节点清单为空"
  [[ "$(printf '%s\n' "${configured_data_members}" | sed '/^$/d' | wc -l | tr -d ' ')" == "${#data_nodes[@]}" ]] ||
    die "数据节点清单包含重复的不可变 resource_id"
  [[ "$(printf '%s\n' "${configured_data_addresses}" | sed '/^$/d' | wc -l | tr -d ' ')" == "${#data_nodes[@]}" ]] ||
    die "数据节点清单包含重复地址"
}

control_status() {
  local host="$1"
  # cgctl status is the authenticated client for control_status_path. Keeping
  # the endpoint contract explicit lets packaging and audit tools detect it.
  : "${control_status_path}"
  remote_run "${host}" "set -a; . /etc/clusterguard/clusterguard.env; set +a; /usr/local/bin/cgctl --server https://${host}:${api_port} --ca-file \"\${CG_TLS_CA_FILE:-/etc/clusterguard/tls/ca.crt}\" --json status"
}

verify_cluster_idle() {
	local expected_leader="${1:-}" expected_maintenance="${2:-false}" host status leaders=0 role observed_leader=""
	local local_controller_id live_members current_live_members configured_members live_data_members current_live_data_members
	local live_data_addresses current_live_data_addresses
  [[ "${expected_maintenance}" == "true" || "${expected_maintenance}" == "false" || "${expected_maintenance}" == "any" ]] ||
    die "内部维护状态参数无效"
	live_members=""
	live_data_members=""
	live_data_addresses=""
	configured_members=""
	for host in "${controllers[@]}"; do
		status="$(control_status "${host}")" || die "无法读取控制节点状态：${host}"
    jq -e --arg maintenance "${expected_maintenance}" '
      .status == "ok" and .result.ready == true and .result.leader_known == true and
      (.result.role != "leader" or .result.quorum_confirmed == true) and
      .result.voter_count >= 3 and (.result.voter_count % 2 == 1) and
      .result.active_operations == 0 and .result.indeterminate_operations == 0 and
		  .result.active_lifecycle_tasks == 0 and
		  (.result.controller_members | type == "array") and
		  (.result.controller_members | length) == .result.voter_count and
		  (.result.data_node_members | type == "array") and
		  ((.result.data_node_members | map(.resource_id) | unique | length) == (.result.data_node_members | length)) and
		  ((.result.data_node_members | map(.ip_address) | all(type == "string" and length > 0))) and
		  ($maintenance == "any" or ((.result.update_maintenance_active // false) == ($maintenance == "true")))
		' <<<"${status}" >/dev/null || die "控制面未就绪、无多数派、维护状态不一致或仍有活动任务：${host}"
		local_controller_id="$(jq -r '.result.local_controller_id // empty' <<<"${status}")"
		[[ -n "${local_controller_id}" ]] || die "控制节点 ${host} 未返回不可变控制器 UUID"
		configured_members="${configured_members}${local_controller_id}"$'\n'
		current_live_members="$(jq -r '.result.controller_members[].resource_id' <<<"${status}" | LC_ALL=C sort -u)"
		[[ -n "${current_live_members}" ]] || die "控制节点 ${host} 未返回实时 Raft 成员清单"
		if [[ -z "${live_members}" ]]; then
			live_members="${current_live_members}"
		elif [[ "${live_members}" != "${current_live_members}" ]]; then
			die "控制节点对实时 Raft 成员清单的观测不一致：${host}"
		fi
		current_live_data_members="$(jq -r '.result.data_node_members[].resource_id' <<<"${status}" | LC_ALL=C sort -u)"
		if [[ -z "${live_data_members}" ]]; then
			live_data_members="${current_live_data_members}"
		elif [[ "${live_data_members}" != "${current_live_data_members}" ]]; then
			die "控制节点对活动数据节点清单的观测不一致：${host}"
		fi
		current_live_data_addresses="$(jq -r '.result.data_node_members[].ip_address' <<<"${status}" | LC_ALL=C sort -u)"
		if [[ -z "${live_data_addresses}" ]]; then
			live_data_addresses="${current_live_data_addresses}"
		elif [[ "${live_data_addresses}" != "${current_live_data_addresses}" ]]; then
			die "控制节点对活动数据节点宿主机映射的观测不一致：${host}"
		fi
		role="$(jq -r '.result.role' <<<"${status}")"
    if [[ "${role}" == "leader" ]]; then leaders=$((leaders + 1)); observed_leader="${host}"; fi
	done
	configured_members="$(printf '%s' "${configured_members}" | sed '/^$/d' | LC_ALL=C sort -u)"
	[[ "${configured_members}" == "${live_members}" ]] ||
		die "静态控制节点清单与实时 Raft 成员不一致；请使用当前部署状态或 --controllers 提供全部控制节点后重试"
	[[ "${configured_data_addresses}" == "${live_data_addresses}" ]] ||
		die "静态数据节点地址与实时活动节点宿主机映射不一致；请使用 --data-nodes 提供全部活动宿主机后重试"
	if [[ "${configured_data_members}" != "${live_data_members}" ]]; then
		log "检测到容器数据节点独立逻辑身份；控制节点观测一致，且宿主机映射已严格核对"
	fi
  ((leaders == 1)) || die "控制面必须且只能识别一个 Leader，当前 ${leaders} 个"
  if [[ -n "${expected_leader}" && "${observed_leader}" != "${expected_leader}" ]]; then
    die "升级期间 Leader 意外变化：期望 ${expected_leader}，实际 ${observed_leader}"
  fi
  leader_host="${observed_leader}"
}

wait_cluster_idle() {
  local expected_leader="${1:-}" expected_maintenance="${2:-false}" attempt output=""
  for attempt in $(seq 1 "${cluster_idle_attempts}"); do
    if output="$(trap - EXIT; verify_cluster_idle "${expected_leader}" "${expected_maintenance}" 2>&1)"; then
      return 0
    fi
    if ((attempt == 1 || attempt % 5 == 0)); then
      log "等待控制面收敛（${attempt}/${cluster_idle_attempts}）：${output##*$'\n'}"
    fi
    sleep "${cluster_idle_delay_seconds}"
  done
  log "错误：控制面在 $((cluster_idle_attempts * cluster_idle_delay_seconds)) 秒内未恢复一致：${output##*$'\n'}" >&2
  return 1
}

remote_package_version() {
  remote_run "$1" "rpm -q --qf '%{VERSION}-%{RELEASE}\\n' clusterguard-ha"
}

verify_node_contract() {
  local host="$1" expected_version="$2" expected_release="$3" expected_state="$4" expected_protocol="$5" info
  if ! info="$(remote_run "${host}" "/usr/local/bin/clusterguard --version-json")"; then
    log "错误：${host} 不支持受控补丁版本合同；请先按升级手册执行一次引导升级" >&2
    return 1
  fi
  jq -e \
    --arg version "${expected_version}" \
    --arg release "${expected_release}" \
    --arg architecture "${rpm_architecture}" \
    --argjson state_format "${expected_state}" \
    --argjson update_protocol "${expected_protocol}" \
    --argjson minimum_state_format "${minimum_state_format}" \
    --argjson maximum_state_format "${maximum_state_format}" '
      .product == "ClusterGuard HA" and .binary == "clusterguard" and
      .version == $version and .release == $release and
      .rpm_architecture == $architecture and
      .state_format == $state_format and
      .state_format >= $minimum_state_format and .state_format <= $maximum_state_format and
      .update_protocol == $update_protocol
    ' <<<"${info}" >/dev/null || {
      log "错误：${host} 的二进制版本或兼容合同与补丁清单不一致" >&2
      return 1
    }
}

is_controller() {
  local value="$1" item
  for item in "${controllers[@]}"; do [[ "${item}" != "${value}" ]] || return 0; done
  return 1
}

is_data_node() {
  local value="$1" item
  for item in "${data_nodes[@]}"; do [[ "${item}" != "${value}" ]] || return 0; done
  return 1
}

build_order() {
  local host
  if ${resume_requested}; then
    verify_cluster_idle "" any
  else
    verify_cluster_idle "" false
  fi
  for host in "${controllers[@]}"; do [[ "${host}" == "${leader_host}" ]] || ordered_nodes[${#ordered_nodes[@]}]="${host}"; done
  for host in "${data_nodes[@]}"; do is_controller "${host}" || ordered_nodes[${#ordered_nodes[@]}]="${host}"; done
  ordered_nodes[${#ordered_nodes[@]}]="${leader_host}"
}

acquire_update_locks() {
  local host command
  upgrade_lock_name="${remote_stage}/.cluster-update.lock"
  if ${resume_requested}; then
    command="set -eu; install -d -m 0700 '${remote_stage}'; if mkdir '${upgrade_lock_name}' 2>/dev/null; then printf '%s\\n' '${patch_id}' >'${upgrade_lock_name}/patch-id'; else test \"\$(cat '${upgrade_lock_name}/patch-id' 2>/dev/null)\" = '${patch_id}'; fi; umask 077; printf '%s\\n' '{\"schema_version\":1,\"patch_id\":\"${patch_id}\",\"mode\":\"rolling_update\"}' >'${maintenance_marker}.tmp'; mv -f '${maintenance_marker}.tmp' '${maintenance_marker}'"
  else
    command="set -eu; install -d -m 0700 '${remote_stage}'; mkdir '${upgrade_lock_name}'; umask 077; printf '%s\\n' '${patch_id}' >'${upgrade_lock_name}/patch-id'; printf '%s\\n' '{\"schema_version\":1,\"patch_id\":\"${patch_id}\",\"mode\":\"rolling_update\"}' >'${maintenance_marker}.tmp'; mv -f '${maintenance_marker}.tmp' '${maintenance_marker}'"
  fi
  for host in "${controllers[@]}"; do
    if ! remote_run "${host}" "${command}"; then
      release_update_locks || true
      die "控制节点已有升级任务或无法建立升级锁：${host}"
    fi
    locked_nodes[${#locked_nodes[@]}]="${host}"
    update_locks_acquired=true
  done
}

release_update_locks() {
	local host relock_host release_failed=false relock_failed=false
	((${#locked_nodes[@]} > 0)) || { update_locks_acquired=false; return 0; }
	# Verify every marker first. This prevents a partial unlock when one node has
	# lost its lock identity or is unreachable before release starts.
	for host in "${locked_nodes[@]}"; do
		remote_run "${host}" "set -eu; test \"\$(cat '${upgrade_lock_name}/patch-id')\" = '${patch_id}'; test -f '${maintenance_marker}'; grep -Fq '\"patch_id\":\"${patch_id}\"' '${maintenance_marker}'" >/dev/null 2>&1 || return 1
	done
	for host in "${locked_nodes[@]}"; do
		if ! remote_run "${host}" "set -eu; rm -f '${maintenance_marker}.tmp' '${maintenance_marker}'; rm -rf '${upgrade_lock_name}'" >/dev/null 2>&1; then
			release_failed=true
			break
		fi
	done
	if ${release_failed}; then
		# A failed multi-node release is compensated by restoring the same patch
		# marker everywhere, including nodes already unlocked in this attempt.
		for relock_host in "${locked_nodes[@]}"; do
			if ! remote_run "${relock_host}" "set -eu; install -d -m 0700 '${remote_stage}'; mkdir -p '${upgrade_lock_name}'; printf '%s\\n' '${patch_id}' >'${upgrade_lock_name}/patch-id'; umask 077; printf '%s\\n' '{\"schema_version\":1,\"patch_id\":\"${patch_id}\",\"mode\":\"rolling_update\"}' >'${maintenance_marker}.tmp'; mv -f '${maintenance_marker}.tmp' '${maintenance_marker}'" >/dev/null 2>&1; then
				relock_failed=true
			fi
		done
		${relock_failed} && log "警告：维护锁释放失败后的补偿回锁不完整；禁止继续变更并立即人工处置"
		update_locks_acquired=true
		return 1
	fi
	locked_nodes=()
	update_locks_acquired=false
}

cleanup_update_artifact_temps() {
  local transaction="$1" host remote_dir="${update_root}/${patch_id}"
  for host in "${controllers[@]}"; do
    remote_run "${host}" "rm -f '${remote_dir}/.package.cgpatch.${transaction}.tmp' '${remote_dir}/.package.json.${transaction}.tmp'" >/dev/null 2>&1 || true
  done
}

publish_update_artifacts() {
  local host remote_dir="${update_root}/${patch_id}"
  local package_source="${PWD}/package.cgpatch" metadata_source="${PWD}/package.json"
  local current_directory managed_directory package_sha metadata_sha transaction package_temporary metadata_temporary

  # Direct CLI upgrades keep using the operator-provided package. Console jobs
  # are identified by their protected update directory and are distributed to
  # every controller before any maintenance gate or RPM mutation is attempted.
  current_directory="$(pwd -P)"
  managed_directory="$(cd "${remote_dir}" 2>/dev/null && pwd -P)" || return 0
  [[ "${current_directory}" == "${managed_directory}" ]] || return 0
  [[ -f "${package_source}" && ! -L "${package_source}" && -f "${metadata_source}" && ! -L "${metadata_source}" ]] ||
    die "控制台升级目录缺少升级包或元数据"
  [[ "${patch_file}" -ef "${package_source}" ]] || die "控制台升级任务引用的升级包与受保护目录不一致"

  package_sha="$(sha256_file "${package_source}")"
  metadata_sha="$(sha256_file "${metadata_source}")"
  jq -e \
    --arg patch_id "${patch_id}" --arg source "${source_version}" --arg target "${target_version}" \
    --arg architecture "${rpm_architecture}" --arg sha256 "${package_sha}" \
    --argjson bootstrap_protocol "${bootstrap_protocol}" '
      .patch_id == $patch_id and .source_version == $source and .target_version == $target and
      .architecture == $architecture and .sha256 == $sha256 and
      .signature_verified == true and .rollback_available == true and .rolling == true and
      .database_mutation == false and
      (if $bootstrap_protocol == 1 then
        .bootstrap_available == true and .bootstrap_protocol == 1
       else true end)
    ' "${metadata_source}" >/dev/null || die "控制台升级包元数据与已验签升级包不一致"

  transaction="${patch_id}.$$"
  package_temporary="${remote_dir}/.package.cgpatch.${transaction}.tmp"
  metadata_temporary="${remote_dir}/.package.json.${transaction}.tmp"
  log "向 ${#controllers[@]} 个控制节点分发已验签升级包并校验 SHA-256"
  for host in "${controllers[@]}"; do
    if ! remote_run "${host}" "set -eu; install -d -o root -g clusterguard -m 0770 '${remote_dir}'; chown root:clusterguard '${remote_dir}'; chmod 0770 '${remote_dir}'" >/dev/null 2>&1 ||
      ! remote_copy "${host}" "${package_source}" "${package_temporary}" >/dev/null 2>&1 ||
      ! remote_run "${host}" "set -eu; printf '%s  %s\\n' '${package_sha}' '${package_temporary}' | sha256sum -c - >/dev/null; chown root:clusterguard '${package_temporary}'; chmod 0640 '${package_temporary}'" >/dev/null 2>&1 ||
      ! remote_copy "${host}" "${metadata_source}" "${metadata_temporary}" >/dev/null 2>&1 ||
      ! remote_run "${host}" "set -eu; printf '%s  %s\\n' '${metadata_sha}' '${metadata_temporary}' | sha256sum -c - >/dev/null; chown root:clusterguard '${metadata_temporary}'; chmod 0640 '${metadata_temporary}'; mv -f '${package_temporary}' '${remote_dir}/package.cgpatch'; mv -f '${metadata_temporary}' '${remote_dir}/package.json'" >/dev/null 2>&1; then
      cleanup_update_artifact_temps "${transaction}"
      die "无法向控制节点 ${host} 分发并验证升级包；尚未建立维护门禁，也未修改任何 RPM"
    fi
  done
  progress_replication_enabled=true
  log "升级包已在全部控制节点完成 SHA-256 校验和原子发布"
}

publish_update_progress() {
  local host remote_dir="${update_root}/${patch_id}" status_source="${PWD}/status.json" temporary
  [[ -f "${status_source}" && -f "${journal_events_file}" ]] || return 0
  for host in "${controllers[@]}"; do
    if ! remote_run "${host}" "set -eu; install -d -o root -g clusterguard -m 0770 '${remote_dir}'; chown root:clusterguard '${remote_dir}'; chmod 0770 '${remote_dir}'" >/dev/null 2>&1; then
      log "警告：控制节点 ${host} 暂时无法接收升级进度"
      continue
    fi
    temporary="${remote_dir}/.status.json.tmp"
    if ! remote_copy "${host}" "${status_source}" "${temporary}" >/dev/null 2>&1 ||
      ! remote_run "${host}" "chown root:clusterguard '${temporary}'; chmod 0640 '${temporary}'; mv -f '${temporary}' '${remote_dir}/status.json'" >/dev/null 2>&1; then
      log "警告：控制节点 ${host} 的升级状态同步失败"
      continue
    fi
    temporary="${remote_dir}/.events.jsonl.tmp"
    if ! remote_copy "${host}" "${journal_events_file}" "${temporary}" >/dev/null 2>&1 ||
      ! remote_run "${host}" "chown root:clusterguard '${temporary}'; chmod 0640 '${temporary}'; mv -f '${temporary}' '${remote_dir}/events.jsonl'" >/dev/null 2>&1; then
      log "警告：控制节点 ${host} 的升级事件同步失败"
    fi
  done
}

write_journal() {
  local status="$1" node="${2:-}" message="${3:-}" phase="${4:-}" current="${5:-0}" total="${6:-0}"
  local event_tmp job_status maintenance finished_at started_at percent=0
  [[ -n "${journal_file}" ]] || return 0
  if ((total > 0)); then
    percent=$((current * 100 / total))
    ((percent <= 100)) || percent=100
  fi
  event_tmp="${journal_file}.event.tmp"
  jq -n --arg patch_id "${patch_id}" --arg mode "${update_mode}" --arg status "${status}" --arg node "${node}" --arg message "${message}" \
    --arg phase "${phase}" --argjson current "${current}" --argjson total "${total}" \
    --arg source "${source_version}" --arg target "${target_version}" --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{patch_id:$patch_id,mode:$mode,status:$status,node:$node,message:$message,phase:$phase,current:$current,total:$total,source:$source,target:$target,updated_at:$at}' >"${event_tmp}"
  chmod 0640 "${event_tmp}"
  cat "${event_tmp}" >>"${journal_events_file}"
  chmod 0640 "${journal_events_file}"
  cp "${event_tmp}" "${journal_file}.tmp"
  chmod 0640 "${journal_file}.tmp"
  mv -f "${journal_file}.tmp" "${journal_file}"
  rm -f "${event_tmp}"

  ${progress_replication_enabled} || return 0

  job_status="running"
  maintenance=true
  finished_at=""
  case "${status}" in
    succeeded) job_status="succeeded"; maintenance=false; finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)" ;;
    rolled_back) job_status="rolled_back"; maintenance=false; finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)" ;;
    failed|rollback_failed|rollback_lock_release_failed) job_status="failed" ;;
  esac
  started_at="$(jq -r '.started_at // empty' "${PWD}/status.json" 2>/dev/null || true)"
  [[ -n "${started_at}" ]] || started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  jq -n --arg patch_id "${patch_id}" --arg mode "${update_mode}" --arg status "${job_status}" \
    --arg node "${node}" --arg message "${message}" --arg phase "${phase}" \
    --arg started_at "${started_at}" --arg updated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg finished_at "${finished_at}" \
    --argjson maintenance_active "${maintenance}" --argjson current "${current}" --argjson total "${total}" --argjson percent "${percent}" \
    '{patch_id:$patch_id,mode:$mode,status:$status,node:$node,message:$message,
      maintenance_active:$maintenance_active,automatic_failover_available:($maintenance_active | not),
      started_at:$started_at,updated_at:$updated_at,finished_at:(if $finished_at == "" then null else $finished_at end),
      progress:{phase:$phase,current:$current,total:$total,percent:$percent}}' >"${PWD}/status.json.tmp"
  chown root:clusterguard "${PWD}/status.json.tmp" 2>/dev/null || true
  chmod 0640 "${PWD}/status.json.tmp"
  mv -f "${PWD}/status.json.tmp" "${PWD}/status.json"
  publish_update_progress
}

wait_node_ready() {
	local host="$1" expected="$2" attempt version status controller_ready data_ready
	for attempt in $(seq 1 60); do
		version="$(remote_package_version "${host}" 2>/dev/null || true)"
		if [[ "${version}" == "${expected}" ]]; then
			controller_ready=true
			data_ready=true
			if is_controller "${host}"; then
				status="$(control_status "${host}" 2>/dev/null || true)"
				[[ -n "${status}" ]] && jq -e '.status == "ok" and .result.ready == true and .result.leader_known == true' <<<"${status}" >/dev/null 2>&1 || controller_ready=false
			fi
			if is_data_node "${host}"; then
				remote_run "${host}" "systemctl is-active --quiet clusterguard-agent.service && systemctl is-active --quiet clusterguard-agent-reconcile.timer" >/dev/null 2>&1 || data_ready=false
			fi
			${controller_ready} && ${data_ready} && return 0
		fi
    sleep 2
  done
  return 1
}

install_node_rpm() {
  local host="$1" rpm_path="$2" expected="$3" remote_dir remote_file services rpm_options
  local expected_product_version expected_release expected_state expected_protocol
  if [[ "${expected}" == "${source_version}" ]]; then
    expected_product_version="${source_product_version}"
    expected_release="${source_release}"
    expected_state="${source_state_format}"
    expected_protocol="${source_update_protocol}"
  else
    expected_product_version="${target_product_version}"
    expected_release="${target_release}"
    expected_state="${target_state_format}"
    expected_protocol="${target_update_protocol}"
  fi
  remote_dir="${remote_stage}/${patch_id}"
  remote_file="${remote_dir}/$(basename "${rpm_path}")"
  remote_run "${host}" "install -d -m 0700 '${remote_dir}'; tar -C /etc -czf '${remote_dir}/etc-clusterguard-before.tgz' clusterguard 2>/dev/null || true" || return 1
  remote_copy "${host}" "${rpm_path}" "${remote_file}.tmp" || return 1
  rpm_options="--replacepkgs"
  if [[ "${expected}" == "${source_version}" ]]; then
    rpm_options="${rpm_options} --oldpackage"
  fi
  remote_run "${host}" "chmod 0600 '${remote_file}.tmp'; mv -f '${remote_file}.tmp' '${remote_file}'; rpm -Uvh ${rpm_options} '${remote_file}'; systemctl daemon-reload" || return 1
  services=""
  if is_controller "${host}"; then services="clusterguard-ha.service"; fi
  if is_data_node "${host}"; then services="${services} clusterguard-agent.service"; fi
  for service in ${services}; do
    remote_run "${host}" "systemctl restart '${service}'" || return 1
  done
  wait_node_ready "${host}" "${expected}" || return 1
  verify_node_contract "${host}" "${expected_product_version}" "${expected_release}" "${expected_state}" "${expected_protocol}" || return 1
}

rollback_updated_nodes() {
  local index host failures=0 current=0 total="${#updated_nodes[@]}"
  ((${#updated_nodes[@]} > 0)) || return 0
  log "开始自动回退已更新节点"
  for ((index=${#updated_nodes[@]}-1; index>=0; index--)); do
    host="${updated_nodes[${index}]}"
    log "回退 ${host} -> ${rollback_version}"
    write_journal rolling_back "${host}" "restoring previous RPM" rollback "${current}" "${total}"
    if ! install_node_rpm "${host}" "${rollback_rpm}" "${rollback_version}"; then
      log "警告：${host} 自动回退失败，需要人工处置"
      failures=$((failures + 1))
      continue
    fi
    current=$((current + 1))
    write_journal rollback_verified "${host}" "rollback version and readiness verified" rollback "${current}" "${total}"
  done
  ((failures == 0))
}

load_nodes
configure_passwords
configure_known_hosts
load_runtime_data_members

if ${rollback_requested}; then
  desired_version="${source_version}"
  desired_rpm="${patch_root}/payload/${source_rpm}"
  desired_product_version="${source_product_version}"
  desired_release="${source_release}"
  desired_state_format="${source_state_format}"
  desired_update_protocol="${source_update_protocol}"
  rollback_version="${target_version}"
  rollback_rpm="${patch_root}/payload/${target_rpm}"
else
  desired_version="${target_version}"
  desired_rpm="${patch_root}/payload/${target_rpm}"
  desired_product_version="${target_product_version}"
  desired_release="${target_release}"
  desired_state_format="${target_state_format}"
  desired_update_protocol="${target_update_protocol}"
  rollback_version="${source_version}"
  rollback_rpm="${patch_root}/payload/${source_rpm}"
fi

for host in "${all_nodes[@]}"; do
  installed="$(remote_package_version "${host}")" || die "无法读取 ${host} 的已安装版本"
  case "${installed}" in
    "${source_version}")
      verify_node_contract "${host}" "${source_product_version}" "${source_release}" "${source_state_format}" "${source_update_protocol}" ||
        die "${host} 的源版本合同校验失败"
      ;;
    "${target_version}")
      verify_node_contract "${host}" "${target_product_version}" "${target_release}" "${target_state_format}" "${target_update_protocol}" ||
        die "${host} 的目标版本合同校验失败"
      ;;
    *)
      die "${host} 当前版本 ${installed} 不在补丁兼容范围 ${source_version}/${target_version}"
      ;;
  esac
done

build_order
printf '\nClusterGuard HA 滚动%s计划\n' "$(${rollback_requested} && printf '回退' || printf '升级')"
printf '  升级包 ID  : %s\n' "${patch_id}"
printf '  当前合同   : %s\n' "${rollback_version}"
printf '  目标合同   : %s\n' "${desired_version}"
printf '  固定顺序   : followers -> data-only -> leader\n'
printf '  Leader     : %s（最后处理）\n' "${leader_host}"
printf '  数据库变更 : false\n'
for host in "${ordered_nodes[@]}"; do printf '  - %s\n' "${host}"; done

if ! ${execute}; then
  log "计划完成，未修改任何节点；确认后追加 --execute"
  exit 0
fi

if ! ${assume_yes}; then
  printf '输入升级包 ID %s 确认：' "${patch_id}"
  read -r confirmation
  [[ "${confirmation}" == "${patch_id}" ]] || die "确认内容不匹配"
fi

journal_file="${PWD}/clusterguard-update-${patch_id}.json"
journal_events_file="${PWD}/clusterguard-update-${patch_id}.events.jsonl"
total_nodes="${#ordered_nodes[@]}"
publish_update_artifacts
write_journal running "" "rolling update started" preparing 0 "${total_nodes}"
write_journal running "" "maintenance gates are being acquired" locking 0 "${total_nodes}"
acquire_update_locks
wait_cluster_idle "${leader_host}" true || die "维护门禁建立后控制面未在时限内恢复一致"
retain_update_locks=true
upgrade_failed=false
failure_node=""
node_index=0
for host in "${ordered_nodes[@]}"; do
	node_index=$((node_index + 1))
  installed="$(remote_package_version "${host}")"
  if [[ "${installed}" == "${desired_version}" ]]; then
    log "跳过已达到目标版本的节点：${host}"
    if ${resume_requested}; then updated_nodes[${#updated_nodes[@]}]="${host}"; fi
    write_journal verified "${host}" "node already matches target contract" updating "${node_index}" "${total_nodes}"
    continue
  fi
  if ! wait_cluster_idle "${leader_host}" true; then
    upgrade_failed=true; failure_node="${host}"; break
  fi
  log "更新节点：${host} (${installed} -> ${desired_version})"
  write_journal updating "${host}" "installing target RPM" updating "$((node_index - 1))" "${total_nodes}"
  updated_nodes[${#updated_nodes[@]}]="${host}"
  if ! install_node_rpm "${host}" "${desired_rpm}" "${desired_version}"; then
    upgrade_failed=true; failure_node="${host}"; break
  fi
  write_journal verified "${host}" "node version and readiness verified" updating "${node_index}" "${total_nodes}"
  if [[ "${host}" != "${leader_host}" ]] && ! wait_cluster_idle "${leader_host}" true; then
    upgrade_failed=true; failure_node="${host}"; break
  fi
done

if ! ${upgrade_failed}; then
  write_journal finalizing "" "verifying all node contracts and maintenance release" finalizing "${total_nodes}" "${total_nodes}"
  if ! wait_cluster_idle "" true; then
    upgrade_failed=true
    failure_node="control-plane"
  fi
fi

if ${upgrade_failed}; then
  write_journal failed "${failure_node}" "node update failed; automatic rollback started" rollback "${#updated_nodes[@]}" "${#updated_nodes[@]}"
  if ! rollback_updated_nodes; then
    write_journal rollback_failed "${failure_node}" "automatic rollback incomplete; maintenance gate retained" rollback 0 "${#updated_nodes[@]}"
    die "节点 ${failure_node} 更新失败且自动回退不完整；维护门禁已保留，请人工处置后使用 --resume"
  fi
  if ! release_update_locks; then
    write_journal rollback_lock_release_failed "${failure_node}" "rollback succeeded but maintenance release failed" rollback "${#updated_nodes[@]}" "${#updated_nodes[@]}"
    die "自动回退完成，但部分维护锁释放失败；变更仍被安全阻断，请修复连通性后使用 --resume"
  fi
  retain_update_locks=false
  write_journal rolled_back "${failure_node}" "automatic rollback completed and maintenance released" rolled_back "${#updated_nodes[@]}" "${#updated_nodes[@]}"
  die "节点 ${failure_node} 更新失败，已完成自动回退"
fi

for host in "${all_nodes[@]}"; do
  installed="$(remote_package_version "${host}")"
  [[ "${installed}" == "${desired_version}" ]] || die "最终版本校验失败：${host}=${installed}"
  verify_node_contract "${host}" "${desired_product_version}" "${desired_release}" "${desired_state_format}" "${desired_update_protocol}" ||
    die "最终版本合同校验失败：${host}"
done
release_update_locks || die "部分控制节点未能释放维护锁；变更操作仍被安全阻断，请修复连通性后使用 --resume"
retain_update_locks=false
wait_cluster_idle "" false || die "维护门禁释放后控制面未在时限内恢复一致"
write_journal succeeded "" "all nodes and maintenance release verified" completed "${total_nodes}" "${total_nodes}"
log "补丁完成：所有节点均为 ${desired_version}，控制面多数派和就绪状态已复核"
