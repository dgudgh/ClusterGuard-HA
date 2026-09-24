#!/usr/bin/env bash
set -euo pipefail
umask 077

declare -a original_arguments=("$@")
patch_file=""
input_patch_file=""
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
rollback_in_progress=false
assume_yes=false
resume_requested=false
private_root="/var/lib/clusterguard-update-private"
remote_stage=""
expected_patch_id=""
managed_job_dir=""
workspace_protocol=2
control_status_path="/api/v1/control-plane/status"
operations_path="/api/v1/operations"
update_gate_path="/api/v1/platform/updates/gate"
maintenance_marker="/etc/clusterguard/update-maintenance.json"
update_locks_acquired=false
retain_update_locks=false
adopted_update_locks=false

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
journal_started=false
upgrade_lock_name=""
configured_data_members=""
configured_data_addresses=""
update_root="/var/lib/clusterguard/updates"
update_pruner="/usr/local/libexec/clusterguard-update-prune.sh"
retained_versions="${CG_UPDATE_RETAINED_VERSIONS:-3}"
update_mode="execute"
progress_replication_enabled=false
recoverable_previous_patch_id=""
current_patch_maintenance_active=false
current_patch_maintenance_inconsistent=false
replicated_update_gate_active=false
all_nodes_at_source=true
bootstrap_available=false
bootstrap_entrypoint=""
bootstrap_sha=""
bootstrap_protocol=0
bootstrap_depth="${CG_UPDATE_BOOTSTRAP_DEPTH:-0}"
cluster_idle_attempts="${CG_UPDATE_CLUSTER_IDLE_ATTEMPTS:-30}"
cluster_idle_delay_seconds="${CG_UPDATE_CLUSTER_IDLE_DELAY_SECONDS:-2}"
cluster_idle_timeout_seconds="${CG_UPDATE_CLUSTER_IDLE_TIMEOUT_SECONDS:-60}"
node_ready_timeout_seconds="${CG_UPDATE_NODE_READY_TIMEOUT_SECONDS:-90}"
node_ready_delay_seconds="${CG_UPDATE_NODE_READY_DELAY_SECONDS:-2}"
stale_operation_threshold_seconds=1800
execution_id=""
previous_execution_id=""
previous_gate_patch_id=""
mutation_guard=""

[[ "${cluster_idle_attempts}" =~ ^[1-9][0-9]*$ ]] || { printf 'CG_UPDATE_CLUSTER_IDLE_ATTEMPTS 必须为正整数\n' >&2; exit 1; }
[[ "${cluster_idle_delay_seconds}" =~ ^[0-9]+$ ]] || { printf 'CG_UPDATE_CLUSTER_IDLE_DELAY_SECONDS 必须为非负整数\n' >&2; exit 1; }
[[ "${cluster_idle_timeout_seconds}" =~ ^[1-9][0-9]*$ ]] || { printf 'CG_UPDATE_CLUSTER_IDLE_TIMEOUT_SECONDS 必须为正整数\n' >&2; exit 1; }
[[ "${node_ready_timeout_seconds}" =~ ^[1-9][0-9]*$ ]] || { printf 'CG_UPDATE_NODE_READY_TIMEOUT_SECONDS 必须为正整数\n' >&2; exit 1; }
# 0 is allowed: scenarios whose peers change state synchronously have nothing to
# wait for, and every retry then costs one probe instead of one sleep. The 60
# attempt ceiling still bounds the loop, so a node that never becomes ready fails
# at exactly the same point, only sooner.
[[ "${node_ready_delay_seconds}" =~ ^[0-9]+$ ]] || { printf 'CG_UPDATE_NODE_READY_DELAY_SECONDS 必须为非负整数\n' >&2; exit 1; }
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
  --private-root DIR            root 私有执行与回退材料目录
  --managed-job-dir DIR         服务账号负责的进度展示目录（仅用于降权发布）
  --expected-patch-id ID        要求签名清单 ID 与任务 ID 完全一致
  --retain-versions COUNT       成功后保留最近升级版本数，默认 3
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
    --retain-versions) need_value "$@"; retained_versions="$2"; shift 2 ;;
    --remote-stage) need_value "$@"; remote_stage="$2"; shift 2 ;;
    --private-root) need_value "$@"; private_root="$2"; shift 2 ;;
    --expected-patch-id) need_value "$@"; expected_patch_id="$2"; shift 2 ;;
    --managed-job-dir) need_value "$@"; managed_job_dir="$2"; shift 2 ;;
    --plan) execute=false; shift ;;
    --execute) execute=true; shift ;;
    --rollback) rollback_requested=true; shift ;;
    --resume) resume_requested=true; shift ;;
    -y|--yes) assume_yes=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数：$1" ;;
  esac
done
[[ -n "${remote_stage}" ]] || remote_stage="${private_root}/history"
[[ "${private_root}" =~ ^/[A-Za-z0-9._/-]+$ && "${private_root}" != *"//"* && "${private_root}" != *"/../"* && "${private_root}" != */.. ]] || die "私有执行目录无效"
[[ -z "${expected_patch_id}" || "${expected_patch_id}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || die "请求升级包 ID 无效"
[[ -z "${managed_job_dir}" || ( "${managed_job_dir}" =~ ^/[A-Za-z0-9._/-]+$ && "${managed_job_dir}" != *"//"* && "${managed_job_dir}" != *"/../"* && "${managed_job_dir}" != */.. ) ]] || die "展示目录无效"

if ${rollback_requested} && ${resume_requested}; then
  die "--rollback 与 --resume 不能同时使用"
fi

command -v jq >/dev/null 2>&1 || die "需要 jq"
command -v openssl >/dev/null 2>&1 || die "需要 openssl"
command -v tar >/dev/null 2>&1 || die "需要 tar"
[[ -f "${patch_file}" && ! -L "${patch_file}" ]] || die "升级包不存在或不是普通文件"
[[ -f "${trust_key}" && ! -L "${trust_key}" ]] || die "可信签名公钥不存在或不是普通文件"
[[ "${ssh_port}" =~ ^[0-9]+$ && "${ssh_port}" -ge 1 && "${ssh_port}" -le 65535 ]] || die "SSH 端口无效"
[[ "${api_port}" =~ ^[0-9]+$ && "${api_port}" -ge 1 && "${api_port}" -le 65535 ]] || die "API 端口无效"
[[ "${retained_versions}" =~ ^[1-9][0-9]*$ ]] || die "保留版本数必须为正整数"
[[ "${ssh_user}" =~ ^[A-Za-z_][A-Za-z0-9_.-]*$ ]] || die "SSH 用户名格式无效"
[[ "${remote_stage}" =~ ^/[A-Za-z0-9._/-]+$ && "${remote_stage}" != *"//"* && "${remote_stage}" != *"/../"* && "${remote_stage}" != */.. ]] ||
  die "远端暂存目录必须是无空格、无相对跳转的绝对路径"
[[ "${update_root}" =~ ^/[A-Za-z0-9._/-]+$ && "${update_root}" != *"//"* && "${update_root}" != *"/../"* && "${update_root}" != */.. ]] ||
  die "升级状态目录必须是无空格、无相对跳转的绝对路径"
input_patch_file="${patch_file}"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

safe_extract_patch() {
  local entry listing
  work_dir="$(mktemp -d /tmp/clusterguard-upgrade.XXXXXX)"
  chmod 0700 "${work_dir}"
  # Copy before listing, verification or extraction. -R -P preserves special
  # files instead of opening devices/FIFOs; these are rejected immediately.
  cp -R -P -- "${patch_file}" "${work_dir}/input.cgpatch"
  [[ -f "${work_dir}/input.cgpatch" && ! -L "${work_dir}/input.cgpatch" ]] || die "升级包快照不是普通文件"
  patch_file="${work_dir}/input.cgpatch"
  listing="$(tar -tzf "${patch_file}")" || die "升级包归档无法读取"
  [[ -n "${listing}" ]] || die "升级包归档为空"
  while IFS= read -r entry; do
    [[ "${entry}" == clusterguard-patch || "${entry}" == clusterguard-patch/* ]] || die "升级包包含范围外路径：${entry}"
    [[ "${entry}" != /* && "${entry}" != *"../"* && "${entry}" != *"/.." && "${entry}" != *"//"* ]] ||
      die "升级包包含不安全路径：${entry}"
  done <<<"${listing}"
  if tar -tvzf "${patch_file}" | awk 'substr($1,1,1) != "-" && substr($1,1,1) != "d" {found=1} END {exit found ? 0 : 1}'; then
    die "升级包禁止包含链接或特殊文件"
  fi
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
  [[ "${patch_id}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || die "patch id 格式无效"
  [[ -z "${expected_patch_id}" || "${expected_patch_id}" == "${patch_id}" ]] || die "签名升级包 ID 与请求不匹配"
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

trusted_directory() {
  local requested="$1" create="${2:-false}" current="" part owner permissions
  local -a parts
  [[ "$requested" == /* && "$requested" != *"//"* && "$requested" != *"/../"* && "$requested" != */.. ]] || return 1
  IFS=/ read -r -a parts <<<"$requested"
  for part in "${parts[@]}"; do
    [[ -n "$part" && "$part" != . ]] || continue
    current="${current}/${part}"
    if [[ ! -e "$current" && ! -L "$current" && "$create" == true ]]; then mkdir -m 0700 -- "$current" || return 1; fi
    [[ -d "$current" && ! -L "$current" ]] || return 1
    read -r owner permissions < <(stat -c '%u %a' -- "$current")
    [[ "$owner" == 0 && "$permissions" =~ ^[0-7]+$ ]] || return 1
    (( (8#$permissions & 8#022) == 0 )) || return 1
  done
}

validate_root_input_patch() {
  ((EUID == 0)) || return 0
  local parent owner permissions links metadata
  [[ "${input_patch_file}" == /* && -f "${input_patch_file}" && ! -L "${input_patch_file}" ]] ||
    die "root 升级输入包必须是绝对路径普通文件"
  metadata="$(stat -c '%u %a %h' -- "${input_patch_file}" 2>/dev/null || stat -f '%u %Lp %l' -- "${input_patch_file}")" ||
    die "无法读取 root 升级输入包属性"
  read -r owner permissions links <<<"${metadata}"
  [[ "${owner}" == 0 && "${permissions}" =~ ^[0-7]+$ && "${links}" == 1 ]] ||
    die "root 升级输入包必须 root 拥有且不可写、不可硬链接"
  (( (8#${permissions} & 8#022) == 0 )) || die "root 升级输入包不可由组或其他用户写入"

  parent="$(dirname -- "${input_patch_file}")"
  # The bootstrap hand-off is a root-created 0700 directory under /tmp. It is
  # accepted only for the exact immutable snapshot name generated above.
  if [[ "${parent}" == /tmp/clusterguard-upgrade.* ]]; then
    [[ "$(basename -- "${input_patch_file}")" == input.cgpatch ]] || die "引导升级快照路径无效"
    metadata="$(stat -c '%u %a' -- "${parent}" 2>/dev/null || stat -f '%u %Lp' -- "${parent}")" ||
      die "无法读取引导升级快照目录属性"
    read -r owner permissions <<<"${metadata}"
    [[ "${owner}" == 0 && "${permissions}" == 700 && -d "${parent}" && ! -L "${parent}" ]] ||
      die "引导升级快照目录不可信"
    return 0
  fi
  trusted_directory "${parent}" false || die "root 升级必须从可信私有目录读取输入包"
}

cleanup() {
  local exit_code=$? last_status
  if ((exit_code != 0)) && ${journal_started}; then
    last_status="$(tail -n 1 "${journal_events_file}" 2>/dev/null | jq -r '.status // empty' 2>/dev/null || true)"
    case "${last_status}" in
      succeeded|rolled_back|failed|rollback_failed|rollback_lock_release_failed) ;;
      *) write_journal failed "${failure_node:-}" "update stopped; inspect node diagnostics before resuming" failed 0 "${total_nodes:-0}" || true ;;
    esac
  fi
  if ${update_locks_acquired} && ! ${retain_update_locks} && ! ${adopted_update_locks} && declare -F release_update_locks >/dev/null 2>&1; then
    release_update_locks || true
  fi
  [[ -z "${work_dir}" || ! -d "${work_dir}" ]] || rm -rf "${work_dir}"
  [[ -z "${known_hosts_file}" || "${known_hosts_file}" == "${known_hosts}" || ! -f "${known_hosts_file}" ]] || rm -f "${known_hosts_file}"
}
trap cleanup EXIT

validate_root_input_patch
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
  jq -e '.bootstrap.workspace_protocol == 2' "${patch_root}/PATCH-MANIFEST.json" >/dev/null || die "旧引导升级器缺少私有执行区安全协议，请重新生成签名升级包"
  log "签名引导升级器校验通过，切换到升级包内执行器"
  set +e
  CG_UPDATE_BOOTSTRAP_DEPTH=1 bash "${patch_root}/${bootstrap_entrypoint}" "${original_arguments[@]}" --patch "${patch_file}"
  bootstrap_exit=$?
  set -e
  exit "${bootstrap_exit}"
fi

if ((EUID == 0)); then
  trusted_directory "$(pwd -P)" || die "root 升级必须在可信私有目录运行，不能使用服务可写目录"
fi

publish_local_update_file() {
  local source="$1" destination="$2"
  [[ -n "${managed_job_dir}" ]] || return 0
  [[ -f "${source}" && ! -L "${source}" ]] || return 0
  [[ "${destination}" == "$(basename -- "${destination}")" ]] || return 1
  command -v runuser >/dev/null 2>&1 || return 1
  # The service account owns the public projection. Root only feeds the
  # already-persisted private bytes over stdin; it never opens a public path.
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
  ' bash "${managed_job_dir}" "${destination}" <"${source}"
}

execution_id="update-$(date -u +%Y%m%dT%H%M%SZ)-$$-$(openssl rand -hex 8)"
mutation_guard="trusted_directory '${remote_stage}' true; exec 8>'${remote_stage}/.node-update.lock'; flock -x -w 30 8;"

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
  command="set -e; $(declare -f trusted_directory); trusted_directory '${private_root}' true; trusted_directory '${remote_stage}' true; trusted_directory '/etc/clusterguard'; ${command}"
  local -a options=(-F /dev/null -o ConnectTimeout=10 -o ServerAliveInterval=5 -o ServerAliveCountMax=3 -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${known_hosts_file}" -o GlobalKnownHostsFile=/dev/null -o IdentitiesOnly=yes)
  if [[ -n "${ssh_key}" ]]; then options+=(-i "${ssh_key}"); else options+=(-o IdentityFile=none); fi
  password_for_host "${host}" || die "找不到 ${host} 的 SSH 凭据"
  if [[ "${ssh_auth_mode}" == "sshpass" ]]; then
    SSHPASS="${current_password}" sshpass -e ssh -p "${ssh_port}" "${options[@]}" -o BatchMode=no "${ssh_user}@${host}" "${command}"
  else
    ssh -p "${ssh_port}" "${options[@]}" -o BatchMode=yes "${ssh_user}@${host}" "${command}"
  fi
}

remote_copy() {
  local host="$1" source="$2" destination="$3"
  local -a options=(-F /dev/null -o ConnectTimeout=10 -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${known_hosts_file}" -o GlobalKnownHostsFile=/dev/null -o IdentitiesOnly=yes)
  if [[ -n "${ssh_key}" ]]; then options+=(-i "${ssh_key}"); else options+=(-o IdentityFile=none); fi
  password_for_host "${host}" || die "找不到 ${host} 的 SSH 凭据"
  if [[ "${ssh_auth_mode}" == "sshpass" ]]; then
    SSHPASS="${current_password}" sshpass -e scp -q -P "${ssh_port}" "${options[@]}" -o BatchMode=no -- "${source}" "${ssh_user}@${host}:${destination}"
  else
    scp -q -P "${ssh_port}" "${options[@]}" -o BatchMode=yes -- "${source}" "${ssh_user}@${host}:${destination}"
  fi
}

prune_update_artifacts() {
  local host failed=false
  for host in "${all_nodes[@]}"; do
    if ! remote_run "${host}" "test -x '${update_pruner}' && '${update_pruner}' --update-root '${update_root}' --history-root '${remote_stage}' --private-root '${private_root}' --retain-versions '${retained_versions}' --protect '${patch_id}'"; then
      failed=true
      log "警告：节点 ${host} 未能完成升级材料保留清理"
    fi
  done
  ${failed} && return 1
  log "升级材料已在全部节点按最近 ${retained_versions} 个版本完成清理"
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

control_operations() {
	local host="$1"
	remote_run "${host}" "set -eu; set -a; . /etc/clusterguard/clusterguard.env; set +a; test -n \"\${CG_CONTROL_TOKEN:-}\"; ca=\"\${CG_TLS_CA_FILE:-/etc/clusterguard/tls/ca.crt}\"; /usr/bin/curl --fail --silent --show-error --max-time 15 --cacert \"\${ca}\" -H \"Authorization: Bearer \${CG_CONTROL_TOKEN}\" -H 'Accept: application/json' 'https://${host}:${api_port}${operations_path}'"
}

control_update_gate() {
	local host="$1" action="$2"
	[[ "${action}" == "acquire" || "${action}" == "release" ]] || return 1
	remote_run "${host}" "set -eu
set -a
. /etc/clusterguard/clusterguard.env
set +a
test -n \"\${CG_CONTROL_TOKEN:-}\"
ca=\"\${CG_TLS_CA_FILE:-/etc/clusterguard/tls/ca.crt}\"
jq_bin=/usr/local/libexec/jq-linux-amd64
test -x \"\${jq_bin}\"
response=\$(/usr/bin/curl --fail --silent --show-error --max-time 15 --cacert \"\${ca}\" \
  -H \"Authorization: Bearer \${CG_CONTROL_TOKEN}\" -H 'Content-Type: application/json' \
  --data-binary '{\"patch_id\":\"${patch_id}\",\"execution_id\":\"${execution_id}\",\"previous_patch_id\":\"${previous_gate_patch_id}\",\"previous_execution_id\":\"${previous_execution_id}\"}' \
  'https://${host}:${api_port}${update_gate_path}/${action}')
printf '%s' \"\${response}\" | \"\${jq_bin}\" -e --arg patch '${patch_id}' --arg execution '${execution_id}' \
  '.status == \"ok\" and .result.patch_id == \$patch and .result.execution_id == \$execution' >/dev/null"
}

acquire_replicated_update_gate() {
	${replicated_update_gate_active} && return 0
	control_update_gate "${leader_host}" acquire || return 1
	replicated_update_gate_active=true
	log "Raft 升级维护门禁已建立 patch_id=${patch_id} execution_id=${execution_id}"
}

release_replicated_update_gate() {
	${replicated_update_gate_active} || return 0
	control_update_gate "${leader_host}" release || return 1
	replicated_update_gate_active=false
	log "Raft 升级维护门禁已释放 patch_id=${patch_id} execution_id=${execution_id}"
}

all_controllers_support_replicated_gate() {
  local host info
  for host in "${controllers[@]}"; do
    info="$(remote_run "${host}" "/usr/local/bin/clusterguard --version-json")" || return 1
    jq -e '.update_gate_protocol == 1' <<<"${info}" >/dev/null || return 1
  done
}

finish_update_maintenance() {
  local activity="$1"
  # Old controllers do not read the replicated gate. Keep their local markers
  # after a downgrade; a signed recovery upgrade can safely take them over.
  all_controllers_support_replicated_gate || return 1
  verify_cluster_idle "" true "${activity}"
  acquire_replicated_update_gate || return 1
  assert_update_lock_ownership || return 1
  release_update_locks || return 1
  wait_cluster_idle "" true "${activity}" || return 1
  verify_cluster_idle "" true "${activity}"
  release_replicated_update_gate || return 1
  wait_cluster_idle "" false "${activity}" || return 1
  verify_cluster_idle "" false "${activity}"
  retain_update_locks=false
}

controller_status_violations() {
	local status="$1" expected_maintenance="$2" expected_activity="$3"
	jq -r --arg maintenance "${expected_maintenance}" --arg activity "${expected_activity}" '
		def add_if($condition; $label): if $condition then $label else empty end;
		[
			add_if(.status != "ok"; "api_status_not_ok"),
			add_if(.result.ready != true; "ready_not_true"),
			add_if(.result.leader_known != true; "leader_unknown"),
			add_if((.result.role != "leader" and .result.role != "follower"); "role_invalid"),
			add_if((.result.role == "leader" and .result.quorum_confirmed != true); "leader_quorum_not_confirmed"),
			add_if((.result.voter_count | type) != "number" or (.result.voter_count < 3) or ((.result.voter_count % 2) != 1); "voter_count_invalid"),
			add_if(($activity != "any") and ((.result.active_operations | type) != "number" or .result.active_operations < 0 or (.result.active_operations | floor) != .result.active_operations); "active_operations_invalid"),
			add_if(($activity == "idle") and .result.active_operations != 0; "active_operations_not_zero"),
			add_if(($activity != "any") and .result.indeterminate_operations != 0; "indeterminate_operations_not_zero"),
			add_if(($activity != "any") and .result.active_lifecycle_tasks != 0; "active_lifecycle_tasks_not_zero"),
			add_if((.result.controller_members | type) != "array"; "controller_members_invalid"),
			add_if((.result.controller_members | type) == "array" and (.result.controller_members | length) != .result.voter_count; "controller_member_count_mismatch"),
			add_if((.result.data_node_members | type) != "array"; "data_node_members_invalid"),
			add_if((.result.data_node_members | type) == "array" and ((.result.data_node_members | map(.resource_id) | unique | length) != (.result.data_node_members | length)); "duplicate_data_node_resource_id"),
			add_if((.result.data_node_members | type) == "array" and ((.result.data_node_members | map(.ip_address) | all(type == "string" and length > 0)) | not); "data_node_address_invalid"),
			add_if(($maintenance != "any") and ((.result.update_maintenance_active // false) != ($maintenance == "true")); "maintenance_state_mismatch")
		] | join(",")
	' <<<"${status}"
}

log_controller_diagnostics() {
	local host="$1" status="$2" violations="$3" summary
	if summary="$(jq -c '.result | {
		role:(.role // null),ready:(.ready // null),leader_known:(.leader_known // null),
		quorum_confirmed:(.quorum_confirmed // null),voter_count:(.voter_count // null),
		active_operations:(.active_operations // null),indeterminate_operations:(.indeterminate_operations // null),
		active_lifecycle_tasks:(.active_lifecycle_tasks // null),update_maintenance_active:(.update_maintenance_active // false)
	}' <<<"${status}" 2>/dev/null)"; then
		log "控制面节点诊断 host=${host} facts=${summary} violations=${violations}"
	else
		log "控制面节点诊断 host=${host} violations=status_response_unreadable"
	fi
}

log_safe_operation_diagnostics() {
	local host="$1" operations="$2" now_epoch="$3"
	jq -c --argjson now "${now_epoch}" --argjson threshold "${stale_operation_threshold_seconds}" '
		def text($value): if ($value | type) == "string" and ($value | length) > 0 then $value else null end;
		def updated_epoch: try (.updated_at | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601) catch null;
		.result[]? |
		. as $record |
		{
			operation_id:text($record.resource_id),
			cluster_id:text($record.operation.cluster_id),
			kind:text($record.operation.kind),
			stage:text($record.stage),
			status:text($record.status),
			updated_at:text($record.updated_at),
			violations:(
				[if ($record | type) != "object" then "malformed_record" else empty end,
				 if text($record.resource_id) == null then "operation_id_missing" else empty end,
				 if text($record.operation.cluster_id) == null then "cluster_id_missing" else empty end,
				 if text($record.operation.kind) == null then "kind_missing" else empty end,
				 if text($record.stage) == null then "stage_missing" else empty end,
				 if text($record.status) == null then "status_missing" else empty end,
				 if text($record.updated_at) == null or ($record | updated_epoch) == null then "updated_at_invalid" else empty end,
				 if $record.status == "indeterminate" then "indeterminate_operation" else empty end,
				 if $record.status == "running" and $record.operation.requested_by != "clusterguard-automatic-recovery" then "requested_by_not_automatic_recovery" else empty end,
				 if $record.status == "running" and (["discover","precheck","plan","safety_guard","lock","approve"] | index($record.stage) | not) then "stage_not_pre_mutation" else empty end,
				 if $record.status == "running" and ($record | updated_epoch) != null and (($now - ($record | updated_epoch)) < $threshold) then "operation_not_stale" else empty end]
			)
		} |
		select(.status == "running" or .status == "indeterminate" or (.violations | length) > 0)
	' <<<"${operations}" 2>/dev/null | while IFS= read -r diagnostic; do
		[[ -z "${diagnostic}" ]] || log "控制面操作诊断 host=${host} safe=${diagnostic}"
	done
}

stale_automatic_operations_allowed() {
	local host="$1" expected_count="$2" scope="${3:-maintenance_gate}" operations now_epoch exemption_label
	[[ "${scope}" == "maintenance_gate" || "${scope}" == "read_only_plan" || "${scope}" == "rollback_release" ]] || return 1
	if ! operations="$(control_operations "${host}" 2>/dev/null)"; then
		log "控制面操作诊断 host=${host} violations=authenticated_operation_inventory_unreadable"
		return 1
	fi
	now_epoch="$(date -u +%s)"
	if ! jq -e '.status == "ok" and (.result | type) == "array"' <<<"${operations}" >/dev/null 2>&1; then
		log "控制面操作诊断 host=${host} violations=authenticated_operation_inventory_malformed"
		return 1
	fi
	if ! jq -e --argjson expected "${expected_count}" --argjson now "${now_epoch}" --argjson threshold "${stale_operation_threshold_seconds}" '
		def valid_text($value): ($value | type) == "string" and ($value | length) > 0;
		def valid_timestamp: (try (.updated_at | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601) catch null) as $updated | $updated != null and ($now - $updated) >= $threshold;
		.status == "ok" and (.result | type) == "array" and
		([.result[] | select(.status == "running")] | length) == $expected and
		([.result[] | select(.status == "indeterminate")] | length) == 0 and
		all(.result[]; . as $record |
			($record | type == "object") and valid_text($record.resource_id) and valid_text($record.operation.cluster_id) and
			valid_text($record.operation.kind) and valid_text($record.operation.requested_by) and valid_text($record.stage) and
			valid_text($record.status) and valid_text($record.updated_at) and
			(["planned","blocked","running","succeeded","failed","indeterminate","unsupported"] | index($record.status)) != null and
			(if $record.status == "running" then
				$record.operation.requested_by == "clusterguard-automatic-recovery" and
				(["discover","precheck","plan","safety_guard","lock","approve"] | index($record.stage)) != null and
				($record | valid_timestamp)
			 else true end))
	' <<<"${operations}" >/dev/null 2>&1; then
		log_safe_operation_diagnostics "${host}" "${operations}" "${now_epoch}"
		return 1
	fi
	exemption_label="维护期陈旧自动恢复豁免"
	[[ "${scope}" != "read_only_plan" ]] || exemption_label="只读计划陈旧自动恢复豁免"
	[[ "${scope}" != "rollback_release" ]] || exemption_label="受控回退释放后陈旧自动恢复豁免"
	log "${exemption_label} host=${host} active_operations=${expected_count} stale_threshold_seconds=${stale_operation_threshold_seconds}；不修改或删除操作，等待新 Leader 运行时协调器收敛"
	log_safe_operation_diagnostics "${host}" "${operations}" "${now_epoch}"
	return 0
}

verify_cluster_idle() {
	local expected_leader="${1:-}" expected_maintenance="${2:-false}" expected_activity="${3:-idle}"
	local host status leaders=0 role observed_leader="" leader_status="" violations active_count=0 operations index
	local stale_operation_scope="maintenance_gate"
	local failed=false activity_detected=false activity_counts_consistent=true
	local local_controller_id live_members current_live_members configured_members live_data_members current_live_data_members
	local live_data_addresses current_live_data_addresses
	local -a controller_statuses=()
  [[ "${expected_maintenance}" == "true" || "${expected_maintenance}" == "false" || "${expected_maintenance}" == "any" ]] ||
    die "内部维护状态参数无效"
	[[ "${expected_activity}" == "idle" || "${expected_activity}" == "any" || "${expected_activity}" == "stale_automatic" ]] ||
		die "内部活动任务参数无效"
	if [[ "${expected_activity}" == "stale_automatic" ]]; then
		if [[ "${expected_maintenance}" == "true" ]]; then
			stale_operation_scope="maintenance_gate"
		elif [[ "${expected_maintenance}" == "false" ]] && ! ${execute}; then
			stale_operation_scope="read_only_plan"
		elif [[ "${expected_maintenance}" == "false" ]] && { ${rollback_requested} || ${rollback_in_progress}; }; then
			stale_operation_scope="rollback_release"
		else
			die "陈旧自动恢复豁免只能用于只读计划、全部控制节点确认维护状态之后或受控回退释放核验"
		fi
	fi
	live_members=""
	live_data_members=""
	live_data_addresses=""
	configured_members=""
	for host in "${controllers[@]}"; do
		if ! status="$(control_status "${host}" 2>/dev/null)" || ! jq -e 'type == "object" and (.result | type) == "object"' <<<"${status}" >/dev/null 2>&1; then
			log "控制面节点诊断 host=${host} violations=status_response_unreadable"
			failed=true
			controller_statuses+=("")
			continue
		fi
		controller_statuses+=("${status}")
		violations="$(controller_status_violations "${status}" "${expected_maintenance}" "${expected_activity}" 2>/dev/null || printf 'status_response_unreadable')"
		if [[ -n "${violations}" ]]; then
			log_controller_diagnostics "${host}" "${status}" "${violations}"
			failed=true
		fi
		if jq -e '.result.active_operations > 0' <<<"${status}" >/dev/null 2>&1; then activity_detected=true; fi
		local_controller_id="$(jq -r '.result.local_controller_id // empty' <<<"${status}")"
		if [[ -z "${local_controller_id}" ]]; then
			log_controller_diagnostics "${host}" "${status}" "local_controller_id_missing"
			failed=true
			continue
		fi
		configured_members="${configured_members}${local_controller_id}"$'\n'
		current_live_members="$(jq -r '.result.controller_members[].resource_id' <<<"${status}" | LC_ALL=C sort -u)"
		if [[ -z "${current_live_members}" ]]; then
			log_controller_diagnostics "${host}" "${status}" "controller_members_empty"
			failed=true
			continue
		fi
		if [[ -z "${live_members}" ]]; then
			live_members="${current_live_members}"
		elif [[ "${live_members}" != "${current_live_members}" ]]; then
				log_controller_diagnostics "${host}" "${status}" "controller_member_observation_mismatch"
				failed=true
		fi
		current_live_data_members="$(jq -r '.result.data_node_members[].resource_id' <<<"${status}" | LC_ALL=C sort -u)"
		if [[ -z "${live_data_members}" ]]; then
			live_data_members="${current_live_data_members}"
		elif [[ "${live_data_members}" != "${current_live_data_members}" ]]; then
				log_controller_diagnostics "${host}" "${status}" "data_node_member_observation_mismatch"
				failed=true
		fi
		current_live_data_addresses="$(jq -r '.result.data_node_members[].ip_address' <<<"${status}" | LC_ALL=C sort -u)"
		if [[ -z "${live_data_addresses}" ]]; then
			live_data_addresses="${current_live_data_addresses}"
		elif [[ "${live_data_addresses}" != "${current_live_data_addresses}" ]]; then
				log_controller_diagnostics "${host}" "${status}" "data_node_address_observation_mismatch"
				failed=true
		fi
		role="$(jq -r '.result.role' <<<"${status}")"
	    if [[ "${role}" == "leader" ]]; then leaders=$((leaders + 1)); observed_leader="${host}"; leader_status="${status}"; fi
	done
	if ((leaders == 1)); then
		active_count="$(jq -r '.result.active_operations // 0' <<<"${leader_status}")"
		for ((index=0; index<${#controller_statuses[@]}; index++)); do
			status="${controller_statuses[${index}]}"
			[[ -n "${status}" ]] || continue
			if [[ "$(jq -r '.result.active_operations' <<<"${status}" 2>/dev/null)" != "${active_count}" ]]; then
				log_controller_diagnostics "${controllers[${index}]}" "${status}" "active_operation_count_observation_mismatch"
				activity_counts_consistent=false
				failed=true
			fi
		done
		if ${activity_detected} && [[ "${expected_activity}" != "any" ]]; then
			if [[ "${expected_activity}" == "stale_automatic" ]] && ${activity_counts_consistent} && [[ "${active_count}" =~ ^[1-9][0-9]*$ ]] && stale_automatic_operations_allowed "${observed_leader}" "${active_count}" "${stale_operation_scope}"; then
				:
			else
				if [[ "${expected_activity}" == "stale_automatic" ]]; then
					for ((index=0; index<${#controllers[@]}; index++)); do
						status="${controller_statuses[${index}]}"
						[[ -n "${status}" ]] || continue
						if jq -e '.result.active_operations > 0' <<<"${status}" >/dev/null 2>&1; then
							log_controller_diagnostics "${controllers[${index}]}" "${status}" "active_operations_not_exempt"
						fi
					done
				else
					operations="$(control_operations "${observed_leader}" 2>/dev/null || true)"
					if jq -e '.status == "ok" and (.result | type) == "array"' <<<"${operations}" >/dev/null 2>&1; then
						log_safe_operation_diagnostics "${observed_leader}" "${operations}" "$(date -u +%s)"
					else
						log "控制面操作诊断 host=${observed_leader} violations=authenticated_operation_inventory_unreadable"
					fi
				fi
				failed=true
			fi
		fi
	fi
	configured_members="$(printf '%s' "${configured_members}" | sed '/^$/d' | LC_ALL=C sort -u)"
	if [[ "${configured_members}" != "${live_members}" ]]; then
		log "静态控制节点清单与实时 Raft 成员不一致；请使用当前部署状态或 --controllers 提供全部控制节点后重试"
		log "控制面集群诊断 violations=configured_controller_members_mismatch"
		failed=true
	fi
	if [[ "${configured_data_addresses}" != "${live_data_addresses}" ]]; then
		log "静态数据节点地址与实时活动节点宿主机映射不一致；请使用 --data-nodes 提供全部活动宿主机后重试"
		log "控制面集群诊断 violations=configured_data_node_addresses_mismatch"
		failed=true
	fi
	if [[ "${configured_data_addresses}" == "${live_data_addresses}" && "${configured_data_members}" != "${live_data_members}" ]]; then
		log "检测到容器数据节点独立逻辑身份；控制节点观测一致，且宿主机映射已严格核对"
	fi
	if ((leaders != 1)); then
		log "控制面集群诊断 leaders=${leaders} violations=leader_count_not_one"
		failed=true
	fi
	if [[ -n "${expected_leader}" && "${observed_leader}" != "${expected_leader}" ]]; then
		log "控制面集群诊断 expected_leader=${expected_leader} observed_leader=${observed_leader:-none} violations=leader_changed"
		failed=true
	fi
	${failed} && die "控制面升级门禁未通过；以上诊断逐项列出实际违反条件"
	leader_host="${observed_leader}"
}

wait_cluster_idle() {
	local expected_leader="${1:-}" expected_maintenance="${2:-false}" expected_activity="${3:-idle}" attempt output=""
	local started_epoch deadline_epoch now_epoch sleep_seconds elapsed_seconds
	started_epoch="$(date +%s)"
	deadline_epoch=$((started_epoch + cluster_idle_timeout_seconds))
	for attempt in $(seq 1 "${cluster_idle_attempts}"); do
		if output="$(trap - EXIT; verify_cluster_idle "${expected_leader}" "${expected_maintenance}" "${expected_activity}" 2>&1)"; then
			[[ -z "${output}" ]] || printf '%s\n' "${output}"
	      return 0
	    fi
    if ((attempt == 1 || attempt % 5 == 0)); then
      log "等待控制面收敛（${attempt}/${cluster_idle_attempts}）：${output##*$'\n'}"
    fi
		now_epoch="$(date +%s)"
		((attempt < cluster_idle_attempts && now_epoch < deadline_epoch)) || break
		sleep_seconds="${cluster_idle_delay_seconds}"
		if ((sleep_seconds > deadline_epoch - now_epoch)); then sleep_seconds=$((deadline_epoch - now_epoch)); fi
		((sleep_seconds > 0)) && sleep "${sleep_seconds}"
  done
	elapsed_seconds=$(($(date +%s) - started_epoch))
	  log "错误：控制面在 ${elapsed_seconds} 秒内未恢复一致（上限 ${cluster_idle_timeout_seconds} 秒）" >&2
	  printf '%s\n' "${output}" >&2
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

node_service_facts() {
  local host="$1"
  remote_run "${host}" "set +e
unit_fact() {
  unit=\"\$1\"
  active=\$(systemctl is-active \"\${unit}\" 2>/dev/null || true)
  enabled=\$(systemctl is-enabled \"\${unit}\" 2>/dev/null || true)
  printf '%s=%s/%s ' \"\${unit}\" \"\${active:-unknown}\" \"\${enabled:-unknown}\"
}
printf 'rpm='
rpm -q --qf '%{VERSION}-%{RELEASE} ' clusterguard-ha 2>/dev/null || printf 'unknown '
unit_fact clusterguard-ha.service
unit_fact clusterguard-update-helper.service
unit_fact clusterguard-agent.service
unit_fact clusterguard-agent-reconcile.timer
printf '\\n'"
}

log_node_service_facts() {
  local host="$1" facts
  facts="$(node_service_facts "${host}" 2>/dev/null || true)"
  [[ -n "${facts}" ]] || facts="service_state_unavailable"
  log "节点服务诊断 host=${host} ${facts}"
}

log_all_node_service_facts() {
  local host
  for host in "${all_nodes[@]}"; do log_node_service_facts "${host}"; done
}

failed_update_lock_on_host() {
  local host="$1"
  remote_run "${host}" "set -eu
lock='${remote_stage}/.cluster-update.lock'
marker='${maintenance_marker}'
jq_bin=/usr/local/libexec/jq-linux-amd64
test -d \"\${lock}\" && test -f \"\${lock}/patch-id\" && test -f \"\${marker}\" && test -x \"\${jq_bin}\"
old=\$(cat \"\${lock}/patch-id\")
case \"\${old}\" in [A-Za-z0-9]*) ;; *) exit 2 ;; esac
case \"\${old}\" in *[!A-Za-z0-9._-]*) exit 2 ;; esac
test \"\${old}\" != '${patch_id}'
\"\${jq_bin}\" -e --arg patch \"\${old}\" '.patch_id == \$patch and .mode == \"rolling_update\"' \"\${marker}\" >/dev/null
status='${private_root}/history/'\"\${old}\"'/status.json'
test -f \"\${status}\"
\"\${jq_bin}\" -e --arg patch \"\${old}\" '.patch_id == \$patch and .status == \"failed\" and .maintenance_active == true' \"\${status}\" >/dev/null
printf '%s\\n' \"\${old}\""
}

current_update_lock_on_host() {
  local host="$1"
  remote_run "${host}" "set -eu
jq_bin=/usr/local/libexec/jq-linux-amd64
test -d '${remote_stage}/.cluster-update.lock' && test -f '${remote_stage}/.cluster-update.lock/patch-id' && test -f '${remote_stage}/.cluster-update.lock/execution-id' && test -f '${maintenance_marker}' && test -x \"\${jq_bin}\"
grep -Fqx '${patch_id}' '${remote_stage}/.cluster-update.lock/patch-id'
owner=\$(cat '${remote_stage}/.cluster-update.lock/execution-id')
case \"\${owner}\" in [A-Za-z0-9]*) ;; *) exit 2 ;; esac
case \"\${owner}\" in *[!A-Za-z0-9._-]*) exit 2 ;; esac
\"\${jq_bin}\" -e --arg patch '${patch_id}' --arg owner \"\${owner}\" '.schema_version == 2 and .patch_id == \$patch and .execution_id == \$owner and .mode == \"rolling_update\"' '${maintenance_marker}' >/dev/null
printf '%s\\n' \"\${owner}\""
}

detect_current_update_lock() {
  local host found=0
  for host in "${controllers[@]}"; do
    if current_update_lock_on_host "${host}" >/dev/null 2>&1; then
      found=$((found + 1))
    fi
  done
  if ((found == ${#controllers[@]})); then
    current_patch_maintenance_active=true
    log "检测到当前升级包的完整维护锁 patch_id=${patch_id} controllers=${found}/${#controllers[@]}"
  elif ((found > 0)); then
    current_patch_maintenance_inconsistent=true
    log "当前升级包维护锁仅在 ${found}/${#controllers[@]} 个控制节点匹配；不会自动修复或复用"
  fi
}

detect_recoverable_failed_update() {
  local host candidate previous="" found=0 owner previous_owner=""
  for host in "${controllers[@]}"; do
    candidate="$(failed_update_lock_on_host "${host}" 2>/dev/null || true)"
    if [[ -z "${candidate}" ]]; then
      continue
    fi
    [[ "${candidate}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || return 0
    if [[ -n "${previous}" && "${candidate}" != "${previous}" ]]; then
      log "检测到不一致的历史升级维护锁；不会自动接管"
      return 0
    fi
    previous="${candidate}"
    found=$((found + 1))
  done
  if ((found == ${#controllers[@]})) && [[ -n "${previous}" ]]; then
    for host in "${controllers[@]}"; do
      owner="$(remote_run "${host}" "if test -f '${remote_stage}/.cluster-update.lock/execution-id'; then cat '${remote_stage}/.cluster-update.lock/execution-id'; else printf 'legacy\\n'; fi")" || return 0
      [[ "${owner}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || return 0
      [[ -z "${previous_owner}" || "${previous_owner}" == "${owner}" ]] || return 0
      previous_owner="${owner}"
    done
    recoverable_previous_patch_id="${previous}"
    if [[ "${previous_owner}" != legacy ]]; then
      previous_gate_patch_id="${previous}"
      previous_execution_id="${previous_owner}"
    fi
    log "检测到可安全接管的失败升级维护锁 previous_patch_id=${recoverable_previous_patch_id}"
  elif ((found > 0)); then
    log "历史升级维护锁仅在 ${found}/${#controllers[@]} 个控制节点满足接管条件；不会自动接管"
  fi
}

build_order() {
  local host
  if ${resume_requested}; then
		verify_cluster_idle "" true any
	elif ${execute}; then
		# Topology and quorum must be stable, but an operation that entered before
		# maintenance is allowed to drain after every controller is gated.
		if ${rollback_requested} && ${current_patch_maintenance_active}; then
			verify_cluster_idle "" true any
		elif [[ -n "${recoverable_previous_patch_id}" ]]; then
			verify_cluster_idle "" true any
		else
			verify_cluster_idle "" false any
		fi
  else
    # Planning does not acquire maintenance or mutate a node. Accept only the
    # same strictly classified stale pre-mutation automatic record that the
    # signed bootstrap can carry through maintenance; otherwise the old record
    # creates a plan-before-upgrade circular dependency.
    if [[ -n "${recoverable_previous_patch_id}" ]]; then
      wait_cluster_idle "" true stale_automatic || die "生成恢复升级计划前控制面未在时限内恢复一致"
    else
      wait_cluster_idle "" false stale_automatic || die "生成升级计划前控制面未在时限内恢复空闲"
    fi
    # wait_cluster_idle isolates a failed probe so `die` cannot terminate the
    # caller. Refresh the stable topology in this shell to retain leader_host;
    # a newly admitted operation does not invalidate the read-only node order.
    if [[ -n "${recoverable_previous_patch_id}" ]]; then
      verify_cluster_idle "" true any
    else
      verify_cluster_idle "" false any
    fi
  fi
  for host in "${controllers[@]}"; do [[ "${host}" == "${leader_host}" ]] || ordered_nodes[${#ordered_nodes[@]}]="${host}"; done
  for host in "${data_nodes[@]}"; do is_controller "${host}" || ordered_nodes[${#ordered_nodes[@]}]="${host}"; done
  ordered_nodes[${#ordered_nodes[@]}]="${leader_host}"
}

transfer_failed_update_locks() {
  local host restore_host command restore_command previous_marker previous_owner_command transfer_failed=false compensation_failed=false
  local -a transfer_order=("${leader_host}") transferred=()
  for host in "${controllers[@]}"; do
    [[ "${host}" == "${leader_host}" ]] || transfer_order[${#transfer_order[@]}]="${host}"
  done
  previous_marker="{\"schema_version\":1,\"patch_id\":\"${recoverable_previous_patch_id}\",\"mode\":\"rolling_update\"}"
  previous_owner_command="rm -f \"\${lock}/execution-id\""
  if [[ -n "${previous_execution_id}" ]]; then
    previous_marker="{\"schema_version\":2,\"patch_id\":\"${recoverable_previous_patch_id}\",\"execution_id\":\"${previous_execution_id}\",\"mode\":\"rolling_update\"}"
    previous_owner_command="printf '%s\\n' '${previous_execution_id}' >\"\${lock}/execution-id\""
  fi
  command="set -eu
${mutation_guard}
lock='${remote_stage}/.cluster-update.lock'
marker='${maintenance_marker}'
jq_bin=/usr/local/libexec/jq-linux-amd64
test \"\$(cat \"\${lock}/patch-id\")\" = '${recoverable_previous_patch_id}'
\"\${jq_bin}\" -e --arg patch '${recoverable_previous_patch_id}' '.patch_id == \$patch and .mode == \"rolling_update\"' \"\${marker}\" >/dev/null
\"\${jq_bin}\" -e --arg patch '${recoverable_previous_patch_id}' '.patch_id == \$patch and .status == \"failed\" and .maintenance_active == true' '${private_root}/history/${recoverable_previous_patch_id}/status.json' >/dev/null
printf '%s\\n' '${patch_id}' >\"\${lock}/patch-id.tmp\"
mv -f \"\${lock}/patch-id.tmp\" \"\${lock}/patch-id\"
printf '%s\\n' '${execution_id}' >\"\${lock}/execution-id.tmp\"
mv -f \"\${lock}/execution-id.tmp\" \"\${lock}/execution-id\"
umask 077
printf '%s\\n' '{\"schema_version\":2,\"patch_id\":\"${patch_id}\",\"execution_id\":\"${execution_id}\",\"mode\":\"rolling_update\",\"supersedes\":\"${recoverable_previous_patch_id}\"}' >\"\${marker}.tmp\"
mv -f \"\${marker}.tmp\" \"\${marker}\""
  restore_command="set -eu
${mutation_guard}
lock='${remote_stage}/.cluster-update.lock'
marker='${maintenance_marker}'
test \"\$(cat \"\${lock}/patch-id\")\" = '${patch_id}'
test \"\$(cat \"\${lock}/execution-id\")\" = '${execution_id}'
printf '%s\\n' '${recoverable_previous_patch_id}' >\"\${lock}/patch-id.tmp\"
mv -f \"\${lock}/patch-id.tmp\" \"\${lock}/patch-id\"
${previous_owner_command}
umask 077
printf '%s\\n' '${previous_marker}' >\"\${marker}.tmp\"
mv -f \"\${marker}.tmp\" \"\${marker}\""
  for host in "${transfer_order[@]}"; do
    if ! remote_run "${host}" "${command}"; then
      transfer_failed=true
      break
    fi
    transferred[${#transferred[@]}]="${host}"
  done
  if ${transfer_failed}; then
    for restore_host in "${transferred[@]}"; do
      remote_run "${restore_host}" "${restore_command}" >/dev/null 2>&1 || compensation_failed=true
    done
    ${compensation_failed} && log "警告：失败升级维护锁接管补偿不完整；所有维护门禁保持关闭"
    die "无法原子接管失败升级 ${recoverable_previous_patch_id} 的维护锁；未修改任何 RPM"
  fi
  locked_nodes=("${transfer_order[@]}")
  adopted_update_locks=true
  retain_update_locks=true
  update_locks_acquired=true
  log "已接管失败升级维护锁 previous_patch_id=${recoverable_previous_patch_id} current_patch_id=${patch_id}"
}

adopt_current_update_locks() {
  local host candidate previous_owner="" adoption_failed=false compensation_failed=false
  local -a adopted=()
  for host in "${controllers[@]}"; do
    candidate="$(current_update_lock_on_host "${host}" 2>/dev/null || true)"
    [[ "${candidate}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || die "控制节点 ${host} 缺少有效的升级执行所有者；不会并发续跑"
    if [[ -n "${previous_owner}" && "${candidate}" != "${previous_owner}" ]]; then
      die "当前升级锁的 execution_id 不一致；不会并发续跑"
    fi
    previous_owner="${candidate}"
    remote_run "${host}" "set -eu
jq_bin=/usr/local/libexec/jq-linux-amd64
test -x \"\${jq_bin}\"
tail -n 1 '${private_root}/history/${patch_id}/events.jsonl' | \"\${jq_bin}\" -e --arg patch '${patch_id}' --arg owner '${previous_owner}' '.patch_id == \$patch and .execution_id == \$owner and (.status == \"failed\" or .status == \"rollback_failed\" or .status == \"rollback_lock_release_failed\")' >/dev/null" >/dev/null 2>&1 ||
      die "升级 ${patch_id} 尚未进入可接管的失败终态；不会并发续跑或回退"
  done

  for host in "${controllers[@]}"; do
    if ! remote_run "${host}" "set -eu
${mutation_guard}
lock='${remote_stage}/.cluster-update.lock'
marker='${maintenance_marker}'
jq_bin=/usr/local/libexec/jq-linux-amd64
test \"\$(cat \"\${lock}/patch-id\")\" = '${patch_id}'
test \"\$(cat \"\${lock}/execution-id\")\" = '${previous_owner}'
\"\${jq_bin}\" -e --arg patch '${patch_id}' --arg owner '${previous_owner}' '.schema_version == 2 and .patch_id == \$patch and .execution_id == \$owner and .mode == \"rolling_update\"' \"\${marker}\" >/dev/null
printf '%s\\n' '${execution_id}' >\"\${lock}/execution-id.tmp\"
mv -f \"\${lock}/execution-id.tmp\" \"\${lock}/execution-id\"
umask 077
printf '%s\\n' '{\"schema_version\":2,\"patch_id\":\"${patch_id}\",\"execution_id\":\"${execution_id}\",\"mode\":\"rolling_update\",\"supersedes_execution_id\":\"${previous_owner}\"}' >\"\${marker}.tmp\"
mv -f \"\${marker}.tmp\" \"\${marker}\""; then
      adoption_failed=true
      break
    fi
    adopted[${#adopted[@]}]="${host}"
  done
  if ${adoption_failed}; then
    for host in "${adopted[@]}"; do
      remote_run "${host}" "set -eu
${mutation_guard}
lock='${remote_stage}/.cluster-update.lock'
marker='${maintenance_marker}'
test \"\$(cat \"\${lock}/patch-id\")\" = '${patch_id}'
test \"\$(cat \"\${lock}/execution-id\")\" = '${execution_id}'
printf '%s\\n' '${previous_owner}' >\"\${lock}/execution-id.tmp\"
mv -f \"\${lock}/execution-id.tmp\" \"\${lock}/execution-id\"
umask 077
printf '%s\\n' '{\"schema_version\":2,\"patch_id\":\"${patch_id}\",\"execution_id\":\"${previous_owner}\",\"mode\":\"rolling_update\"}' >\"\${marker}.tmp\"
mv -f \"\${marker}.tmp\" \"\${marker}\"" >/dev/null 2>&1 || compensation_failed=true
    done
    ${compensation_failed} && log "警告：执行所有权接管补偿不完整；维护门禁保持关闭"
    die "无法原子接管升级 ${patch_id} 的执行所有权；未修改任何 RPM"
  fi
  locked_nodes=("${controllers[@]}")
  adopted_update_locks=true
  retain_update_locks=true
  update_locks_acquired=true
  previous_execution_id="${previous_owner}"
  previous_gate_patch_id="${patch_id}"
  log "已接管失败升级执行 previous_execution_id=${previous_owner} execution_id=${execution_id}"
}

assert_update_lock_ownership() {
  local host
  for host in "${locked_nodes[@]}"; do
    remote_run "${host}" "set -eu
jq_bin=/usr/local/libexec/jq-linux-amd64
test \"\$(cat '${upgrade_lock_name}/patch-id')\" = '${patch_id}'
test \"\$(cat '${upgrade_lock_name}/execution-id')\" = '${execution_id}'
\"\${jq_bin}\" -e --arg patch '${patch_id}' --arg execution '${execution_id}' '.schema_version == 2 and .patch_id == \$patch and .execution_id == \$execution and .mode == \"rolling_update\"' '${maintenance_marker}' >/dev/null" >/dev/null 2>&1 || return 1
  done
  if ${replicated_update_gate_active}; then
    control_update_gate "${leader_host}" acquire >/dev/null 2>&1 || return 1
  fi
}

acquire_update_locks() {
	local host command
	local -a lock_order=("${leader_host}")
  upgrade_lock_name="${remote_stage}/.cluster-update.lock"
  if [[ -n "${recoverable_previous_patch_id}" ]]; then
    transfer_failed_update_locks
    return
  fi
  if ${current_patch_maintenance_active}; then
    adopt_current_update_locks
    return
  fi
  command="set -eu; ${mutation_guard} mkdir '${upgrade_lock_name}'; umask 077; printf '%s\\n' '${patch_id}' >'${upgrade_lock_name}/patch-id'; printf '%s\\n' '${execution_id}' >'${upgrade_lock_name}/execution-id'; printf '%s\\n' '{\"schema_version\":2,\"patch_id\":\"${patch_id}\",\"execution_id\":\"${execution_id}\",\"mode\":\"rolling_update\"}' >'${maintenance_marker}.tmp'; mv -f '${maintenance_marker}.tmp' '${maintenance_marker}'"
	for host in "${controllers[@]}"; do
		[[ "${host}" == "${leader_host}" ]] || lock_order[${#lock_order[@]}]="${host}"
	done
	# The Leader owns mutation authority. Gate it first so no new automatic
	# recovery can enter while the remaining controller markers are published.
	for host in "${lock_order[@]}"; do
    if ! remote_run "${host}" "${command}"; then
      release_update_locks || true
      die "控制节点已有升级任务或无法建立升级锁：${host}"
    fi
    locked_nodes[${#locked_nodes[@]}]="${host}"
    update_locks_acquired=true
  done
  retain_update_locks=true
}

release_update_locks() {
	local host relock_host release_failed=false relock_failed=false
	local -a release_order=()
	((${#locked_nodes[@]} > 0)) || { update_locks_acquired=false; return 0; }
	# Verify every marker first. This prevents a partial unlock when one node has
	# lost its lock identity or is unreachable before release starts.
	for host in "${locked_nodes[@]}"; do
		remote_run "${host}" "set -eu; jq_bin=/usr/local/libexec/jq-linux-amd64; test \"\$(cat '${upgrade_lock_name}/patch-id')\" = '${patch_id}'; test \"\$(cat '${upgrade_lock_name}/execution-id')\" = '${execution_id}'; test -x \"\${jq_bin}\"; \"\${jq_bin}\" -e --arg patch '${patch_id}' --arg execution '${execution_id}' '.schema_version == 2 and .patch_id == \$patch and .execution_id == \$execution and .mode == \"rolling_update\"' '${maintenance_marker}' >/dev/null" >/dev/null 2>&1 || return 1
	done
	for host in "${locked_nodes[@]}"; do
		[[ "${host}" == "${leader_host}" ]] || release_order[${#release_order[@]}]="${host}"
	done
	for host in "${locked_nodes[@]}"; do
		[[ "${host}" != "${leader_host}" ]] || release_order[${#release_order[@]}]="${host}"
	done
	# Release the current Leader last so automatic mutations remain blocked
	# until every follower has already left software-update maintenance.
	for host in "${release_order[@]}"; do
		if ! remote_run "${host}" "set -eu; ${mutation_guard} test \"\$(cat '${upgrade_lock_name}/execution-id')\" = '${execution_id}'; rm -f '${maintenance_marker}.tmp' '${maintenance_marker}'; rm -rf '${upgrade_lock_name}'" >/dev/null 2>&1; then
			release_failed=true
			break
		fi
	done
	if ${release_failed}; then
		# A failed multi-node release is compensated by restoring the same patch
		# marker everywhere, including nodes already unlocked in this attempt.
		for relock_host in "${locked_nodes[@]}"; do
			if ! remote_run "${relock_host}" "set -eu; ${mutation_guard} if test -f '${upgrade_lock_name}/execution-id'; then test \"\$(cat '${upgrade_lock_name}/execution-id')\" = '${execution_id}'; fi; mkdir -p '${upgrade_lock_name}'; printf '%s\\n' '${patch_id}' >'${upgrade_lock_name}/patch-id'; printf '%s\\n' '${execution_id}' >'${upgrade_lock_name}/execution-id'; umask 077; printf '%s\\n' '{\"schema_version\":2,\"patch_id\":\"${patch_id}\",\"execution_id\":\"${execution_id}\",\"mode\":\"rolling_update\"}' >'${maintenance_marker}.tmp'; mv -f '${maintenance_marker}.tmp' '${maintenance_marker}'" >/dev/null 2>&1; then
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
  local transaction="$1" host remote_dir="${private_root}/inbox/${patch_id}"
  for host in "${controllers[@]}"; do
    remote_run "${host}" "rm -f '${remote_dir}/package.cgpatch.${transaction}.tmp' '${remote_dir}/package.json.${transaction}.tmp'" >/dev/null 2>&1 || true
  done
}

publish_update_artifacts() {
  local host remote_dir="${update_root}/${patch_id}"
  local package_source="${PWD}/package.cgpatch" metadata_source="${PWD}/package.json"
  local package_sha input_sha metadata_sha transaction private_dir package_temporary metadata_temporary

  # Direct CLI upgrades keep using the operator-provided package. Managed jobs
  # use a root-private snapshot as input and publish only through the service
  # account, so root never opens the shared update directory.
  [[ -n "${managed_job_dir}" ]] || return 0
  [[ -f "${package_source}" && ! -L "${package_source}" && -f "${metadata_source}" && ! -L "${metadata_source}" ]] ||
    die "控制台升级目录缺少升级包或元数据"
  [[ -f "${input_patch_file}" && ! -L "${input_patch_file}" ]] || die "私有升级任务输入快照不存在"

  package_sha="$(sha256_file "${package_source}")"
  input_sha="$(sha256_file "${input_patch_file}")"
  [[ "${input_sha}" == "${package_sha}" ]] || die "私有升级任务输入不一致"
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
  private_dir="${private_root}/inbox/${patch_id}"
  package_temporary="${private_dir}/package.cgpatch.${transaction}.tmp"
  metadata_temporary="${private_dir}/package.json.${transaction}.tmp"
  log "向 ${#controllers[@]} 个控制节点分发已验签升级包并校验 SHA-256"
  for host in "${controllers[@]}"; do
    if ! remote_run "${host}" "set -eu; install -d -m 0700 '${private_dir}'; runuser -u clusterguard -- install -d -m 0770 '${remote_dir}'" >/dev/null 2>&1 ||
      ! remote_copy "${host}" "${package_source}" "${package_temporary}" >/dev/null 2>&1 ||
      ! remote_run "${host}" "set -eu; printf '%s  %s\\n' '${package_sha}' '${package_temporary}' | sha256sum -c - >/dev/null; chmod 0600 '${package_temporary}'; mv -f '${package_temporary}' '${private_dir}/package.cgpatch'" >/dev/null 2>&1 ||
      ! remote_copy "${host}" "${metadata_source}" "${metadata_temporary}" >/dev/null 2>&1 ||
      ! remote_run "${host}" "set -eu; printf '%s  %s\\n' '${metadata_sha}' '${metadata_temporary}' | sha256sum -c - >/dev/null; chmod 0600 '${metadata_temporary}'; mv -f '${metadata_temporary}' '${private_dir}/package.json'; runuser -u clusterguard -- bash -c 'set -eu; d=\"\$1\"; n=\"\$2\"; t=\"\$(mktemp \"\$d/.publish.XXXXXXXX\")\"; trap '\''rm -f -- \"\$t\"'\'' EXIT; cat >\"\$t\"; chmod 0640 \"\$t\"; mv -fT \"\$t\" \"\$d/\$n\"; trap - EXIT' sh '${remote_dir}' package.cgpatch < '${private_dir}/package.cgpatch'; runuser -u clusterguard -- bash -c 'set -eu; d=\"\$1\"; n=\"\$2\"; t=\"\$(mktemp \"\$d/.publish.XXXXXXXX\")\"; trap '\''rm -f -- \"\$t\"'\'' EXIT; cat >\"\$t\"; chmod 0640 \"\$t\"; mv -fT \"\$t\" \"\$d/\$n\"; trap - EXIT' sh '${remote_dir}' package.json < '${private_dir}/package.json'" >/dev/null 2>&1; then
      cleanup_update_artifact_temps "${transaction}"
      die "无法向控制节点 ${host} 分发并验证升级包；尚未建立维护门禁，也未修改任何 RPM"
    fi
  done
  progress_replication_enabled=true
  log "升级包已在全部控制节点完成 SHA-256 校验和原子发布"
}

publish_update_progress() {
  local host status_source="${PWD}/status.json"
  [[ -f "${status_source}" && -f "${journal_events_file}" ]] || return 0
  publish_local_update_file "${status_source}" status.json ||
    log "警告：本机控制台状态投影失败，私有升级记录仍已保留"
  publish_local_update_file "${journal_events_file}" events.jsonl ||
    log "警告：本机控制台事件投影失败，私有升级记录仍已保留"

  remote_publish_private_file() {
    local target_host="$1" source="$2" destination="$3"
    local remote_dir="${update_root}/${patch_id}"
    local remote_history="${private_root}/history/${patch_id}"
    local remote_source="${private_root}/inbox/${patch_id}/.${destination}.$$"
    remote_copy "${target_host}" "${source}" "${remote_source}" >/dev/null 2>&1 || return 1
    remote_run "${target_host}" "set -eu; install -d -m 0700 '${remote_history}'; chmod 0600 '${remote_source}'; mv -f '${remote_source}' '${remote_history}/${destination}'; runuser -u clusterguard -- bash -c 'set -eu; d=\"\$1\"; n=\"\$2\"; t=\"\$(mktemp \"\$d/.publish.XXXXXXXX\")\"; trap '\''rm -f -- \"\$t\"'\'' EXIT; cat >\"\$t\"; chmod 0640 \"\$t\"; mv -fT \"\$t\" \"\$d/\$n\"; trap - EXIT' sh '${remote_dir}' '${destination}' < '${remote_history}/${destination}'" >/dev/null 2>&1
  }

  for host in "${controllers[@]}"; do
    if ! remote_publish_private_file "${host}" "${status_source}" status.json; then
      log "警告：控制节点 ${host} 的升级状态同步失败"
      continue
    fi
    if ! remote_publish_private_file "${host}" "${journal_events_file}" events.jsonl; then
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
  jq -cn --arg execution_id "${execution_id}" --arg patch_id "${patch_id}" --arg mode "${update_mode}" --arg status "${status}" --arg node "${node}" --arg message "${message}" \
    --arg phase "${phase}" --argjson current "${current}" --argjson total "${total}" \
    --arg source "${source_version}" --arg target "${target_version}" --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{execution_id:$execution_id,patch_id:$patch_id,mode:$mode,status:$status,node:$node,message:$message,phase:$phase,current:$current,total:$total,source:$source,target:$target,updated_at:$at}' >"${event_tmp}"
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
  jq -n --arg execution_id "${execution_id}" --arg patch_id "${patch_id}" --arg mode "${update_mode}" --arg status "${job_status}" \
    --arg node "${node}" --arg message "${message}" --arg phase "${phase}" \
    --arg started_at "${started_at}" --arg updated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg finished_at "${finished_at}" \
    --argjson maintenance_active "${maintenance}" --argjson current "${current}" --argjson total "${total}" --argjson percent "${percent}" \
    '{execution_id:$execution_id,patch_id:$patch_id,mode:$mode,status:$status,node:$node,message:$message,
      maintenance_active:$maintenance_active,automatic_failover_available:($maintenance_active | not),
      started_at:$started_at,updated_at:$updated_at,finished_at:(if $finished_at == "" then null else $finished_at end),
      progress:{phase:$phase,current:$current,total:$total,percent:$percent}}' >"${PWD}/status.json.tmp"
  chown root:clusterguard "${PWD}/status.json.tmp" 2>/dev/null || true
  chmod 0640 "${PWD}/status.json.tmp"
  mv -f "${PWD}/status.json.tmp" "${PWD}/status.json"
  publish_update_progress
}

wait_node_ready() {
	local host="$1" expected="$2" attempt version status controller_ready data_ready facts sleep_seconds
	local started_epoch deadline_epoch now_epoch controller_summary="status_unavailable" data_summary="service_state_unavailable"
	started_epoch="$(date +%s)"
	deadline_epoch=$((started_epoch + node_ready_timeout_seconds))
	for attempt in $(seq 1 60); do
		version="$(remote_package_version "${host}" 2>/dev/null || true)"
		if [[ "${version}" == "${expected}" ]]; then
			controller_ready=true
			data_ready=true
			if is_controller "${host}"; then
				status="$(control_status "${host}" 2>/dev/null || true)"
				if [[ -n "${status}" ]]; then
					controller_summary="$(jq -c '.result | {ready:(.ready // false),reason:(.readiness_reason // .reason // "unknown"),role:(.role // "unknown"),leader_known:(.leader_known // false),quorum_confirmed:(.quorum_confirmed // false)}' <<<"${status}" 2>/dev/null || printf 'status_unreadable')"
				else
					controller_summary="status_unavailable"
				fi
				[[ -n "${status}" ]] && jq -e '.status == "ok" and .result.ready == true and .result.leader_known == true' <<<"${status}" >/dev/null 2>&1 || controller_ready=false
			fi
			if is_data_node "${host}"; then
				if remote_run "${host}" "systemctl is-active --quiet clusterguard-agent.service && systemctl is-active --quiet clusterguard-agent-reconcile.timer" >/dev/null 2>&1; then
					data_summary="agent_and_reconcile_active"
				else
					data_summary="agent_or_reconcile_inactive"
					data_ready=false
				fi
			fi
			${controller_ready} && ${data_ready} && return 0
		fi
		if ((attempt == 1 || attempt % 10 == 0)); then
			facts="$(node_service_facts "${host}" 2>/dev/null || true)"
			log "等待节点就绪 host=${host} attempt=${attempt} expected=${expected} observed=${version:-unknown} controller=${controller_summary} data=${data_summary} services=${facts:-unavailable}"
		fi
		now_epoch="$(date +%s)"
		((attempt < 60 && now_epoch < deadline_epoch)) || break
		sleep_seconds="${node_ready_delay_seconds}"
		if ((sleep_seconds > deadline_epoch - now_epoch)); then sleep_seconds=$((deadline_epoch - now_epoch)); fi
		((sleep_seconds > 0)) && sleep "${sleep_seconds}"
	done
	facts="$(node_service_facts "${host}" 2>/dev/null || true)"
	log "错误：节点在真实 ${node_ready_timeout_seconds} 秒上限内未就绪 host=${host} expected=${expected} observed=${version:-unknown} controller=${controller_summary} data=${data_summary} services=${facts:-unavailable}" >&2
	return 1
}

install_node_rpm() {
  local host="$1" rpm_path="$2" expected="$3" remote_dir remote_file rpm_options services="" service
  local expected_product_version expected_release expected_state expected_protocol service_commands=""
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
  assert_update_lock_ownership || { log "升级执行所有权已改变，停止节点变更 host=${host}"; return 1; }
  if is_controller "${host}"; then
    service_commands="systemctl enable 'clusterguard-ha.service'; systemctl enable 'clusterguard-update-helper.service';"
    services="clusterguard-ha.service"
    # The Leader Helper owns this running job and is refreshed asynchronously
    # by clusterguard-update-job.sh after the terminal status is durable.
    if [[ "${host}" == "${leader_host}" ]]; then
      :
    else
      :
      services="${services} clusterguard-update-helper.service"
    fi
  fi
  if is_data_node "${host}"; then
    service_commands="${service_commands} systemctl enable 'clusterguard-agent.service'; systemctl enable --now 'clusterguard-agent-reconcile.timer';"
    services="${services} clusterguard-agent.service"
  fi
  for service in ${services}; do
    service_commands="${service_commands} systemctl restart '${service}';"
  done
  remote_run "${host}" "set -eu; ${mutation_guard}
if test -f '${upgrade_lock_name}/execution-id'; then test \"\$(cat '${upgrade_lock_name}/execution-id')\" = '${execution_id}'; fi
chmod 0600 '${remote_file}.tmp'
mv -f '${remote_file}.tmp' '${remote_file}'
rpm -Uvh ${rpm_options} '${remote_file}'
systemctl daemon-reload
${service_commands}" || return 1
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
log_all_node_service_facts

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
      all_nodes_at_source=false
      verify_node_contract "${host}" "${target_product_version}" "${target_release}" "${target_state_format}" "${target_update_protocol}" ||
        die "${host} 的目标版本合同校验失败"
      ;;
    *)
      die "${host} 当前版本 ${installed} 不在补丁兼容范围 ${source_version}/${target_version}"
      ;;
  esac
done

detect_current_update_lock
detect_recoverable_failed_update
${current_patch_maintenance_inconsistent} && die "当前升级包维护锁不完整；为避免误清门禁，未执行任何变更"
if ${resume_requested} && ! ${current_patch_maintenance_active}; then
  die "续跑只允许复用全部控制节点上的同一升级包完整维护锁；未执行任何变更"
fi
if [[ -n "${recoverable_previous_patch_id}" ]] && ! ${all_nodes_at_source}; then
  die "失败升级维护锁只能在所有节点均已回到源版本 ${source_version} 后接管"
fi

build_order
printf '\nClusterGuard HA 滚动%s计划\n' "$(${rollback_requested} && printf '回退' || printf '升级')"
printf '  升级包 ID  : %s\n' "${patch_id}"
printf '  当前合同   : %s\n' "${rollback_version}"
printf '  目标合同   : %s\n' "${desired_version}"
printf '  固定顺序   : followers -> data-only -> leader\n'
printf '  Leader     : %s（最后处理）\n' "${leader_host}"
printf '  数据库变更 : false\n'
if [[ -n "${recoverable_previous_patch_id}" ]]; then
  printf '  维护恢复   : 接管已失败升级 %s 的现有门禁\n' "${recoverable_previous_patch_id}"
elif ${current_patch_maintenance_active}; then
  printf '  维护恢复   : 复用当前升级包的现有门禁\n'
fi
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
acquire_update_locks
journal_started=true
write_journal running "" "rolling update started" preparing 0 "${total_nodes}"
write_journal running "" "maintenance gates are being acquired" locking 0 "${total_nodes}"
maintenance_activity_mode="idle"
if ! ${rollback_requested} || ${current_patch_maintenance_active} || [[ -n "${recoverable_previous_patch_id}" ]]; then
  maintenance_activity_mode="stale_automatic"
fi
wait_cluster_idle "${leader_host}" true "${maintenance_activity_mode}" || die "维护门禁建立后控制面未在时限内恢复一致"
if all_controllers_support_replicated_gate; then
  acquire_replicated_update_gate || die "无法建立 Raft 升级维护门禁；未修改 RPM"
fi
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
	  if ! wait_cluster_idle "${leader_host}" true "${maintenance_activity_mode}"; then
    upgrade_failed=true; failure_node="${host}"; break
  fi
  log "更新节点：${host} (${installed} -> ${desired_version})"
  write_journal updating "${host}" "installing target RPM" updating "$((node_index - 1))" "${total_nodes}"
  updated_nodes[${#updated_nodes[@]}]="${host}"
  if ! install_node_rpm "${host}" "${desired_rpm}" "${desired_version}"; then
    upgrade_failed=true; failure_node="${host}"; break
  fi
  write_journal verified "${host}" "node version and readiness verified" updating "${node_index}" "${total_nodes}"
	  if [[ "${host}" != "${leader_host}" ]] && ! wait_cluster_idle "${leader_host}" true "${maintenance_activity_mode}"; then
    upgrade_failed=true; failure_node="${host}"; break
  fi
done

if ! ${upgrade_failed}; then
  write_journal finalizing "" "verifying all node contracts and maintenance release" finalizing "${total_nodes}" "${total_nodes}"
  final_activity_mode="idle"
  ${rollback_requested} && final_activity_mode="${maintenance_activity_mode}"
  if ! wait_cluster_idle "" true "${final_activity_mode}"; then
    upgrade_failed=true
    failure_node="control-plane"
  fi
fi

if ${upgrade_failed}; then
  rollback_in_progress=true
  write_journal rolling_back "${failure_node}" "node update failed; automatic rollback started" rollback "${#updated_nodes[@]}" "${#updated_nodes[@]}"
  if ! rollback_updated_nodes; then
    write_journal rollback_failed "${failure_node}" "automatic rollback incomplete; maintenance gate retained" rollback 0 "${#updated_nodes[@]}"
    die "节点 ${failure_node} 更新失败且自动回退不完整；维护门禁已保留，请人工处置后使用 --resume"
  fi
  verify_cluster_idle "" true "${maintenance_activity_mode}"
  if ! finish_update_maintenance "${maintenance_activity_mode}"; then
    write_journal rollback_lock_release_failed "${failure_node}" "rollback succeeded but maintenance release failed" rollback "${#updated_nodes[@]}" "${#updated_nodes[@]}"
    die "自动回退完成，但部分维护锁释放失败；变更仍被安全阻断，请修复连通性后使用 --resume"
  fi
  retain_update_locks=false
  write_journal rolled_back "${failure_node}" "automatic rollback completed and maintenance released" rolled_back "${#updated_nodes[@]}" "${#updated_nodes[@]}"
  prune_update_artifacts || log "警告：自动回退已完成，但部分旧升级材料需要稍后清理"
  die "节点 ${failure_node} 更新失败，已完成自动回退"
fi

for host in "${all_nodes[@]}"; do
  installed="$(remote_package_version "${host}")"
  [[ "${installed}" == "${desired_version}" ]] || die "最终版本校验失败：${host}=${installed}"
  verify_node_contract "${host}" "${desired_product_version}" "${desired_release}" "${desired_state_format}" "${desired_update_protocol}" ||
    die "最终版本合同校验失败：${host}"
done
final_activity_mode="idle"
${rollback_requested} && final_activity_mode="${maintenance_activity_mode}"
verify_cluster_idle "" true "${final_activity_mode}"
finish_update_maintenance "${final_activity_mode}" || die "无法验证并释放集群维护门禁；维护状态保留，请检查执行日志后续跑"
# The upgraded Leader must reconcile any maintenance-only bootstrap exception
# before the maintenance gate is released, and final convergence remains strict.
post_release_activity_mode="idle"
${rollback_requested} && post_release_activity_mode="${maintenance_activity_mode}"
wait_cluster_idle "" false "${post_release_activity_mode}" || die "维护门禁释放后控制面未在时限内恢复一致或仍有活动任务"
helper_refresh_unit="clusterguard-update-helper-refresh-$(date +%s)-$$"
remote_run "${leader_host}" "systemd-run --quiet --unit '${helper_refresh_unit}' --on-active=5s /usr/bin/systemctl restart clusterguard-update-helper.service" || {
  write_journal failed "${leader_host}" "all node contracts passed, but Leader update Helper refresh could not be scheduled" failed "${total_nodes}" "${total_nodes}"
  die "所有节点已达到目标版本，但 Leader 软件更新 Helper 自刷新调度失败；请重启 clusterguard-update-helper.service 后核验"
}
write_journal succeeded "" "all nodes and maintenance release verified" completed "${total_nodes}" "${total_nodes}"
prune_update_artifacts || log "警告：升级已成功，但部分旧升级材料需要稍后清理"
log "补丁完成：所有节点均为 ${desired_version}，控制面多数派和就绪状态已复核"
