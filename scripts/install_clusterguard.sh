#!/usr/bin/env bash
set -euo pipefail
export COPYFILE_DISABLE=1

# ClusterGuard HA offline multi-node installer. The installer is intentionally
# fail-closed: it renders a plan by default and mutates remote hosts only when
# --execute is supplied.

script_dir="$(cd "$(dirname "$0")" && pwd)"
controllers_raw=""
data_nodes_raw=""
application_rpm=""
database_package=""
database_package_dir="${CG_DATABASE_PACKAGE_DIR:-}"
database_package_kind=""
database_engine=""
database_version=""
postgresql_build_node=""
postgresql_build_jobs="${CG_POSTGRESQL_BUILD_JOBS:-}"
postgresql_build_min_free_gb=6
dependency_dir=""
postgresql_dependency_dir=""
ssh_user="root"
ssh_password="${CG_SSH_PASSWORD:-}"
ssh_passwords_raw=""
ssh_credentials_file=""
ssh_key=""
ssh_port=22
controller_data_root="/var/lib/clusterguard"
database_data_root="/data"
cluster_name=""
vip=""
vip_interface="ens160"
vip_prefix=24
database_port=""
api_port=3000
raft_port=10009
state_file="${PWD}/clusterguard-deployment-state.json"
secrets_file="${PWD}/clusterguard-deployment-secrets.env"
work_dir="${PWD}/clusterguard-site"
known_hosts_input=""
remote_stage="/var/lib/clusterguard/bootstrap"
mysql_root_password="${CG_NODE_MYSQL_ROOT_PASSWORD:-}"
mysql_root_remote_host="${CG_NODE_MYSQL_ROOT_REMOTE_HOST:-}"
postgresql_admin_password="${CG_POSTGRESQL_ADMIN_PASSWORD:-}"
postgresql_allowed_cidr="${CG_POSTGRESQL_ALLOWED_CIDR:-}"
controller_min_free_gb=2
database_min_free_gb=10
execute=false
assume_yes=false
accept_host_keys=false
control_only=false
data_on_arbitrators=false
activate_vip_reconcile=true
mysql_automatic_failover=false
postgresql_automatic_failover=false
agent_quorum_fencing=false
manual_failover_only=false
fencer_file=""
fencer_assets_dir=""
patch_trust_key="${CG_PATCH_TRUST_KEY:-}"
ssh_auth_mode="key"
jq_binary=""
known_hosts_file=""
new_known_hosts=false
api_controller_host=""

declare -a controller_nodes=()
declare -a data_nodes=()
declare -a all_nodes=()
declare -a node_roles=()
declare -a node_ids=()
declare -a node_names=()
declare -a node_hostnames=()
declare -a instance_ids=()
declare -a ssh_node_passwords=()

timestamp() { date '+%Y-%m-%d %H:%M:%S'; }
log() { printf '[%s] %s\n' "$(timestamp)" "$*"; }
warn() { printf '[%s] 警告：%s\n' "$(timestamp)" "$*" >&2; }
die() { printf '[%s] 错误：%s\n' "$(timestamp)" "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
ClusterGuard HA 多节点离线安装器

用法：
	  ./install_clusterguard.sh \
	    -l 192.168.40.81,192.168.40.82,192.168.40.83 \
	    -n 192.168.40.83,192.168.40.84,192.168.40.85 \
	    -g ./packages/clusterguard-ha-<version>-<release>.x86_64.rpm \
	    -r ./packages/database/<mysql-or-upsql-package>.tar.xz \
	    -u root -P 'YourSshPassword' -ld /data/clusterguard -nd /data \
	    --engine mysql --cluster-name mysql-production --vip 192.168.40.100 \
	    --interface ens160 --database-port 3306 \
	    --fencer /secure/clusterguard/fencing/site-fencer \
	    --fencer-assets /secure/clusterguard/fencing/site-config \
	    --accept-host-keys --execute

核心参数：
  -l, --arbitrator-nodes LIST  仲裁控制节点，逗号分隔；必须为至少 3 个的奇数
      --controller-nodes LIST  --arbitrator-nodes 的兼容别名
  -n, --data-nodes LIST        数据节点，逗号分隔；数量不受奇数限制
  -g, --application-rpm FILE   ClusterGuard HA RPM；省略时自动从本离线介质 packages/ 目录选择唯一 RPM
  -r, --database-package FILE  企业批准的 MySQL/UPSQL 二进制包，或 PostgreSQL 二进制/官方源码包
      --postgresql-source-package FILE
                                -r 的 PostgreSQL 源码包专用别名
      --database-package-dir DIR
                                仅扫描指定目录；默认优先使用 packages/database/，无匹配时检查 /opt
  -u, --ssh-user USER          SSH 用户，默认 root
  -P, --ssh-password PASS      SSH 密码；也可使用 CG_SSH_PASSWORD 或在真实安装开始时隐藏输入一次
  -p, --ssh-passwords LIST     节点 SSH 密码，逗号分隔，按去重后的 -l/-n 节点顺序对应
      --ssh-credentials-file FILE
                                节点级 SSH 密码文件；每行“节点地址=密码”，权限必须为 0600
  -ld, --controller-data DIR   控制面元数据和 Raft 根目录
  -nd, --database-data DIR     数据库数据根目录

集群参数：
      --engine ENGINE          mysql、postgresql 或 none；有数据节点时默认 mysql
      --database-version VER   数据库版本；PostgreSQL 安装时必填，例如 16.4
      --database-port NUMBER   数据库端口；MySQL 默认 3306，PostgreSQL 默认 5432
      --data-on-arbitrators    仲裁节点同时作为数据节点并安装 Agent
      --cluster-name NAME      固定集群名称，默认 ENGINE-ha-PORT
      --vip ADDRESS            可选的集群写入口 VIP
      --interface NAME         VIP 网卡，默认 ens160
      --prefix NUMBER          VIP CIDR，默认 24
      --mysql-port NUMBER      --database-port 的 MySQL 兼容别名
      --mysql-root-password P  指定 MySQL root 密码；省略则生成并保存到受保护文件
      --mysql-root-remote-host HOST
                                显式创建远程 root，例如 %；省略则不创建或修改远程 root
      --postgresql-password P  指定 PostgreSQL postgres 密码；省略则安全生成
      --postgresql-allowed-cidr CIDR
                                PostgreSQL 管理与复制网络；跨网段时必须显式指定
      --postgresql-build-node HOST
                                源码构建节点；默认第一个数据节点，只在该节点编译一次
      --postgresql-build-jobs N PostgreSQL 源码并行编译任务数；默认自动检测，最多 16
      --api-port NUMBER        控制台/API 端口，默认 3000
      --raft-port NUMBER       Raft 端口，默认 10009

离线与安全参数：
      --dependencies DIR       与目标系统匹配的基础运行依赖 RPM 目录
      --postgresql-dependencies DIR
                                可选的 PostgreSQL 源码编译依赖包 dependencies/ 目录；
                                省略时默认使用目标构建节点的联网软件源
      --ssh-key FILE           SSH 私钥
      --ssh-port NUMBER        SSH 端口，默认 22
      --known-hosts FILE       已审核的 SSH known_hosts
      --accept-host-keys       首次执行时采集并接受当前主机密钥
      --state-file FILE        固定 UUID 部署状态，默认当前目录下 JSON 文件
      --secrets-file FILE      受保护的站点秘密文件，默认当前目录下 env 文件
      --work-dir DIR           证书和节点配置工作目录
      --remote-stage DIR       远端暂存目录
      --controller-min-free-gb N
                                控制面目录最小可用空间，默认 2 GiB
      --database-min-free-gb N 数据目录最小可用空间，默认 10 GiB
      --control-only           只安装仲裁控制面，不安装数据库或 Agent
      --skip-mysql             --control-only 的兼容别名
      --no-vip-reconcile       安装后保持 VIP 自动收敛定时器关闭
      --fencer FILE            可选的外部隔离增强程序；安装为 /usr/local/libexec/clusterguard-fencer
      --fencer-assets DIR      外部隔离程序的站点配置目录；文件安装到 /etc/clusterguard/fencing/
      --manual-failover-only   显式关闭自动故障切换，仅保留人工受控切换
      --patch-trust-key FILE   补丁签名公钥；配置后可在控制台上传并受控滚动升级
      --plan                   仅显示计划（默认）
      --execute                校验后真实安装
  -y, --yes                    非交互确认
  -h, --help                   显示帮助

说明：
  1. 仅 -l + --control-only：部署奇数仲裁控制面。
  2. -l 与 -n 分离：仲裁控制节点和数据库/Agent 节点分开部署。
  3. -l 与 -n 重叠，或使用 --data-on-arbitrators：部署混合节点。
  4. 固定节点 UUID 保存在 --state-file，并同时写入远端 node.json。
  5. 默认不会修改远端；必须显式追加 --execute。
  6. 不使用跳过依赖检查的 RPM 参数，不关闭 SSH 主机密钥校验，不在日志打印数据库密码。
EOF
}

need_value() {
  (($# >= 2)) && [[ -n "${2:-}" ]] || die "参数 $1 缺少值"
}

parse_args() {
  while (($#)); do
    case "$1" in
      -l|--arbitrator-nodes|--controller-nodes) need_value "$@"; controllers_raw="$2"; shift 2 ;;
      -n|--data-nodes) need_value "$@"; data_nodes_raw="$2"; shift 2 ;;
      -g|--application-rpm) need_value "$@"; application_rpm="$2"; shift 2 ;;
      -r|--database-package|--mysql-package|--postgresql-source-package) need_value "$@"; database_package="$2"; shift 2 ;;
      --database-package-dir) need_value "$@"; database_package_dir="$2"; shift 2 ;;
      -u|--ssh-user) need_value "$@"; ssh_user="$2"; shift 2 ;;
      -P|--ssh-password) need_value "$@"; ssh_password="$2"; shift 2 ;;
      -p|--ssh-passwords) need_value "$@"; ssh_passwords_raw="$2"; shift 2 ;;
      --ssh-credentials-file) need_value "$@"; ssh_credentials_file="$2"; shift 2 ;;
      -ld|--controller-data) need_value "$@"; controller_data_root="$2"; shift 2 ;;
      -nd|--database-data|--mysql-data) need_value "$@"; database_data_root="$2"; shift 2 ;;
      --engine) need_value "$@"; database_engine="$2"; shift 2 ;;
      --database-version|--postgresql-version) need_value "$@"; database_version="$2"; shift 2 ;;
      --database-port|--mysql-port|--postgresql-port) need_value "$@"; database_port="$2"; shift 2 ;;
      --data-on-arbitrators) data_on_arbitrators=true; shift ;;
      --cluster-name) need_value "$@"; cluster_name="$2"; shift 2 ;;
      --vip) need_value "$@"; vip="$2"; shift 2 ;;
      --interface) need_value "$@"; vip_interface="$2"; shift 2 ;;
      --prefix) need_value "$@"; vip_prefix="$2"; shift 2 ;;
      --mysql-root-password) need_value "$@"; mysql_root_password="$2"; shift 2 ;;
      --mysql-root-remote-host) need_value "$@"; mysql_root_remote_host="$2"; shift 2 ;;
      --postgresql-password) need_value "$@"; postgresql_admin_password="$2"; shift 2 ;;
      --postgresql-allowed-cidr) need_value "$@"; postgresql_allowed_cidr="$2"; shift 2 ;;
      --postgresql-build-node) need_value "$@"; postgresql_build_node="$2"; shift 2 ;;
      --postgresql-build-jobs) need_value "$@"; postgresql_build_jobs="$2"; shift 2 ;;
      --api-port) need_value "$@"; api_port="$2"; shift 2 ;;
      --raft-port) need_value "$@"; raft_port="$2"; shift 2 ;;
      --ssh-key) need_value "$@"; ssh_key="$2"; shift 2 ;;
      --ssh-port) need_value "$@"; ssh_port="$2"; shift 2 ;;
      --known-hosts) need_value "$@"; known_hosts_input="$2"; shift 2 ;;
      --accept-host-keys) accept_host_keys=true; shift ;;
      --dependencies) need_value "$@"; dependency_dir="$2"; shift 2 ;;
      --postgresql-dependencies) need_value "$@"; postgresql_dependency_dir="$2"; shift 2 ;;
      --state-file) need_value "$@"; state_file="$2"; shift 2 ;;
      --secrets-file) need_value "$@"; secrets_file="$2"; shift 2 ;;
      --work-dir) need_value "$@"; work_dir="$2"; shift 2 ;;
      --remote-stage) need_value "$@"; remote_stage="$2"; shift 2 ;;
      --controller-min-free-gb) need_value "$@"; controller_min_free_gb="$2"; shift 2 ;;
      --database-min-free-gb) need_value "$@"; database_min_free_gb="$2"; shift 2 ;;
      --control-only|--skip-mysql) control_only=true; shift ;;
      --no-vip-reconcile) activate_vip_reconcile=false; shift ;;
      --fencer) need_value "$@"; fencer_file="$2"; shift 2 ;;
      --fencer-assets) need_value "$@"; fencer_assets_dir="$2"; shift 2 ;;
      --manual-failover-only) manual_failover_only=true; shift ;;
      --patch-trust-key) need_value "$@"; patch_trust_key="$2"; shift 2 ;;
      --plan) execute=false; shift ;;
      --execute) execute=true; shift ;;
      -y|--yes) assume_yes=true; shift ;;
      -h|--help) usage; exit 0 ;;
      *) die "未知参数：$1" ;;
    esac
  done
}

discover_bundled_dependencies() {
  [[ -z "${dependency_dir}" ]] || return 0
  local bundled="${script_dir}/dependencies"
  [[ -d "${bundled}" ]] || return 0
  find "${bundled}" -maxdepth 1 -type f -name '*.rpm' -print -quit | grep -q . || return 0
  dependency_dir="${bundled}"
  log "自动使用离线介质依赖目录：${dependency_dir}"
}

discover_bundled_patch_trust_key() {
  [[ -z "${patch_trust_key}" ]] || return 0
  local candidate
  for candidate in "${script_dir}/trust/patch-signing-public.pem" "${script_dir}/patch-signing-public.pem"; do
    if [[ -f "${candidate}" && ! -L "${candidate}" ]]; then
      patch_trust_key="${candidate}"
      log "自动使用补丁签名公钥：${patch_trust_key}"
      return 0
    fi
  done
}

contains() {
  local wanted="$1"; shift
  local item
  for item in "$@"; do [[ "${item}" == "${wanted}" ]] && return 0; done
  return 1
}

controller_node_contains() {
  local wanted="$1" index
  for ((index=0; index<${#controller_nodes[@]}; index++)); do [[ "${controller_nodes[${index}]}" == "${wanted}" ]] && return 0; done
  return 1
}

data_node_contains() {
  local wanted="$1" index
  for ((index=0; index<${#data_nodes[@]}; index++)); do [[ "${data_nodes[${index}]}" == "${wanted}" ]] && return 0; done
  return 1
}

all_node_contains() {
  local wanted="$1" index
  for ((index=0; index<${#all_nodes[@]}; index++)); do [[ "${all_nodes[${index}]}" == "${wanted}" ]] && return 0; done
  return 1
}

parse_list() {
  local raw="$1" destination="$2" item
  local -a parsed=()
  IFS=',' read -r -a parsed <<<"${raw}"
  case "${destination}" in
    controller_nodes) controller_nodes=() ;;
    data_nodes) data_nodes=() ;;
    *) die "内部错误：不支持的节点列表 ${destination}" ;;
  esac
  for item in "${parsed[@]}"; do
    item="${item#"${item%%[![:space:]]*}"}"
    item="${item%"${item##*[![:space:]]}"}"
    [[ -n "${item}" ]] || continue
    [[ "${item}" =~ ^[A-Za-z0-9._-]+$ ]] || die "节点地址必须是 IPv4 或 DNS 名称：${item}"
    case "${destination}" in
      controller_nodes) controller_node_contains "${item}" || controller_nodes+=("${item}") ;;
      data_nodes) data_node_contains "${item}" || data_nodes+=("${item}") ;;
    esac
  done
  case "${destination}" in
    controller_nodes) ((${#controller_nodes[@]} > 0)) || die "仲裁节点列表不能为空" ;;
    data_nodes) ((${#data_nodes[@]} > 0)) || die "数据节点列表不能为空" ;;
  esac
}

append_data_node() {
  data_node_contains "$1" || data_nodes+=("$1")
}

node_index() {
  local wanted="$1" index
  for ((index=0; index<${#all_nodes[@]}; index++)); do
    if [[ "${all_nodes[${index}]}" == "${wanted}" ]]; then
      printf '%s\n' "${index}"
      return 0
    fi
  done
  return 1
}

node_role_for() { local index; index="$(node_index "$1")" || die "未知节点：$1"; printf '%s\n' "${node_roles[${index}]}"; }
node_id_for() { local index; index="$(node_index "$1")" || die "未知节点：$1"; printf '%s\n' "${node_ids[${index}]:-}"; }
node_name_for() { local index; index="$(node_index "$1")" || die "未知节点：$1"; printf '%s\n' "${node_names[${index}]:-}"; }
node_hostname_for() { local index; index="$(node_index "$1")" || die "未知节点：$1"; printf '%s\n' "${node_hostnames[${index}]:-}"; }
instance_id_for() { local index; index="$(node_index "$1")" || die "未知节点：$1"; printf '%s\n' "${instance_ids[${index}]:-}"; }

set_node_identity() {
  local index
  index="$(node_index "$1")" || die "未知节点：$1"
  node_ids[${index}]="$2"
  node_names[${index}]="$3"
  node_hostnames[${index}]="$4"
  instance_ids[${index}]="$5"
}

set_instance_id() {
  local index
  index="$(node_index "$1")" || die "未知节点：$1"
  instance_ids[${index}]="$2"
}

valid_port() { [[ "$1" =~ ^[0-9]+$ && "$1" -ge 1 && "$1" -le 65535 ]]; }
valid_ipv4() {
  local address="$1" octet
  local -a octets=()
  [[ "${address}" =~ ^([0-9]{1,3}[.]){3}[0-9]{1,3}$ ]] || return 1
  IFS=. read -r -a octets <<<"${address}"
  for octet in "${octets[@]}"; do ((10#${octet} <= 255)) || return 1; done
}

valid_ipv4_cidr() {
  local value="$1" address prefix
  [[ "${value}" =~ ^([^/]+)/([0-9]|[12][0-9]|3[0-2])$ ]] || return 1
  address="${value%/*}"; prefix="${value##*/}"
  valid_ipv4 "${address}" && ((10#${prefix} >= 0 && 10#${prefix} <= 32))
}

valid_absolute_root() {
  [[ "$1" == /* && "$1" != "/" && "$1" != *$'\n'* && "$1" != *$'\r'* ]] || return 1
  case "/${1#/}/" in */../*|*/./*) return 1 ;; esac
  return 0
}

archive_contains() {
  local listing="$1" pattern="$2"
  awk -v pattern="${pattern}" '$0 ~ pattern {found=1} END {exit !found}' "${listing}"
}

archive_has_single_root() {
  local listing="$1"
  awk '
    {
      original=$0
      path=$0
      sub(/^\.\//, "", path)
      sub(/\/$/, "", path)
      if (path == "") next
      count=split(path, parts, "/")
      if (root == "") root=parts[1]
      if (parts[1] != root) bad=1
      if (count == 1 && original !~ /\/$/) bad=1
    }
    END {exit !(root != "" && !bad)}
  ' "${listing}"
}

validate_database_media() {
  [[ "${database_engine}" == "none" ]] && return
  local listing validation_error="" required pattern binary_complete source_complete
  listing="$(mktemp /tmp/clusterguard-database-listing.XXXXXX)"
  if ! tar -tf "${database_package}" >"${listing}" 2>/dev/null; then
    rm -f "${listing}"
    die "数据库介质不是可读取的 tar 包：${database_package}"
  fi
  if ! awk '$0 ~ /^\// || $0 ~ /(^|\/)\.\.(\/|$)/ {bad=1} END {exit bad}' "${listing}"; then
    validation_error="数据库介质包含绝对路径或目录越界项"
  elif ! archive_has_single_root "${listing}"; then
    validation_error="数据库介质必须只包含一个顶层目录，不能包含根目录散文件"
  fi
  case "${database_engine}" in
    mysql)
      database_package_kind="binary"
      for required in mysqld mysql mysqldump; do
        pattern="(^|/)bin/${required}$"
        if ! archive_contains "${listing}" "${pattern}"; then validation_error="MySQL/UPSQL 介质缺少 bin/${required}"; break; fi
      done
      ;;
    postgresql)
      binary_complete=true
      for required in initdb postgres psql pg_basebackup pg_rewind pg_controldata; do
        pattern="(^|/)bin/${required}$"
        if ! archive_contains "${listing}" "${pattern}"; then binary_complete=false; break; fi
      done
      if [[ "${binary_complete}" == "true" ]]; then
        database_package_kind="binary"
      else
        source_complete=true
        for pattern in '(^|/)configure$' '(^|/)src/backend/Makefile$' '(^|/)src/bin/initdb/Makefile$' '(^|/)src/include/pg_config.h.in$' '(^|/)contrib/Makefile$'; do
          if ! archive_contains "${listing}" "${pattern}"; then source_complete=false; break; fi
        done
        if [[ "${source_complete}" == "true" ]]; then
          database_package_kind="postgresql-source"
        else
          validation_error="PostgreSQL 介质既不是完整二进制包，也不是完整官方源码包"
        fi
      fi
      ;;
  esac
  rm -f "${listing}"
  [[ -z "${validation_error}" ]] || die "${validation_error}"
}

find_jq() {
  jq_binary="${CG_JQ_BINARY:-$(command -v jq 2>/dev/null || true)}"
  if [[ -z "${jq_binary}" && -x "${script_dir}/tools/jq-linux-amd64" ]]; then
    jq_binary="${script_dir}/tools/jq-linux-amd64"
  fi
  [[ -x "${jq_binary}" ]] || die "缺少 jq；请使用标准离线介质中的 tools/jq-linux-amd64"
}

verify_sidecar() {
  local file="$1" sidecar="${1}.sha256" expected actual
  [[ -f "${sidecar}" ]] || return 0
  expected="$(awk 'NR==1 {print $1}' "${sidecar}")"
  [[ "${expected}" =~ ^[0-9a-fA-F]{64}$ ]] || die "摘要文件格式无效：${sidecar}"
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "${file}" | awk '{print $1}')"
  else
    actual="$(shasum -a 256 "${file}" | awk '{print $1}')"
  fi
  [[ "${actual}" == "${expected}" ]] || die "介质摘要不匹配：${file}"
}

resolve_database_package() {
  [[ "${database_engine}" == "none" || -n "${database_package}" ]] && return 0
  local -a search_dirs candidates unique
  local directory candidate base normalized existing version_match=false engine_match=false version_normalized
  local candidate_count=0 unique_count=0 candidate_index unique_index
  search_dirs=()
  candidates=()
  unique=()
  if [[ -n "${database_package_dir}" ]]; then
    search_dirs+=("${database_package_dir}")
  else
    search_dirs+=("${script_dir}/packages/database" /opt)
  fi
  for directory in "${search_dirs[@]}"; do
    [[ -d "${directory}" ]] || continue
    while IFS= read -r candidate; do
      base="$(basename "${candidate}")"
      normalized="$(printf '%s' "${base}" | tr '[:upper:]' '[:lower:]')"
      engine_match=false
      case "${database_engine}" in
        mysql) [[ "${normalized}" == *mysql* || "${normalized}" == *upsql* ]] && engine_match=true ;;
        postgresql) [[ "${normalized}" == *postgres* ]] && engine_match=true ;;
      esac
      [[ "${engine_match}" == "true" ]] || continue
      version_match=true
      version_normalized="$(printf '%s' "${database_version}" | tr '[:upper:]' '[:lower:]')"
      [[ -z "${version_normalized}" || "${normalized}" == *"${version_normalized}"* ]] || version_match=false
      [[ "${version_match}" == "true" ]] || continue
      candidates[${candidate_count}]="${candidate}"
      candidate_count=$((candidate_count + 1))
    done < <(find "${directory}" -maxdepth 1 -type f \( -name '*.tar' -o -name '*.tar.gz' -o -name '*.tgz' -o -name '*.tar.xz' -o -name '*.tar.bz2' -o -name '*.tbz2' \) -print | sort)
    if [[ -z "${database_package_dir}" && "${directory}" == "${script_dir}/packages/database" ]] && ((candidate_count > 0)); then
      break
    fi
  done
  for ((candidate_index=0; candidate_index<candidate_count; candidate_index++)); do
    candidate="${candidates[${candidate_index}]}"
    existing=false
    for ((unique_index=0; unique_index<unique_count; unique_index++)); do
      [[ "${unique[${unique_index}]}" == "${candidate}" ]] && existing=true && break
    done
    if [[ "${existing}" != "true" ]]; then
      unique[${unique_count}]="${candidate}"
      unique_count=$((unique_count + 1))
    fi
  done
  if ((unique_count == 1)); then
    database_package="${unique[0]}"
    log "自动选择数据库介质：${database_package}"
    return 0
  fi
  if ((unique_count == 0)); then
    die "未找到 ${database_engine}${database_version:+ ${database_version}} 数据库介质；请将 tar 包放入 ${database_package_dir:-${script_dir}/packages/database 或 /opt}，或显式传 -r"
  fi
  printf '发现多个匹配的数据库介质，拒绝自动选择：\n' >&2
  for ((unique_index=0; unique_index<unique_count; unique_index++)); do printf '  %s\n' "${unique[${unique_index}]}" >&2; done
  die "请通过 -r 明确指定数据库包，或使用 --database-package-dir 限定统一目录"
}

resolve_application_rpm() {
  [[ -n "${application_rpm}" ]] && return 0
  local package_dir="${script_dir}/packages" candidate
  local -a candidates=()
  [[ -d "${package_dir}" ]] || die "离线介质缺少 packages/ 目录；请显式通过 -g 指定 ClusterGuard RPM"
  while IFS= read -r candidate; do
    candidates+=("${candidate}")
  done < <(find "${package_dir}" -maxdepth 1 -type f -name 'clusterguard-ha-*.rpm' -print | sort)
  if ((${#candidates[@]} == 1)); then
    application_rpm="${candidates[0]}"
    log "自动选择 ClusterGuard RPM：${application_rpm}"
    return 0
  fi
  if ((${#candidates[@]} == 0)); then
    die "未在 ${package_dir} 找到 ClusterGuard RPM；请将 RPM 放入 packages/ 或显式通过 -g 指定"
  fi
  printf '发现多个 ClusterGuard RPM，拒绝自动选择：\n' >&2
  printf '  %s\n' "${candidates[@]}" >&2
  die "请通过 -g 明确指定本次安装使用的 ClusterGuard RPM"
}

validate_inputs() {
  [[ "${BASH_VERSINFO[0]}" -ge 3 ]] || die "安装器需要 Bash 3 或更高版本"
  [[ -n "${controllers_raw}" ]] || die "必须提供 -l/--controller-nodes"
  resolve_application_rpm
  [[ -f "${application_rpm}" && "${application_rpm}" == *.rpm ]] || die "ClusterGuard RPM 不存在或扩展名错误：${application_rpm}"
  parse_list "${controllers_raw}" controller_nodes
  if ((${#controller_nodes[@]} < 3 || ${#controller_nodes[@]} % 2 == 0)); then
    die "仲裁控制节点必须是至少 3 个的奇数，当前为 ${#controller_nodes[@]}"
  fi
  if [[ -n "${data_nodes_raw}" ]]; then
    parse_list "${data_nodes_raw}" data_nodes
  fi
  if [[ "${data_on_arbitrators}" == "true" ]]; then
    local arbitrator
    for arbitrator in "${controller_nodes[@]}"; do append_data_node "${arbitrator}"; done
  fi
  if [[ "${control_only}" == "true" ]]; then
    ((${#data_nodes[@]} == 0)) || die "--control-only 不能同时配置数据节点"
    [[ -z "${database_package}" ]] || die "--control-only 不接受数据库介质"
    [[ -z "${database_engine}" || "${database_engine}" == "none" ]] || die "--control-only 的 engine 必须为 none"
    database_engine="none"
  elif ((${#data_nodes[@]} == 0)); then
    [[ -z "${database_engine}" || "${database_engine}" == "none" ]] || die "${database_engine} 部署必须提供 -n，或使用 --data-on-arbitrators"
    database_engine="none"
    control_only=true
  else
    [[ -n "${database_engine}" ]] || database_engine="mysql"
    case "${database_engine}" in mysql|postgresql) ;; *) die "数据节点 engine 只能是 mysql 或 postgresql" ;; esac
    resolve_database_package
    [[ -f "${database_package}" ]] || die "数据库安装包不存在：${database_package}"
    case "${database_package}" in *.tar|*.tar.gz|*.tgz|*.tar.xz|*.tar.bz2|*.tbz2) ;; *) die "数据库介质必须是 tar/tar.gz/tgz/tar.xz/tar.bz2/tbz2" ;; esac
  fi
  case "${database_engine}" in
    mysql)
      [[ -n "${database_port}" ]] || database_port=3306
      ;;
    postgresql)
      [[ -n "${database_port}" ]] || database_port=5432
      [[ -n "${database_version}" ]] || database_version=16
      [[ "${database_version}" =~ ^[0-9]+([.][0-9]+)*$ ]] || die "PostgreSQL 版本格式无效"
      if [[ -z "${postgresql_allowed_cidr}" ]]; then
        local inferred_subnet="" data_index data_address subnet
        for ((data_index=0; data_index<${#data_nodes[@]}; data_index++)); do
          data_address="${data_nodes[${data_index}]}"
          valid_ipv4 "${data_address}" || die "PostgreSQL 使用 DNS 或跨网段部署时必须提供 --postgresql-allowed-cidr"
          subnet="${data_address%.*}"
          [[ -z "${inferred_subnet}" || "${inferred_subnet}" == "${subnet}" ]] || die "PostgreSQL 数据节点跨网段，必须提供 --postgresql-allowed-cidr"
          inferred_subnet="${subnet}"
        done
        postgresql_allowed_cidr="${inferred_subnet}.0/24"
      fi
      ;;
    none)
      [[ -n "${database_port}" ]] || database_port=0
      activate_vip_reconcile=false
      ;;
    *) die "--engine 只能是 mysql、postgresql 或 none" ;;
  esac
  [[ -z "${cluster_name}" ]] && cluster_name="$([[ "${database_engine}" == "none" ]] && echo clusterguard-control-plane || echo "${database_engine}-ha-${database_port}")"
  [[ "${cluster_name}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{2,62}$ ]] || die "集群名称格式无效"
  [[ "${ssh_user}" =~ ^[A-Za-z_][A-Za-z0-9_-]*$ ]] || die "SSH 用户名格式无效"
  [[ "${vip_interface}" =~ ^[A-Za-z0-9_.:-]+$ ]] || die "VIP 网卡格式无效"
  valid_port "${ssh_port}" || die "SSH 端口无效"
  [[ "${database_engine}" == "none" ]] || valid_port "${database_port}" || die "数据库端口无效"
  valid_port "${api_port}" || die "API 端口无效"
  valid_port "${raft_port}" || die "Raft 端口无效"
  [[ "${vip_prefix}" =~ ^[0-9]+$ && "${vip_prefix}" -ge 1 && "${vip_prefix}" -le 32 ]] || die "VIP prefix 必须为 1 到 32"
  [[ -z "${vip}" ]] || valid_ipv4 "${vip}" || die "VIP 必须是有效 IPv4 地址"
  if [[ ( "${database_engine}" == "mysql" || "${database_engine}" == "postgresql" ) && -n "${vip}" ]]; then
	if [[ "${manual_failover_only}" == "true" ]]; then
	  [[ -z "${fencer_file}" && -z "${fencer_assets_dir}" ]] ||
		die "--manual-failover-only 不能同时配置 --fencer 或 --fencer-assets"
	else
	  ((${#data_nodes[@]} >= 2)) || die "${database_engine} 自动故障切换至少需要 2 个数据节点"
	  if [[ "${database_engine}" == "mysql" ]]; then
		mysql_automatic_failover=true
	  else
		postgresql_automatic_failover=true
	  fi
	  agent_quorum_fencing=true
	fi
  elif [[ -n "${fencer_file}" || -n "${fencer_assets_dir}" ]]; then
	die "当前 --fencer 只用于带 VIP 的 MySQL/PostgreSQL HA 部署"
  fi
  if [[ -n "${fencer_file}" ]]; then
    [[ -f "${fencer_file}" && ! -L "${fencer_file}" && -x "${fencer_file}" ]] ||
      die "外部隔离程序必须是本地可执行的普通文件：${fencer_file}"
  elif [[ -n "${fencer_assets_dir}" ]]; then
    die "--fencer-assets 必须与 --fencer FILE 同时使用"
  fi
  if [[ -n "${fencer_assets_dir}" ]]; then
    [[ -d "${fencer_assets_dir}" && ! -L "${fencer_assets_dir}" ]] || die "外部隔离配置目录无效：${fencer_assets_dir}"
    [[ -z "$(find "${fencer_assets_dir}" -mindepth 1 -type l -print -quit)" ]] || die "外部隔离配置目录不能包含符号链接"
    [[ -z "$(find "${fencer_assets_dir}" -mindepth 2 -print -quit)" ]] || die "外部隔离配置目录只允许普通文件，不能包含子目录"
    while IFS= read -r fencer_asset; do
      [[ "$(basename "${fencer_asset}")" =~ ^[A-Za-z0-9._-]+$ ]] || die "外部隔离配置文件名无效：${fencer_asset}"
    done < <(find "${fencer_assets_dir}" -mindepth 1 -maxdepth 1 -type f -print | sort)
  fi
  [[ -z "${postgresql_allowed_cidr}" ]] || valid_ipv4_cidr "${postgresql_allowed_cidr}" || die "PostgreSQL 允许网段必须是有效 IPv4 CIDR"
  [[ "${controller_min_free_gb}" =~ ^[1-9][0-9]*$ ]] || die "控制面最小空间必须是正整数 GiB"
  [[ "${database_min_free_gb}" =~ ^[1-9][0-9]*$ ]] || die "数据库最小空间必须是正整数 GiB"
  valid_absolute_root "${controller_data_root}" || die "-ld 必须是安全的绝对目录"
  valid_absolute_root "${database_data_root}" || die "-nd 必须是安全的绝对目录"
  [[ "${remote_stage}" == /* && "${remote_stage}" != *$'\n'* ]] || die "远端暂存目录无效"
  [[ -z "${ssh_key}" || -f "${ssh_key}" ]] || die "SSH 私钥不存在：${ssh_key}"
  [[ -z "${ssh_credentials_file}" || -f "${ssh_credentials_file}" ]] || die "节点级 SSH 凭据文件不存在：${ssh_credentials_file}"
  [[ -z "${known_hosts_input}" || -f "${known_hosts_input}" ]] || die "known_hosts 不存在：${known_hosts_input}"
  if [[ -n "${patch_trust_key}" ]]; then
    [[ -f "${patch_trust_key}" && ! -L "${patch_trust_key}" ]] || die "补丁签名公钥不存在或不是普通文件：${patch_trust_key}"
    openssl pkey -pubin -in "${patch_trust_key}" -noout >/dev/null 2>&1 || die "补丁签名公钥格式无效：${patch_trust_key}"
  fi
  [[ -z "${dependency_dir}" || -d "${dependency_dir}" ]] || die "依赖 RPM 目录不存在：${dependency_dir}"
  [[ -z "${postgresql_dependency_dir}" || -d "${postgresql_dependency_dir}" ]] || die "PostgreSQL 编译依赖目录不存在：${postgresql_dependency_dir}"
  [[ -z "${database_package_dir}" || -d "${database_package_dir}" ]] || die "数据库统一介质目录不存在：${database_package_dir}"
  if [[ -n "${mysql_root_password}" && ! "${mysql_root_password}" =~ ^[A-Za-z0-9@%+=:,._-]{8,128}$ ]]; then
    die "MySQL root 密码包含环境文件不支持的字符；请使用字母、数字和 @%+=:,._-"
  fi
  if [[ -n "${mysql_root_remote_host}" && ! "${mysql_root_remote_host}" =~ ^[A-Za-z0-9._:%/-]{1,255}$ ]]; then
    die "MySQL 远程 root Host 格式无效"
  fi
  if [[ -n "${mysql_root_remote_host}" && "${database_engine}" != "mysql" ]]; then
    die "--mysql-root-remote-host 仅适用于 MySQL"
  fi
  if [[ -n "${postgresql_admin_password}" && ! "${postgresql_admin_password}" =~ ^[A-Za-z0-9@%+=:,._-]{8,128}$ ]]; then
    die "PostgreSQL 管理密码包含环境文件不支持的字符；请使用字母、数字和 @%+=:,._-"
  fi
  if [[ "${database_engine}" != "none" && "${ssh_port}" -ne 22 ]]; then
    die "当前 Agent 清单使用标准 SSH 22 端口；数据库节点部署暂不允许非 22 端口"
  fi
  verify_sidecar "${application_rpm}"
  [[ "${database_engine}" == "none" ]] || verify_sidecar "${database_package}"
  validate_database_media
  if [[ "${database_package_kind}" == "postgresql-source" ]]; then
    [[ "$(basename "${database_package}")" =~ ^[A-Za-z0-9._+-]+$ ]] || die "PostgreSQL 源码包文件名包含不支持的字符"
    [[ -n "${postgresql_build_node}" ]] || postgresql_build_node="${data_nodes[0]}"
    data_node_contains "${postgresql_build_node}" || die "PostgreSQL 源码构建节点必须属于数据节点清单：${postgresql_build_node}"
    if [[ -n "${postgresql_build_jobs}" ]]; then
      [[ "${postgresql_build_jobs}" =~ ^[1-9][0-9]*$ && "${postgresql_build_jobs}" -le 128 ]] || die "--postgresql-build-jobs 必须为 1 到 128"
    fi
    if [[ -z "${postgresql_dependency_dir}" && -n "${dependency_dir}" && -f "${dependency_dir}/COLLECTION-INFO" ]] &&
      grep -q '^postgresql_source_build=true$' "${dependency_dir}/COLLECTION-INFO"; then
      postgresql_dependency_dir="${dependency_dir}"
      warn "检测到 --dependencies 包含 PostgreSQL 源码工具链；兼容使用该目录。后续建议改用 --postgresql-dependencies"
    fi
  elif [[ -n "${postgresql_build_node}" || -n "${postgresql_build_jobs}" ]]; then
    die "--postgresql-build-node/--postgresql-build-jobs 仅适用于 PostgreSQL 官方源码包"
  fi
  all_nodes=("${controller_nodes[@]}")
  local host
  local index
  for ((index=0; index<${#data_nodes[@]}; index++)); do
    host="${data_nodes[${index}]}"
    all_node_contains "${host}" || all_nodes+=("${host}")
  done
  node_roles=()
  node_ids=()
  node_names=()
  node_hostnames=()
  instance_ids=()
  for host in "${all_nodes[@]}"; do
    if controller_node_contains "${host}" && data_node_contains "${host}"; then
      node_roles+=("mixed")
    elif controller_node_contains "${host}"; then
      node_roles+=("controller")
    else
      node_roles+=("data")
    fi
    node_ids+=("")
    node_names+=("")
    node_hostnames+=("")
    instance_ids+=("")
  done
  initialize_ssh_passwords
  validate_ssh_credentials_file
}

print_plan() {
	local automatic_failover="false"
	if [[ "${database_engine}" == "mysql" ]]; then
		automatic_failover="${mysql_automatic_failover}"
	elif [[ "${database_engine}" == "postgresql" ]]; then
		automatic_failover="${postgresql_automatic_failover}"
	fi
  printf '\nClusterGuard HA 离线部署计划\n'
  printf '  集群名称       : %s\n' "${cluster_name}"
  printf '  仲裁控制节点   : %s\n' "$(IFS=,; echo "${controller_nodes[*]}")"
  printf '  数据节点       : %s\n' "$([[ ${#data_nodes[@]} -gt 0 ]] && (IFS=,; echo "${data_nodes[*]}") || echo 未配置)"
  printf '  ClusterGuard   : %s\n' "${application_rpm}"
  printf '  数据库引擎     : %s\n' "${database_engine}"
  printf '  数据库介质     : %s\n' "$([[ "${database_engine}" == "none" ]] && echo 跳过 || echo "${database_package}")"
  if [[ "${database_package_kind}" == "postgresql-source" ]]; then
    printf '  数据库介质类型 : PostgreSQL 官方源码\n'
    printf '  源码构建节点   : %s（仅编译一次）\n' "${postgresql_build_node}"
    printf '  构建临时空间   : 至少 %s GiB\n' "${postgresql_build_min_free_gb}"
    printf '  统一制品分发   : 启用（SHA256 校验）\n'
    if [[ -n "${postgresql_dependency_dir}" ]]; then
      printf '  PG 编译依赖    : 独立离线依赖包 %s\n' "${postgresql_dependency_dir}"
    else
      printf '  PG 编译依赖    : 默认联网安装到隔离构建根；失败时提示上传独立依赖包\n'
    fi
  elif [[ "${database_engine}" != "none" ]]; then
    printf '  数据库介质类型 : 原厂二进制\n'
  fi
  printf '  控制面数据目录 : %s\n' "${controller_data_root}"
  printf '  首次管理员口令 : 随机生成；交互安装完成时显示，非交互时读取 %s/bootstrap-admin-password\n' "${controller_data_root}"
  if [[ "${database_engine}" != "none" ]]; then
    printf '  数据库数据目录 : %s/%s/%s\n' "${database_data_root}" "${database_engine}" "${database_port}"
  fi
  printf '  控制台         : https://%s:%s/\n' "${controller_nodes[0]}" "${api_port}"
  printf '  VIP            : %s/%s (%s)\n' "${vip:-未配置}" "${vip_prefix}" "${vip_interface}"
  printf '  自动故障切换   : %s\n' "$([[ "${automatic_failover}" == "true" ]] && echo '启用（数据库探测 + Raft 多数派 + Agent 本地隔离）' || { [[ "${database_engine}" == "mysql" || "${database_engine}" == "postgresql" ]] && echo '关闭（仅人工受控切换）' || echo 不适用; })"
  if [[ "${database_engine}" == "mysql" ]]; then
    if [[ -n "${mysql_root_remote_host}" ]]; then
      printf '  MySQL 远程 root : root@%s（显式启用）\n' "${mysql_root_remote_host}"
    else
      printf '  MySQL 远程 root : 关闭（默认）\n'
    fi
  fi
  [[ -z "${fencer_file}" ]] || printf '  外部隔离增强   : %s\n' "${fencer_file}"
  printf '  状态文件       : %s\n' "${state_file}"
  if [[ -n "${patch_trust_key}" ]]; then
    printf '  图形化补丁升级 : 启用（签名校验 + 计划 + 滚动执行 + 回退）\n'
  else
    printf '  图形化补丁升级 : 未启用（安装时追加 --patch-trust-key）\n'
  fi
  [[ -z "${ssh_passwords_raw}" ]] || printf '  SSH 凭据       : 节点顺序密码列表（已校验，不显示密码）\n'
  [[ -z "${ssh_credentials_file}" ]] || printf '  SSH 凭据       : 节点级凭据文件（已校验，不显示密码）\n'
  printf '  执行模式       : %s\n\n' "$([[ "${execute}" == "true" ]] && echo 真实安装 || echo 只读计划)"
  local host
  for host in "${all_nodes[@]}"; do printf '  %-24s %s\n' "${host}" "$(node_role_for "${host}")"; done
  printf '\n'
}

select_ssh_auth_mode() {
  ssh_auth_mode="key"
  [[ -n "${ssh_password}" || -n "${ssh_passwords_raw}" || -n "${ssh_credentials_file}" ]] || return 0
  if command -v sshpass >/dev/null 2>&1; then ssh_auth_mode="sshpass"; return 0; fi
  if command -v setsid >/dev/null 2>&1; then ssh_auth_mode="askpass"; return 0; fi
  if command -v expect >/dev/null 2>&1; then ssh_auth_mode="expect"; return 0; fi
  die "密码 SSH 需要 sshpass、setsid 或 expect；也可改用 --ssh-key"
}

prompt_for_ssh_password() {
  [[ "${execute}" == "true" ]] || return 0
  [[ -n "${ssh_key}" || -n "${ssh_password}" || -n "${ssh_passwords_raw}" || -n "${ssh_credentials_file}" ]] && return 0
  [[ -t 0 ]] || die "未提供 SSH 私钥或密码；非交互执行请设置 CG_SSH_PASSWORD 或使用 --ssh-key"
  read -r -s -p "SSH 用户 ${ssh_user} 的密码（仅输入一次）: " ssh_password
  printf '\n'
  [[ -n "${ssh_password}" ]] || die "SSH 密码不能为空"
  log "已读取 SSH 密码，将在本次安装中复用，不会显示或写入部署状态"
}

initialize_ssh_passwords() {
  [[ -z "${ssh_passwords_raw}" ]] && return 0
  [[ -z "${ssh_password}" && -z "${ssh_credentials_file}" && -z "${ssh_key}" ]] || die "-p/--ssh-passwords 不能与 -P、--ssh-credentials-file 或 --ssh-key 同时使用"
  local -a parsed=()
  local item index
  IFS=',' read -r -a parsed <<<"${ssh_passwords_raw}"
  ((${#parsed[@]} == ${#all_nodes[@]})) || die "-p/--ssh-passwords 需要 ${#all_nodes[@]} 个密码，当前提供 ${#parsed[@]} 个；顺序为 $(IFS=,; echo "${all_nodes[*]}")"
  ssh_node_passwords=()
  for ((index=0; index<${#parsed[@]}; index++)); do
    item="${parsed[${index}]}"
    [[ -n "${item}" ]] || die "-p/--ssh-passwords 的第 $((index + 1)) 个密码为空"
    [[ "${item}" != *$'\n'* && "${item}" != *$'\r'* ]] || die "SSH 密码不能包含换行"
    ssh_node_passwords+=("${item}")
  done
}

credential_password_for_host() {
  local host="$1" password index
  if [[ -n "${ssh_passwords_raw}" ]]; then
    for ((index=0; index<${#all_nodes[@]}; index++)); do
      if [[ "${all_nodes[${index}]}" == "${host}" ]]; then
        printf '%s' "${ssh_node_passwords[${index}]}"
        return 0
      fi
    done
    die "内部错误：找不到节点 ${host} 的 SSH 密码顺序"
  fi
  if [[ -z "${ssh_credentials_file}" ]]; then
    printf '%s' "${ssh_password}"
    return 0
  fi
  password="$(awk -v wanted="${host}" '
    /^[[:space:]]*($|#)/ { next }
    {
      pos=index($0, "=")
      if (pos < 2) next
      key=substr($0, 1, pos - 1)
      value=substr($0, pos + 1)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", key)
      if (key == wanted) { print value; exit }
    }
  ' "${ssh_credentials_file}")"
  [[ -n "${password}" ]] || die "节点级 SSH 凭据文件缺少 ${host} 的密码"
  printf '%s' "${password}"
}

validate_ssh_credentials_file() {
  [[ -z "${ssh_credentials_file}" ]] && return 0
  [[ -z "${ssh_key}" && -z "${ssh_passwords_raw}" ]] || die "--ssh-key、-p/--ssh-passwords 与 --ssh-credentials-file 不能同时使用"
  local permissions host duplicate_hosts="" password
  permissions="$(stat -c '%a' "${ssh_credentials_file}" 2>/dev/null || stat -f '%Lp' "${ssh_credentials_file}")"
  [[ "${permissions}" == "600" ]] || die "节点级 SSH 凭据文件权限必须为 0600：${ssh_credentials_file}"
  while IFS= read -r host; do
    password="$(credential_password_for_host "${host}")"
    [[ -n "${password}" ]] || die "节点级 SSH 凭据文件缺少 ${host} 的密码"
  done < <(printf '%s\n' "${all_nodes[@]}")
  # A duplicate key can make an operator believe a changed password took effect.
  duplicate_hosts="$(awk '
    /^[[:space:]]*($|#)/ { next }
    { pos=index($0, "="); if (pos < 2) next; key=substr($0,1,pos-1); gsub(/^[[:space:]]+|[[:space:]]+$/, "", key); if (++seen[key] == 2) print key }
  ' "${ssh_credentials_file}")"
  [[ -z "${duplicate_hosts}" ]] || die "节点级 SSH 凭据文件存在重复节点：${duplicate_hosts}"
}

prepare_known_hosts() {
  if [[ -n "${known_hosts_input}" ]]; then
    known_hosts_file="${known_hosts_input}"
    return
  fi
  known_hosts_file="${work_dir}/ssh/installer_known_hosts"
  mkdir -p "$(dirname "${known_hosts_file}")"
  chmod 0700 "$(dirname "${known_hosts_file}")"
  if [[ -s "${known_hosts_file}" ]]; then return; fi
  [[ "${accept_host_keys}" == "true" ]] || die "首次执行必须提供 --known-hosts 或显式使用 --accept-host-keys"
  : >"${known_hosts_file}"
  chmod 0600 "${known_hosts_file}"
  local host
  for host in "${all_nodes[@]}"; do
    ssh-keyscan -T 8 -p "${ssh_port}" -H "${host}" >>"${known_hosts_file}" 2>/dev/null || die "无法采集 ${host} 的 SSH 主机密钥"
  done
  sort -u "${known_hosts_file}" -o "${known_hosts_file}"
  new_known_hosts=true
  log "已采集 SSH 主机密钥；指纹如下："
  ssh-keygen -lf "${known_hosts_file}" || true
}

ssh_common_options() {
  printf '%s\0' -o "UserKnownHostsFile=${known_hosts_file}" -o StrictHostKeyChecking=yes -o ConnectTimeout=10 -o ServerAliveInterval=15 -o ServerAliveCountMax=3 -o LogLevel=ERROR
  [[ -z "${ssh_key}" ]] || printf '%s\0' -i "${ssh_key}"
}

run_askpass() {
  local helper
  helper="$(mktemp /tmp/clusterguard-askpass.XXXXXX)"
  printf '#!/bin/sh\nprintf "%%s\\n" "$CG_INSTALLER_PASSWORD"\n' >"${helper}"
  chmod 0700 "${helper}"
  CG_INSTALLER_PASSWORD="${ssh_password}" DISPLAY=:0 SSH_ASKPASS="${helper}" SSH_ASKPASS_REQUIRE=force setsid -w "$@" </dev/null
  local status=$?
  rm -f "${helper}"
  return "${status}"
}

expect_ssh() {
  local host="$1" command="$2"
  CG_EXPECT_HOST="${host}" CG_EXPECT_USER="${ssh_user}" CG_EXPECT_PORT="${ssh_port}" CG_EXPECT_PASSWORD="${ssh_password}" CG_EXPECT_KNOWN="${known_hosts_file}" CG_EXPECT_COMMAND="${command}" expect <<'EOF'
set timeout 1800
set command [list ssh -p $env(CG_EXPECT_PORT) -o UserKnownHostsFile=$env(CG_EXPECT_KNOWN) -o StrictHostKeyChecking=yes -o ConnectTimeout=10 "$env(CG_EXPECT_USER)@$env(CG_EXPECT_HOST)" $env(CG_EXPECT_COMMAND)]
log_user 0
eval spawn $command

proc finish_child {} {
  global expect_out env
  set output $expect_out(buffer)
  regsub {^\r?\n} $output {} output
  if {$env(CG_EXPECT_PASSWORD) ne ""} {
    set output [string map [list $env(CG_EXPECT_PASSWORD) "<redacted>"] $output]
  }
  puts -nonewline $output
  set status 0
  if {![catch wait result] && [llength $result] >= 4} {
    set status [lindex $result 3]
  }
  exit $status
}

# Password authentication is a one-time transport phase. Once authenticated,
# application output must never be interpreted as another SSH password prompt.
expect {
  -re {(?i)(^|[\r\n])[^\r\n]*password:[ ]*$} {
    send -- "$env(CG_EXPECT_PASSWORD)\r"
    expect {
      -re {(?i)permission denied, please try again[.]} {
        set output [string map [list $env(CG_EXPECT_PASSWORD) "<redacted>"] $expect_out(buffer)]
        puts -nonewline $output
        exit 126
      }
      eof { finish_child }
      timeout { exit 124 }
    }
  }
  -re {(?i)permission denied} {
    set output [string map [list $env(CG_EXPECT_PASSWORD) "<redacted>"] $expect_out(buffer)]
    puts -nonewline $output
    exit 126
  }
  eof { finish_child }
  timeout { exit 124 }
}
EOF
}

expect_scp() {
  local source="$1" host="$2" destination="$3"
  CG_EXPECT_SOURCE="${source}" CG_EXPECT_HOST="${host}" CG_EXPECT_USER="${ssh_user}" CG_EXPECT_PORT="${ssh_port}" CG_EXPECT_PASSWORD="${ssh_password}" CG_EXPECT_KNOWN="${known_hosts_file}" CG_EXPECT_DEST="${destination}" expect <<'EOF'
set timeout 1800
set command [list scp -q -P $env(CG_EXPECT_PORT) -o UserKnownHostsFile=$env(CG_EXPECT_KNOWN) -o StrictHostKeyChecking=yes -- $env(CG_EXPECT_SOURCE) "$env(CG_EXPECT_USER)@$env(CG_EXPECT_HOST):$env(CG_EXPECT_DEST)"]
log_user 0
eval spawn $command

proc finish_child {} {
  global expect_out env
  set output $expect_out(buffer)
  regsub {^\r?\n} $output {} output
  if {$env(CG_EXPECT_PASSWORD) ne ""} {
    set output [string map [list $env(CG_EXPECT_PASSWORD) "<redacted>"] $output]
  }
  puts -nonewline $output
  set status 0
  if {![catch wait result] && [llength $result] >= 4} {
    set status [lindex $result 3]
  }
  exit $status
}

expect {
  -re {(?i)(^|[\r\n])[^\r\n]*password:[ ]*$} {
    send -- "$env(CG_EXPECT_PASSWORD)\r"
    expect {
      -re {(?i)permission denied, please try again[.]} {
        set output [string map [list $env(CG_EXPECT_PASSWORD) "<redacted>"] $expect_out(buffer)]
        puts -nonewline $output
        exit 126
      }
      eof { finish_child }
      timeout { exit 124 }
    }
  }
  -re {(?i)permission denied} {
    set output [string map [list $env(CG_EXPECT_PASSWORD) "<redacted>"] $expect_out(buffer)]
    puts -nonewline $output
    exit 126
  }
  eof { finish_child }
  timeout { exit 124 }
}
EOF
}

expect_scp_from() {
  local host="$1" source="$2" destination="$3"
  CG_EXPECT_SOURCE="${source}" CG_EXPECT_HOST="${host}" CG_EXPECT_USER="${ssh_user}" CG_EXPECT_PORT="${ssh_port}" CG_EXPECT_PASSWORD="${ssh_password}" CG_EXPECT_KNOWN="${known_hosts_file}" CG_EXPECT_DEST="${destination}" expect <<'EOF'
set timeout 1800
set command [list scp -q -P $env(CG_EXPECT_PORT) -o UserKnownHostsFile=$env(CG_EXPECT_KNOWN) -o StrictHostKeyChecking=yes -- "$env(CG_EXPECT_USER)@$env(CG_EXPECT_HOST):$env(CG_EXPECT_SOURCE)" $env(CG_EXPECT_DEST)]
log_user 0
eval spawn $command

proc finish_child {} {
  global expect_out env
  set output $expect_out(buffer)
  regsub {^\r?\n} $output {} output
  if {$env(CG_EXPECT_PASSWORD) ne ""} {
    set output [string map [list $env(CG_EXPECT_PASSWORD) "<redacted>"] $output]
  }
  puts -nonewline $output
  set status 0
  if {![catch wait result] && [llength $result] >= 4} {
    set status [lindex $result 3]
  }
  exit $status
}

expect {
  -re {(?i)(^|[\r\n])[^\r\n]*password:[ ]*$} {
    send -- "$env(CG_EXPECT_PASSWORD)\r"
    expect {
      -re {(?i)permission denied, please try again[.]} { exit 126 }
      eof { finish_child }
      timeout { exit 124 }
    }
  }
  -re {(?i)permission denied} { exit 126 }
  eof { finish_child }
  timeout { exit 124 }
}
EOF
}

remote_exec() {
  local host="$1" command="$2" current_ssh_password
  current_ssh_password="$(credential_password_for_host "${host}")"
  local -a options=()
  while IFS= read -r -d '' item; do options+=("${item}"); done < <(ssh_common_options)
  case "${ssh_auth_mode}" in
    key) ssh -p "${ssh_port}" "${options[@]}" "${ssh_user}@${host}" "${command}" ;;
    sshpass) sshpass -p "${current_ssh_password}" ssh -p "${ssh_port}" "${options[@]}" -o BatchMode=no "${ssh_user}@${host}" "${command}" ;;
    askpass) ssh_password="${current_ssh_password}" run_askpass ssh -p "${ssh_port}" "${options[@]}" -o BatchMode=no "${ssh_user}@${host}" "${command}" ;;
    expect) ssh_password="${current_ssh_password}" expect_ssh "${host}" "${command}" | tr -d '\r' ;;
    *) die "不支持的 SSH 认证模式" ;;
  esac
}

redact_remote_output() {
  local output="$1" line secret index
  if [[ -f "${secrets_file}" ]]; then
    while IFS= read -r line; do
      [[ "${line}" == *=* ]] || continue
      secret="${line#*=}"
      [[ -n "${secret}" ]] || continue
      output="${output//"${secret}"/<redacted>}"
    done <"${secrets_file}"
  fi
  if [[ -n "${ssh_password}" ]]; then
    output="${output//"${ssh_password}"/<redacted>}"
  fi
  if [[ -n "${ssh_credentials_file}" ]]; then
    while IFS= read -r line; do
      [[ "${line}" == *=* ]] || continue
      secret="${line#*=}"
      [[ -n "${secret}" ]] || continue
      output="${output//"${secret}"/<redacted>}"
    done <"${ssh_credentials_file}"
  fi
  for ((index=0; index<${#ssh_node_passwords[@]}; index++)); do
    secret="${ssh_node_passwords[${index}]}"
    [[ -n "${secret}" ]] || continue
    output="${output//"${secret}"/<redacted>}"
  done
  printf '%s' "${output}"
}

remote_exec_checked() {
  local host="$1" label="$2" command="$3" output status
  set +e
  output="$(remote_exec "${host}" "${command}" 2>&1)"
  status=$?
  set -e
  if [[ "${status}" -ne 0 ]]; then
    output="$(redact_remote_output "${output}")"
    printf '远端步骤失败：%s [%s] (exit=%s)\n' "${label}" "${host}" "${status}" >&2
    if [[ -n "${output}" ]]; then
      printf '%s\n' "${output}" | tail -n 120 >&2
    fi
    return "${status}"
  fi
}

remote_copy() {
  local source="$1" host="$2" destination="$3" current_ssh_password
  current_ssh_password="$(credential_password_for_host "${host}")"
  local -a options=()
  while IFS= read -r -d '' item; do options+=("${item}"); done < <(ssh_common_options)
  case "${ssh_auth_mode}" in
    key) scp -q -P "${ssh_port}" "${options[@]}" -- "${source}" "${ssh_user}@${host}:${destination}" ;;
    sshpass) sshpass -p "${current_ssh_password}" scp -q -P "${ssh_port}" "${options[@]}" -o BatchMode=no -- "${source}" "${ssh_user}@${host}:${destination}" ;;
    askpass) ssh_password="${current_ssh_password}" run_askpass scp -q -P "${ssh_port}" "${options[@]}" -o BatchMode=no -- "${source}" "${ssh_user}@${host}:${destination}" ;;
    expect) ssh_password="${current_ssh_password}" expect_scp "${source}" "${host}" "${destination}" ;;
  esac
}

remote_fetch() {
  local host="$1" source="$2" destination="$3" current_ssh_password
  current_ssh_password="$(credential_password_for_host "${host}")"
  local -a options=()
  while IFS= read -r -d '' item; do options+=("${item}"); done < <(ssh_common_options)
  case "${ssh_auth_mode}" in
    key) scp -q -P "${ssh_port}" "${options[@]}" -- "${ssh_user}@${host}:${source}" "${destination}" ;;
    sshpass) sshpass -p "${current_ssh_password}" scp -q -P "${ssh_port}" "${options[@]}" -o BatchMode=no -- "${ssh_user}@${host}:${source}" "${destination}" ;;
    askpass) ssh_password="${current_ssh_password}" run_askpass scp -q -P "${ssh_port}" "${options[@]}" -o BatchMode=no -- "${ssh_user}@${host}:${source}" "${destination}" ;;
    expect) ssh_password="${current_ssh_password}" expect_scp_from "${host}" "${source}" "${destination}" ;;
    *) die "不支持的 SSH 认证模式" ;;
  esac
}

uuid() {
  if command -v uuidgen >/dev/null 2>&1; then uuidgen | tr '[:upper:]' '[:lower:]'; return; fi
  local hex
  hex="$(openssl rand -hex 16)"
  printf '%s-%s-4%s-%s%s-%s\n' "${hex:0:8}" "${hex:8:4}" "${hex:13:3}" "$(printf '%x' $(( (0x${hex:16:1} & 3) | 8 )))" "${hex:17:3}" "${hex:20:12}"
}

read_env_value() {
  local name="$1" file="$2"
  awk -v key="${name}" 'index($0, key "=") == 1 {sub("^[^=]*=", ""); print; exit}' "${file}"
}

random_secret() { openssl rand -hex 24; }
mysql_replication_secret() { openssl rand -hex 16; }

initialize_state_and_secrets() {
  mkdir -p "$(dirname "${state_file}")" "$(dirname "${secrets_file}")" "${work_dir}"
  chmod 0700 "${work_dir}"
  if [[ ! -s "${state_file}" ]]; then
    "${jq_binary}" -n --arg cluster_id "$(uuid)" --arg display_name "${cluster_name}" --arg engine "${database_engine}" --arg mysql_root_remote_host "${mysql_root_remote_host}" --argjson database_port "${database_port}" \
      '{schema_version:2,cluster:{resource_id:$cluster_id,display_name:$display_name,engine:$engine,database_port:$database_port,mysql_root_remote_host:$mysql_root_remote_host,phase:"pending"},nodes:[]}' >"${state_file}"
    chmod 0600 "${state_file}"
  fi
  "${jq_binary}" -e --arg name "${cluster_name}" --arg engine "${database_engine}" --arg mysql_root_remote_host "${mysql_root_remote_host}" --argjson port "${database_port}" \
    '.schema_version == 2 and .cluster.display_name == $name and .cluster.engine == $engine and .cluster.database_port == $port and (.cluster.mysql_root_remote_host // "") == $mysql_root_remote_host and (.nodes|type == "array")' "${state_file}" >/dev/null ||
    die "状态文件与本次集群名称、引擎、端口或 MySQL 远程 root 策略不一致：${state_file}"
  if [[ ! -s "${secrets_file}" ]]; then
    [[ -n "${mysql_root_password}" ]] || mysql_root_password="$(random_secret)"
    [[ -n "${postgresql_admin_password}" ]] || postgresql_admin_password="$(random_secret)"
    local mysql_managed_replication_password postgresql_replication_password
    mysql_managed_replication_password="$(mysql_replication_secret)"
    postgresql_replication_password="$(random_secret)"
    cat >"${secrets_file}" <<EOF
CG_CONTROL_TOKEN=$(random_secret)
CG_MONITORING_TOKEN=$(random_secret)
CG_APPROVAL_TOKEN=$(random_secret)
CG_MYSQL_DISCOVERY_PASSWORD=$(random_secret)
CG_MYSQL_OPERATION_PASSWORD=$(random_secret)
CG_MYSQL_REPLICATION_PASSWORD=${mysql_managed_replication_password}
CG_AGENT_SHARED_SECRET=$(random_secret)
CG_NODE_MYSQL_ROOT_PASSWORD=${mysql_root_password}
CG_NODE_MYSQL_REPLICATION_PASSWORD=${mysql_managed_replication_password}
CG_POSTGRESQL_ADMIN_PASSWORD=${postgresql_admin_password}
CG_POSTGRESQL_DISCOVERY_PASSWORD=${postgresql_admin_password}
CG_POSTGRESQL_OPERATION_PASSWORD=${postgresql_admin_password}
CG_POSTGRESQL_REPLICATION_PASSWORD=${postgresql_replication_password}
EOF
    chmod 0600 "${secrets_file}"
  else
    chmod 0600 "${secrets_file}"
    mysql_root_password="$(read_env_value CG_NODE_MYSQL_ROOT_PASSWORD "${secrets_file}")"
    postgresql_admin_password="$(read_env_value CG_POSTGRESQL_ADMIN_PASSWORD "${secrets_file}")"
  fi
  for required in CG_CONTROL_TOKEN CG_MONITORING_TOKEN CG_APPROVAL_TOKEN CG_MYSQL_DISCOVERY_PASSWORD CG_MYSQL_OPERATION_PASSWORD CG_MYSQL_REPLICATION_PASSWORD CG_AGENT_SHARED_SECRET CG_NODE_MYSQL_ROOT_PASSWORD CG_NODE_MYSQL_REPLICATION_PASSWORD CG_POSTGRESQL_ADMIN_PASSWORD CG_POSTGRESQL_DISCOVERY_PASSWORD CG_POSTGRESQL_OPERATION_PASSWORD CG_POSTGRESQL_REPLICATION_PASSWORD; do
    [[ -n "$(read_env_value "${required}" "${secrets_file}")" ]] || die "秘密文件缺少 ${required}"
  done
}

reconcile_mysql_replication_secret_policy() {
  [[ "${database_engine}" == "mysql" ]] || return 0
  local platform_secret lifecycle_secret replacement temporary
  platform_secret="$(read_env_value CG_MYSQL_REPLICATION_PASSWORD "${secrets_file}")"
  lifecycle_secret="$(read_env_value CG_NODE_MYSQL_REPLICATION_PASSWORD "${secrets_file}")"
  if (( ${#platform_secret} <= 32 )) && [[ "${platform_secret}" == "${lifecycle_secret}" ]]; then
    return 0
  fi
  [[ "$(state_phase)" == "pending" ]] ||
    die "MySQL 复制凭据不兼容：复制通道密码必须不超过 32 字符且初装/节点恢复必须一致；现有集群拒绝自动轮换，请使用受控凭据轮换流程"
  replacement="$(mysql_replication_secret)"
  temporary="$(mktemp "${secrets_file}.XXXXXX")"
  awk -F= -v replacement="${replacement}" '
    $1 == "CG_MYSQL_REPLICATION_PASSWORD" || $1 == "CG_NODE_MYSQL_REPLICATION_PASSWORD" {
      print $1 "=" replacement
      next
    }
    { print }
  ' "${secrets_file}" >"${temporary}"
  chmod 0600 "${temporary}"
  mv -f "${temporary}" "${secrets_file}"
  warn "检测到旧版 MySQL 复制凭据策略；已在全新安装阶段轮换为兼容的统一受管凭据"
}

state_cluster_id() { "${jq_binary}" -r '.cluster.resource_id' "${state_file}"; }
state_phase() { "${jq_binary}" -r '.cluster.phase // "pending"' "${state_file}"; }
set_state_phase() {
  local phase="$1" temporary
  temporary="$(mktemp "${state_file}.XXXXXX")"
  "${jq_binary}" --arg phase "${phase}" '.cluster.phase=$phase' "${state_file}" >"${temporary}"
  chmod 0600 "${temporary}"; mv "${temporary}" "${state_file}"
}

state_node_field() {
  local address="$1" hostname="$2" field="$3"
  "${jq_binary}" -r --arg address "${address}" --arg hostname "${hostname}" --arg field "${field}" \
    'first(.nodes[] | select(.address==$address or ($hostname!="" and .system_hostname==$hostname)) | .[$field]) // empty' "${state_file}"
}

store_node_state() {
  local address="$1" role="$2" hostname="$3" resource_id="$4" fixed_name="$5" database_id="$6" temporary
  temporary="$(mktemp "${state_file}.XXXXXX")"
  "${jq_binary}" --arg address "${address}" --arg role "${role}" --arg hostname "${hostname}" --arg resource_id "${resource_id}" --arg node_name "${fixed_name}" --arg instance_id "${database_id}" '
    .nodes = ([.nodes[] | select(.address != $address and .resource_id != $resource_id and .node_name != $node_name and ($hostname=="" or .system_hostname != $hostname))] + [{address:$address,role:$role,system_hostname:$hostname,resource_id:$resource_id,node_name:$node_name,instance_id:$instance_id}])
  ' "${state_file}" >"${temporary}"
  "${jq_binary}" -e '([.nodes[].resource_id]|unique|length)==(.nodes|length) and ([.nodes[].node_name]|unique|length)==(.nodes|length)' "${temporary}" >/dev/null || die "固定节点身份发生冲突"
  chmod 0600 "${temporary}"; mv "${temporary}" "${state_file}"
}

assert_node_identity_unique() {
  local address="$1" resource_id="$2" fixed_name="$3" hostname="$4" index other
  for ((index=0; index<${#all_nodes[@]}; index++)); do
    other="${all_nodes[${index}]}"
    [[ "${other}" == "${address}" || -z "${node_ids[${index}]:-}" ]] && continue
    [[ "${node_ids[${index}]}" != "${resource_id}" ]] || die "${address} 与 ${other} 报告了相同 resource_id，拒绝合并节点"
    [[ "${node_names[${index}]}" != "${fixed_name}" ]] || die "${address} 与 ${other} 报告了相同固定节点名 ${fixed_name}"
    [[ "${node_hostnames[${index}]}" != "${hostname}" ]] || die "${address} 与 ${other} 使用了相同 hostname ${hostname}"
  done
}

discover_node_identities() {
  local host role hostname remote_identity resource fixed database index=1
  for host in "${all_nodes[@]}"; do
    log "确认固定节点身份：${host}"
    remote_exec "${host}" "test \"\$(id -u)\" -eq 0 && command -v bash >/dev/null && command -v systemctl >/dev/null" >/dev/null || die "${host} 必须允许 root 级安装并使用 systemd"
    hostname="$(remote_exec "${host}" "hostname -f 2>/dev/null || hostname")"
    hostname="${hostname%%$'\n'*}"
    [[ "${hostname}" =~ ^[A-Za-z0-9._-]+$ ]] || die "${host} 返回了无效 hostname：$(printf '%q' "${hostname}")"
    remote_identity="$(remote_exec "${host}" "test -s /etc/clusterguard/node.json && cat /etc/clusterguard/node.json || true")"
    resource=""; fixed=""; database=""
    if [[ -n "${remote_identity}" ]] && "${jq_binary}" -e '.resource_id and .node_name' <<<"${remote_identity}" >/dev/null 2>&1; then
      resource="$("${jq_binary}" -r '.resource_id' <<<"${remote_identity}")"
      fixed="$("${jq_binary}" -r '.node_name' <<<"${remote_identity}")"
    fi
    [[ -n "${resource}" ]] || resource="$(state_node_field "${host}" "${hostname}" resource_id)"
    [[ -n "${fixed}" ]] || fixed="$(state_node_field "${host}" "${hostname}" node_name)"
    database="$(state_node_field "${host}" "${hostname}" instance_id)"
    [[ -n "${resource}" ]] || resource="$(uuid)"
    [[ -n "${fixed}" ]] || fixed="$(printf 'cg-node-%04d' "${index}")"
    if data_node_contains "${host}" && [[ -z "${database}" ]]; then database="$(uuid)"; fi
    role="$(node_role_for "${host}")"
    assert_node_identity_unique "${host}" "${resource}" "${fixed}" "${hostname}"
    set_node_identity "${host}" "${resource}" "${fixed}" "${hostname}" "${database}"
    store_node_state "${host}" "${role}" "${hostname}" "${resource}" "${fixed}" "${database}"
    index=$((index + 1))
  done
}

remote_preflight() {
  local host role output architecture="" expected_arch="" build_platform="" expected_build_platform="" build_node_required=false controller_required_kb database_required_kb media_required_kb source_build_required_kb
  controller_required_kb=$((controller_min_free_gb * 1024 * 1024))
  database_required_kb=$((database_min_free_gb * 1024 * 1024))
  source_build_required_kb=$((postgresql_build_min_free_gb * 1024 * 1024))
  media_required_kb=$(( ($(wc -c <"${application_rpm}") + 1048575) / 1024 + 1048576 ))
  if [[ "${database_engine}" != "none" ]]; then
    media_required_kb=$((media_required_kb + ($(wc -c <"${database_package}") + 1023) / 1024 * 2))
  fi
  for host in "${all_nodes[@]}"; do
    role="$(node_role_for "${host}")"
    build_node_required=false
    [[ "${database_package_kind}" == "postgresql-source" && "${host}" == "${postgresql_build_node}" ]] && build_node_required=true
    log "执行远端前置检查：${host} (${role})"
    output="$(remote_exec "${host}" "
set -euo pipefail
for command_name in bash systemctl rpm tar df dirname find ss hostname uname install getconf; do command -v \"\${command_name}\" >/dev/null 2>&1 || { echo \"missing:\${command_name}\" >&2; exit 20; }; done
nearest_existing() { path=\"\$1\"; while [[ ! -e \"\${path}\" ]]; do parent=\"\$(dirname \"\${path}\")\"; [[ \"\${parent}\" != \"\${path}\" ]] || break; path=\"\${parent}\"; done; printf '%s\\n' \"\${path}\"; }
available_kb() { df -Pk \"\$(nearest_existing \"\$1\")\" | awk 'END {print \$4}'; }
arch=\"\$(uname -m)\"
case \"\${arch}\" in x86_64|aarch64) ;; *) echo \"unsupported architecture: \${arch}\" >&2; exit 21 ;; esac
media_free=\"\$(available_kb /opt/clusterguard)\"
(( media_free >= ${media_required_kb} )) || { echo \"insufficient /opt space: \${media_free} KiB\" >&2; exit 22; }
if [[ '${role}' == controller || '${role}' == mixed ]]; then
  control_free=\"\$(available_kb '${controller_data_root}')\"
  (( control_free >= ${controller_required_kb} )) || { echo \"insufficient controller space: \${control_free} KiB\" >&2; exit 23; }
  for port in '${api_port}' '${raft_port}'; do
    if ss -H -ltn \"sport = :\${port}\" 2>/dev/null | grep -q . && ! systemctl is-active --quiet clusterguard-ha.service; then
      echo \"controller port already owned by another process: \${port}\" >&2
      exit 24
    fi
  done
fi
if [[ '${role}' == data || '${role}' == mixed ]]; then
  database_free=\"\$(available_kb '${database_data_root}')\"
  (( database_free >= ${database_required_kb} )) || { echo \"insufficient database space: \${database_free} KiB\" >&2; exit 25; }
fi
if [[ '${build_node_required}' == true ]]; then
  source_build_free=\"\$(available_kb '/var/lib/clusterguard/postgresql-source-build')\"
  (( source_build_free >= ${source_build_required_kb} )) || { echo \"insufficient PostgreSQL source build space: \${source_build_free} KiB\" >&2; exit 26; }
fi
ntp=unknown
os_id=unknown
os_version=unknown
if [[ -r /etc/os-release ]]; then
  . /etc/os-release
  os_id="\${ID:-unknown}"
  os_version="\${VERSION_ID:-unknown}"
fi
glibc=\"\$(getconf GNU_LIBC_VERSION 2>/dev/null | tr ' ' '_' || printf unknown)\"
if command -v timedatectl >/dev/null 2>&1; then ntp=\"\$(timedatectl show -p NTPSynchronized --value 2>/dev/null || printf unknown)\"; fi
printf 'CG_PREFLIGHT|%s|%s|%s|%s|%s\\n' \"\${arch}\" \"\${ntp}\" \"\${os_id}\" \"\${os_version}\" \"\${glibc}\"
")"
    output="$(printf '%s\n' "${output}" | tail -n 1)"
    [[ "${output}" == CG_PREFLIGHT\|* ]] || die "${host} 前置检查没有返回可验证结果"
    architecture="$(cut -d'|' -f2 <<<"${output}")"
    if [[ -z "${expected_arch}" ]]; then expected_arch="${architecture}"; fi
    [[ "${architecture}" == "${expected_arch}" ]] || die "所有节点 CPU 架构必须一致：${host}=${architecture}，期望 ${expected_arch}"
    if [[ "${database_package_kind}" == "postgresql-source" ]]; then
      build_platform="${architecture}|$(cut -d'|' -f4 <<<"${output}")|$(cut -d'|' -f5 <<<"${output}")|$(cut -d'|' -f6 <<<"${output}")"
      [[ -n "${expected_build_platform}" ]] || expected_build_platform="${build_platform}"
      [[ "${build_platform}" == "${expected_build_platform}" ]] || die "PostgreSQL 源码统一制品要求所有控制/数据节点的架构、发行版版本和 glibc 一致：${host}=${build_platform}，期望 ${expected_build_platform}"
    fi
    [[ "$(cut -d'|' -f3 <<<"${output}")" == "yes" ]] || warn "${host} 未确认 NTP/Chrony 已同步；生产启用前必须处理"
  done
}

# Agent requests are short-lived and signed. A material clock skew would make a
# healthy peer reject the leader's request as expired, which must never surface
# as a misleading topology or VIP fault after installation has begun.
verify_cluster_clock_skew() {
  local host output epoch minimum_epoch=0 maximum_epoch=0 skew=0
  for host in "${all_nodes[@]}"; do
    output="$(remote_exec "${host}" "date -u +%s")"
    epoch="${output%%$'\n'*}"
    [[ "${epoch}" =~ ^[0-9]{10,}$ ]] || die "${host} 未返回可验证的 UTC 时间戳"
    if (( minimum_epoch == 0 || epoch < minimum_epoch )); then minimum_epoch="${epoch}"; fi
    if (( epoch > maximum_epoch )); then maximum_epoch="${epoch}"; fi
  done
  skew=$((maximum_epoch - minimum_epoch))
  if (( skew > 30 )); then
    die "节点系统时钟偏差为 ${skew} 秒，超过 30 秒安全上限；请先校正 RTC 或时间后重试，安装器随后会配置固定内部时钟源"
  fi
  log "节点时钟一致性已确认，最大偏差 ${skew} 秒"
}

clock_mesh_tool() {
  local candidate
  for candidate in "${script_dir}/tools/clusterguard-clock-mesh.sh" "${script_dir}/clusterguard-clock-mesh.sh"; do
    [[ -f "${candidate}" ]] && { printf '%s\n' "${candidate}"; return 0; }
  done
  die "缺少固定内部时钟源工具 clusterguard-clock-mesh.sh"
}

configure_fixed_cluster_clock_source() {
  local source_host="${controller_nodes[0]}" source_subnet tool host
  valid_ipv4 "${source_host}" || die "固定内部时钟源必须使用 IPv4 控制节点，当前为：${source_host}"
  source_subnet="${source_host%.*}.0/24"
  tool="$(clock_mesh_tool)"

  log "配置固定内部时钟源：${source_host}（不跟随 VIP 或数据库主库切换）"
  for host in "${all_nodes[@]}"; do
    remote_exec_checked "${host}" "创建时钟工具目录" "install -d -m 0755 /usr/local/sbin"
    remote_copy "${tool}" "${host}" "/usr/local/sbin/clusterguard-clock-mesh.sh"
    remote_exec_checked "${host}" "安装固定内部时钟源工具" "chmod 0755 /usr/local/sbin/clusterguard-clock-mesh.sh"
  done
  remote_exec_checked "${source_host}" "启用固定内部时钟源" "/usr/local/sbin/clusterguard-clock-mesh.sh --server --subnet '${source_subnet}'"
  for host in "${all_nodes[@]}"; do
    [[ "${host}" == "${source_host}" ]] && continue
    remote_exec_checked "${host}" "同步固定内部时钟源" "/usr/local/sbin/clusterguard-clock-mesh.sh --client --server-address '${source_host}'"
  done
}

install_rpm_on_node() {
  local host="$1" stage="${remote_stage}/packages" rpm_name dependencies_archive=""
  rpm_name="$(basename "${application_rpm}")"
  remote_exec "${host}" "install -d -m 0700 '${stage}'"
  remote_copy "${application_rpm}" "${host}" "${stage}/${rpm_name}"
  if [[ -n "${dependency_dir}" ]]; then
    dependencies_archive="${work_dir}/dependencies.tar"
    tar --no-xattrs --exclude='._*' -C "${dependency_dir}" -cf "${dependencies_archive}" .
    remote_copy "${dependencies_archive}" "${host}" "${stage}/dependencies.tar"
    remote_exec "${host}" "rm -rf '${stage}/dependencies' && install -d -m 0700 '${stage}/dependencies' && tar -xf '${stage}/dependencies.tar' -C '${stage}/dependencies'"
  fi
  remote_exec_checked "${host}" "安装 ClusterGuard RPM" "
set -euo pipefail
host_arch=\"\$(uname -m)\"
package_arch=\"\$(rpm -qp --qf '%{ARCH}' '${stage}/${rpm_name}')\"
case \"\${host_arch}:\${package_arch}\" in
  x86_64:x86_64|x86_64:noarch|aarch64:aarch64|aarch64:noarch) ;;
  *) echo \"ClusterGuard RPM architecture mismatch: host=\${host_arch}, package=\${package_arch}\" >&2; exit 26 ;;
esac
deps=()
if [[ -d '${stage}/dependencies' ]]; then
  while IFS= read -r dependency; do deps+=(\"\${dependency}\"); done < <(find '${stage}/dependencies' -maxdepth 1 -type f -name '*.rpm' ! -name '._*' -print | sort)
fi
if ((\${#deps[@]} > 0)); then
  for system_key in /etc/pki/rpm-gpg/RPM-GPG-KEY-*; do
    [[ -f \"\${system_key}\" ]] || continue
    rpm --import \"\${system_key}\"
  done
fi
for dependency in \"\${deps[@]}\"; do
  rpm --checksig \"\${dependency}\" >/dev/null || { echo \"dependency RPM signature verification failed: \${dependency}\" >&2; exit 27; }
done
if [[ -f '${stage}/dependencies/SHA256SUMS' ]]; then
  (cd '${stage}/dependencies' && sha256sum -c SHA256SUMS >/dev/null) || {
    echo 'dependency repository checksum verification failed' >&2
    exit 27
  }
fi
repo_options=()
runtime_packages=()
if ((\${#deps[@]} > 0)); then
  [[ -f '${stage}/dependencies/repodata/repomd.xml' ]] || {
    echo 'dependency repository metadata is missing: repodata/repomd.xml' >&2
    exit 27
  }
  install -d -m 0700 '${stage}/repo-config'
  cat >'${stage}/repo-config/clusterguard-offline.repo' <<'REPO'
[clusterguard-offline]
name=ClusterGuard HA verified offline dependencies
baseurl=file://${stage}/dependencies
enabled=1
gpgcheck=0
repo_gpgcheck=0
metadata_expire=-1
REPO
  repo_options=(--disablerepo='*' --enablerepo=clusterguard-offline --setopt=reposdir='${stage}/repo-config' --setopt=install_weak_deps=False)
  if [[ '${database_engine}' == mysql ]]; then
    for package_name in libaio ncurses-compat-libs numactl-libs; do
      rpm -q \"\${package_name}\" >/dev/null 2>&1 || runtime_packages+=(\"\${package_name}\")
    done
  fi
fi
if command -v dnf >/dev/null 2>&1; then
  dnf \"\${repo_options[@]}\" --nogpgcheck install -y \"\${runtime_packages[@]}\" '${stage}/${rpm_name}'
elif command -v yum >/dev/null 2>&1; then
  yum \"\${repo_options[@]}\" --nogpgcheck localinstall -y \"\${runtime_packages[@]}\" '${stage}/${rpm_name}'
else
  rpm -Uvh '${stage}/${rpm_name}'
fi
rpm -q clusterguard-ha >/dev/null
ln -sfn /usr/local/libexec/jq-linux-amd64 /usr/local/bin/jq
"
}

configure_node_firewall() {
  local host="$1" role ports=""
  role="$(node_role_for "${host}")"
  case "${role}" in
    controller) ports="${api_port} ${raft_port}" ;;
    data) ports="${database_port}" ;;
    mixed) ports="${api_port} ${raft_port} ${database_port}" ;;
    *) die "未知节点角色：${role}" ;;
  esac
  remote_exec_checked "${host}" "配置 ClusterGuard 防火墙端口" "
set -euo pipefail
if ! command -v firewall-cmd >/dev/null 2>&1 || ! systemctl is-active --quiet firewalld.service; then
  exit 0
fi
zone=\"\$(firewall-cmd --get-zone-of-interface='${vip_interface}' 2>/dev/null || true)\"
if [[ -z \"\${zone}\" || \"\${zone}\" == no\ zone ]]; then
  zone=\"\$(firewall-cmd --get-default-zone)\"
fi
[[ \"\${zone}\" =~ ^[A-Za-z0-9_.-]+$ ]] || { echo \"unable to determine a safe firewalld zone for ${vip_interface}\" >&2; exit 28; }
for port in ${ports}; do
  firewall-cmd --zone=\"\${zone}\" --query-port=\"\${port}/tcp\" >/dev/null 2>&1 ||
    firewall-cmd --zone=\"\${zone}\" --add-port=\"\${port}/tcp\" >/dev/null
  firewall-cmd --permanent --zone=\"\${zone}\" --query-port=\"\${port}/tcp\" >/dev/null 2>&1 ||
    firewall-cmd --permanent --zone=\"\${zone}\" --add-port=\"\${port}/tcp\" >/dev/null
done
install -d -m 0750 /etc/clusterguard
manifest=/etc/clusterguard/firewall-ports.managed
touch \"\${manifest}\"
chmod 0640 \"\${manifest}\"
for port in ${ports}; do
  entry='$(state_cluster_id)|'\"\${zone}\"'|'\"\${port}\"'/tcp|${role}'
  grep -Fqx \"\${entry}\" \"\${manifest}\" || printf '%s\\n' \"\${entry}\" >>\"\${manifest}\"
done
"
}

configure_cluster_firewalls() {
  local host
  for host in "${all_nodes[@]}"; do
    log "配置节点防火墙：${host} ($(node_role_for "${host}"))"
    configure_node_firewall "${host}"
  done
}

reconcile_database_installation_phase() {
  local phase marker_check host managed_count=0
  phase="$(state_phase)"
  [[ "${database_engine}" != "none" && "${phase}" != "pending" ]] || return 0
  case "${database_engine}" in
    mysql)
      marker_check="test -f '${database_data_root}/mysql/${database_port}/.clusterguard-managed' && test -f '/etc/systemd/system/clusterguard-mysql-${database_port}.service'"
      ;;
    postgresql)
      marker_check="test -f '/etc/systemd/system/clusterguard-postgresql-${database_port}.service' && test -f '${database_data_root}/postgresql/${database_port}/data/PG_VERSION'"
      ;;
    *) return 0 ;;
  esac
  for host in "${data_nodes[@]}"; do
    if remote_exec "${host}" "${marker_check}"; then
      managed_count=$((managed_count + 1))
    fi
  done
  if ((managed_count == 0)); then
    warn "部署状态为 ${phase}，但所有数据节点的 ClusterGuard 管理标记均不存在；按完整快照回退场景重新从 pending 安装"
    set_state_phase pending
    return 0
  fi
  if ((managed_count != ${#data_nodes[@]})); then
    die "部署状态与远端节点不一致：${#data_nodes[@]} 个数据节点仅 ${managed_count} 个保留管理标记；拒绝整集群覆盖，请使用节点恢复流程"
  fi
}

verify_remote_tcp_reachability() {
  local probe_host="$1" target_host="$2" target_port="$3" label="$4"
  remote_exec_checked "${probe_host}" "${label}" "timeout 5s bash -c 'exec 3<>/dev/tcp/${target_host}/${target_port}'"
}

distribute_database_package() {
  local host package_name destination
  package_name="$(basename "${database_package}")"
  destination="/opt/clusterguard/packages/${package_name}"
  for host in "${all_nodes[@]}"; do
    remote_exec "${host}" "install -d -m 0750 /opt/clusterguard/packages"
    remote_copy "${database_package}" "${host}" "${destination}"
    remote_exec "${host}" "chmod 0640 '${destination}'"
  done
}

prepare_postgresql_binary_package() {
  [[ "${database_package_kind}" == "postgresql-source" ]] || return 0
  local source_package="${database_package}" source_name remote_source remote_output remote_artifact artifact_name local_output local_artifact jobs_argument="" repository_argument="" dependencies_archive=""
  source_name="$(basename "${source_package}")"
  [[ "${source_name}" =~ ^[A-Za-z0-9._+-]+$ ]] || die "PostgreSQL 源码包文件名包含不支持的字符"
  remote_source="/opt/clusterguard/packages/source/${source_name}"
  remote_output="/opt/clusterguard/packages/source-build"
  local_output="${work_dir}/generated-database"
  install -d -m 0700 "${local_output}"
  log "上传 PostgreSQL 官方源码：${postgresql_build_node}"
  remote_exec "${postgresql_build_node}" "install -d -m 0750 /opt/clusterguard/packages/source '${remote_output}'"
  remote_copy "${source_package}" "${postgresql_build_node}" "${remote_source}"
  remote_exec "${postgresql_build_node}" "chmod 0640 '${remote_source}'"
  [[ -z "${postgresql_build_jobs}" ]] || jobs_argument="--jobs '${postgresql_build_jobs}'"
  if [[ -n "${postgresql_dependency_dir}" ]]; then
    [[ -f "${postgresql_dependency_dir}/repodata/repomd.xml" ]] || die "PostgreSQL 独立依赖包缺少 dependencies/repodata/repomd.xml"
    dependencies_archive="${work_dir}/postgresql-dependencies.tar"
    tar --no-xattrs --exclude='._*' -C "${postgresql_dependency_dir}" -cf "${dependencies_archive}" .
    remote_copy "${dependencies_archive}" "${postgresql_build_node}" "${remote_stage}/packages/postgresql-dependencies.tar"
    remote_exec "${postgresql_build_node}" "rm -rf '${remote_stage}/packages/postgresql-dependencies' && install -d -m 0700 '${remote_stage}/packages/postgresql-dependencies' && tar -xf '${remote_stage}/packages/postgresql-dependencies.tar' -C '${remote_stage}/packages/postgresql-dependencies'"
    repository_argument="--dependency-repository '${remote_stage}/packages/postgresql-dependencies'"
    log "使用已上传的 PostgreSQL 独立编译依赖包"
  else
    repository_argument="--online-dependencies"
    log "PostgreSQL 编译依赖默认从构建节点已配置的软件源联网获取；仅写入一次性隔离构建根"
  fi
  log "在 ${postgresql_build_node} 编译 PostgreSQL（全集群仅此一次）"
  remote_artifact="$(remote_exec "${postgresql_build_node}" "
set -euo pipefail
/usr/local/libexec/clusterguard-postgresql-build.sh \\
  --source-package '${remote_source}' \\
  --output-dir '${remote_output}' \\
  --install-prefix '/opt/clusterguard/postgresql/${database_port}/software' \\
  --version '${database_version}' ${jobs_argument} ${repository_argument}
")"
  remote_artifact="$(printf '%s\n' "${remote_artifact}" | tail -n 1 | tr -d '\r')"
  [[ "${remote_artifact}" == "${remote_output}/"*.tar.gz ]] || die "PostgreSQL 源码构建未返回受控制品路径：${remote_artifact}"
  artifact_name="$(basename "${remote_artifact}")"
  [[ "${artifact_name}" =~ ^postgresql-[A-Za-z0-9._+-]+-clusterguard-linux-(x86_64|aarch64)-p[0-9]+[.]tar[.]gz$ ]] || die "PostgreSQL 源码构建返回了无效制品名：${artifact_name}"
  local_artifact="${local_output}/${artifact_name}"
  log "回收并校验统一 PostgreSQL 制品：${artifact_name}"
  remote_fetch "${postgresql_build_node}" "${remote_artifact}" "${local_artifact}"
  remote_fetch "${postgresql_build_node}" "${remote_artifact}.sha256" "${local_artifact}.sha256"
  verify_sidecar "${local_artifact}"
  database_package="${local_artifact}"
  database_package_kind=""
  validate_database_media
  [[ "${database_package_kind}" == "binary" ]] || die "源码构建结果未通过 PostgreSQL 二进制制品校验"
  log "PostgreSQL 统一制品就绪；后续所有节点使用同一 SHA256 内容"
}

mysql_server_id() {
  local checksum
  checksum="$(printf '%s' "$1:${database_port}" | cksum | awk '{print $1}')"
  printf '%s\n' "$((checksum % 4294967294 + 1))"
}

mysql_payload() {
  local host="$1" package_name root discovery operation replication
  package_name="$(basename "${database_package}")"
  root="$(read_env_value CG_NODE_MYSQL_ROOT_PASSWORD "${secrets_file}")"
  discovery="$(read_env_value CG_MYSQL_DISCOVERY_PASSWORD "${secrets_file}")"
  operation="$(read_env_value CG_MYSQL_OPERATION_PASSWORD "${secrets_file}")"
  replication="$(read_env_value CG_MYSQL_REPLICATION_PASSWORD "${secrets_file}")"
  "${jq_binary}" -nc --arg node_id "$(node_id_for "${host}")" --arg node_name "$(node_name_for "${host}")" --arg hostname "$(node_hostname_for "${host}")" --arg ip "${host}" --arg package "/opt/clusterguard/packages/${package_name}" --arg data_root "${database_data_root}" --arg root "${root}" --arg mysql_root_remote_host "${mysql_root_remote_host}" --arg discovery "${discovery}" --arg operation "${operation}" --arg replication "${replication}" --argjson port "${database_port}" --argjson server_id "$(mysql_server_id "${host}")" '
    {target:{node_id:$node_id,node_name:$node_name,hostname:$hostname,ip_address:$ip,mysql_port:$port,server_id:$server_id,package_path:$package,data_root:$data_root,mysql_root_remote_host:$mysql_root_remote_host,rebuild:false},secrets:{mysql_root_password:$root,mysql_discovery_username:"cg_discovery",mysql_discovery_password:$discovery,mysql_operation_username:"cg_operator",mysql_operation_password:$operation,mysql_replication_username:"cg_replication",replication_password:$replication}}
  '
}

postgresql_payload() {
  local host="$1" package_name admin replication data_directory
  package_name="$(basename "${database_package}")"
  admin="$(read_env_value CG_POSTGRESQL_ADMIN_PASSWORD "${secrets_file}")"
  replication="$(read_env_value CG_POSTGRESQL_REPLICATION_PASSWORD "${secrets_file}")"
  data_directory="${database_data_root}/postgresql/${database_port}/data"
  "${jq_binary}" -nc --arg node_id "$(node_id_for "${host}")" --arg node_name "$(node_name_for "${host}")" --arg hostname "$(node_hostname_for "${host}")" --arg ip "${host}" --arg version "${database_version}" --arg package "/opt/clusterguard/packages/${package_name}" --arg data_directory "${data_directory}" --arg admin "${admin}" --arg replication "${replication}" --argjson port "${database_port}" '
    {target:{node_id:$node_id,node_name:$node_name,hostname:$hostname,ip_address:$ip,postgresql_version:$version,postgresql_port:$port,postgresql_service:("clusterguard-postgresql-"+($port|tostring)+".service"),postgresql_data_directory:$data_directory,package_path:$package,rebuild:true},secrets:{postgresql_admin_password:$admin,postgresql_replication_password:$replication}}
  '
}

remote_json_helper() {
  local host="$1" helper="$2" argument="$3" payload="$4" environment_prefix="${5:-}" payload_file remote_payload command
  payload_file="$(mktemp "${work_dir}/payload.XXXXXX.json")"
  printf '%s\n' "${payload}" >"${payload_file}"; chmod 0600 "${payload_file}"
  remote_payload="${remote_stage}/$(basename "${payload_file}")"
  remote_copy "${payload_file}" "${host}" "${remote_payload}"
  rm -f "${payload_file}"
  command="PATH=/usr/local/bin:/usr/local/libexec:/usr/sbin:/usr/bin:/sbin:/bin '${helper}'"
  [[ -z "${environment_prefix}" ]] || command="${environment_prefix} ${command}"
  [[ -z "${argument}" ]] || command+=" '${argument}'"
  remote_exec_checked "${host}" "$(basename "${helper}")" "${command} <'${remote_payload}'; status=\$?; rm -f '${remote_payload}'; exit \${status}"
}

verify_managed_mysql_node() {
  local host="$1" stage="$2"
  remote_exec_checked "${host}" "验证 MySQL 节点 ${stage}" "
set -euo pipefail
service='clusterguard-mysql-${database_port}.service'
client='/etc/clusterguard/mysql/${database_port}-client.cnf'
managed='${database_data_root}/mysql/${database_port}/.clusterguard-managed'
systemctl is-enabled --quiet \"\${service}\"
systemctl is-active --quiet \"\${service}\"
test -s \"\${client}\"
test -f \"\${managed}\"
/opt/clusterguard/mysql/${database_port}/software/bin/mysql --defaults-file=\"\${client}\" --protocol=tcp --host=127.0.0.1 --port='${database_port}' --connect-timeout=5 --batch --skip-column-names -e 'SELECT @@server_uuid, @@read_only, @@super_read_only' >/dev/null
"
}

initialize_mysql_cluster() {
  local phase host payload primary sync_payload root discovery operation replication
  phase="$(state_phase)"
  if [[ "${phase}" == "pending" ]]; then
    for host in "${data_nodes[@]}"; do
      if remote_exec "${host}" "ss -H -ltn 'sport = :${database_port}' 2>/dev/null | grep -q . && test ! -f '${database_data_root}/mysql/${database_port}/.clusterguard-managed'"; then
        die "${host}:${database_port} 已有非 ClusterGuard 管理实例，拒绝覆盖"
      fi
      log "安装或续接 MySQL：${host}:${database_port}"
      payload="$(mysql_payload "${host}")"
      remote_json_helper "${host}" /usr/local/libexec/clusterguard-mysql-install.sh "" "${payload}"
      verify_managed_mysql_node "${host}" "安装后"
    done
    set_state_phase installed
    phase="installed"
  fi
  if [[ "${phase}" == "installed" ]]; then
    primary="${data_nodes[0]}"
    root="$(read_env_value CG_NODE_MYSQL_ROOT_PASSWORD "${secrets_file}")"
    discovery="$(read_env_value CG_MYSQL_DISCOVERY_PASSWORD "${secrets_file}")"
    operation="$(read_env_value CG_MYSQL_OPERATION_PASSWORD "${secrets_file}")"
    replication="$(read_env_value CG_MYSQL_REPLICATION_PASSWORD "${secrets_file}")"
    local index=0
    for host in "${data_nodes[@]}"; do
      if ((index == 0)); then index=$((index + 1)); continue; fi
      log "同步副本：${primary}:${database_port} -> ${host}:${database_port}"
      verify_remote_tcp_reachability "${host}" "${primary}" "${database_port}" "MySQL 复制源连通性 ${host} -> ${primary}:${database_port}"
      sync_payload="$("${jq_binary}" -nc --arg cluster_id "$(state_cluster_id)" --arg donor_host "$(node_hostname_for "${primary}")" --arg donor_ip "${primary}" --arg target_node_id "$(node_id_for "${host}")" --arg target_name "$(node_name_for "${host}")" --arg target_host "$(node_hostname_for "${host}")" --arg target_ip "${host}" --arg data_root "${database_data_root}" --arg vip "${vip}" --arg root "${root}" --arg mysql_root_remote_host "${mysql_root_remote_host}" --arg discovery "${discovery}" --arg operation "${operation}" --arg replication "${replication}" --argjson port "${database_port}" '
        {request:{cluster_id:$cluster_id,donor:{hostname:$donor_host,ip_address:$donor_ip,port:$port},vip:$vip},target:{node_id:$target_node_id,node_name:$target_name,hostname:$target_host,ip_address:$target_ip,mysql_port:$port,data_root:$data_root,mysql_root_remote_host:$mysql_root_remote_host,rebuild:false},secrets:{mysql_root_password:$root,mysql_discovery_username:"cg_discovery",mysql_discovery_password:$discovery,mysql_operation_username:"cg_operator",mysql_operation_password:$operation,mysql_replication_username:"cg_replication",replication_password:$replication}}
      ')"
      remote_json_helper "${host}" /usr/local/libexec/clusterguard-mysql-sync.sh logical_dump "${sync_payload}"
      verify_managed_mysql_node "${host}" "副本同步后"
      index=$((index + 1))
    done
    remote_exec "${primary}" "/opt/clusterguard/mysql/${database_port}/software/bin/mysql --defaults-file=/etc/clusterguard/mysql/${database_port}-client.cnf --protocol=tcp --host=127.0.0.1 --port=${database_port} -e 'SET GLOBAL super_read_only=OFF; SET GLOBAL read_only=OFF;'"
    verify_managed_mysql_node "${primary}" "主库写入启用后"
    set_state_phase synchronized
  fi
}

initialize_postgresql_cluster() {
  local phase host payload primary sync_payload admin replication system_identifier index
  phase="$(state_phase)"
  if [[ "${phase}" == "pending" ]]; then
    for host in "${data_nodes[@]}"; do
      if remote_exec "${host}" "ss -H -ltn 'sport = :${database_port}' 2>/dev/null | grep -q . && test ! -f '/etc/systemd/system/clusterguard-postgresql-${database_port}.service'"; then
        die "${host}:${database_port} 已有非 ClusterGuard 管理的 PostgreSQL 实例，拒绝覆盖"
      fi
      log "安装或续接 PostgreSQL：${host}:${database_port}"
      payload="$(postgresql_payload "${host}")"
      remote_json_helper "${host}" /usr/local/libexec/clusterguard-postgresql-install.sh "" "${payload}" "CG_POSTGRESQL_ALLOWED_CIDR='${postgresql_allowed_cidr}'" >/dev/null
    done
    set_state_phase installed
    phase="installed"
  fi
  if [[ "${phase}" == "installed" ]]; then
    primary="${data_nodes[0]}"
    admin="$(read_env_value CG_POSTGRESQL_ADMIN_PASSWORD "${secrets_file}")"
    replication="$(read_env_value CG_POSTGRESQL_REPLICATION_PASSWORD "${secrets_file}")"
    system_identifier="$(remote_exec "${primary}" "PGPASSFILE='/etc/clusterguard/postgresql/${database_port}.pass' /opt/clusterguard/postgresql/${database_port}/software/bin/psql --no-password --no-psqlrc --quiet --tuples-only --no-align --host 127.0.0.1 --port '${database_port}' --username postgres --dbname postgres --command 'SELECT system_identifier::text FROM pg_control_system()'")"
    system_identifier="$(printf '%s' "${system_identifier}" | tr -d '[:space:]')"
    [[ "${system_identifier}" =~ ^[0-9]+$ && "${system_identifier}" != "0" ]] || die "无法读取 PostgreSQL system_identifier"
    index=0
    for host in "${data_nodes[@]}"; do
      if ((index == 0)); then index=$((index + 1)); continue; fi
      log "同步 PostgreSQL 副本：${primary}:${database_port} -> ${host}:${database_port}"
      verify_remote_tcp_reachability "${host}" "${primary}" "${database_port}" "PostgreSQL 复制源连通性 ${host} -> ${primary}:${database_port}"
      sync_payload="$("${jq_binary}" -nc --arg cluster_id "$(state_cluster_id)" --arg donor_instance "$(instance_id_for "${primary}")" --arg donor_node "$(node_id_for "${primary}")" --arg donor_host "$(node_hostname_for "${primary}")" --arg donor_ip "${primary}" --arg system_id "${system_identifier}" --arg target_node "$(node_id_for "${host}")" --arg target_name "$(node_name_for "${host}")" --arg target_host "$(node_hostname_for "${host}")" --arg target_ip "${host}" --arg data_directory "${database_data_root}/postgresql/${database_port}/data" --arg vip "${vip}" --arg admin "${admin}" --arg replication "${replication}" --argjson port "${database_port}" '
        {request:{cluster_id:$cluster_id,donor:{instance_id:$donor_instance,native_resource_id:$donor_node,system_identifier:$system_id,hostname:$donor_host,ip_address:$donor_ip,port:$port},vip:$vip},target:{node_id:$target_node,node_name:$target_name,hostname:$target_host,ip_address:$target_ip,postgresql_port:$port,postgresql_service:("clusterguard-postgresql-"+($port|tostring)+".service"),postgresql_data_directory:$data_directory,rebuild:false},secrets:{postgresql_admin_password:$admin,postgresql_replication_password:$replication}}
      ')"
      remote_json_helper "${host}" /usr/local/libexec/clusterguard-postgresql-sync.sh pg_basebackup "${sync_payload}" >/dev/null
      index=$((index + 1))
    done
    set_state_phase synchronized
  fi
}

initialize_database_cluster() {
  case "${database_engine}" in
    mysql) initialize_mysql_cluster ;;
    postgresql) initialize_postgresql_cluster ;;
    none) return 0 ;;
  esac
}

install_database_client_on_controllers() {
  [[ "${database_engine}" == "none" ]] && return
  local host package_name
  package_name="$(basename "${database_package}")"
  for host in "${controller_nodes[@]}"; do
    if [[ "${database_engine}" == "mysql" ]]; then
      if remote_exec "${host}" "command -v mysql >/dev/null 2>&1"; then continue; fi
      # Mixed nodes already have the managed server distribution. Reuse its
      # client tools instead of extracting the database archive a second time.
      if remote_exec "${host}" "test -x '/opt/clusterguard/mysql/${database_port}/software/bin/mysql' && test -x '/opt/clusterguard/mysql/${database_port}/software/bin/mysqldump'"; then
        remote_exec_checked "${host}" "复用 ClusterGuard 管理的 MySQL 客户端" "
set -euo pipefail
install -d -m 0755 /usr/local/bin
ln -sfn '/opt/clusterguard/mysql/${database_port}/software/bin/mysql' /usr/local/bin/mysql
ln -sfn '/opt/clusterguard/mysql/${database_port}/software/bin/mysqldump' /usr/local/bin/mysqldump
/usr/local/bin/mysql --version
" >/dev/null
        continue
      fi
      remote_exec "${host}" "
set -euo pipefail
rm -rf /opt/clusterguard/mysql-client/software
install -d -m 0755 /opt/clusterguard/mysql-client/software /usr/local/bin
tar -xf '/opt/clusterguard/packages/${package_name}' -C /opt/clusterguard/mysql-client/software --strip-components=1
ln -sfn /opt/clusterguard/mysql-client/software/bin/mysql /usr/local/bin/mysql
ln -sfn /opt/clusterguard/mysql-client/software/bin/mysqldump /usr/local/bin/mysqldump
mysql --version
" >/dev/null
    else
      if remote_exec "${host}" "command -v psql >/dev/null 2>&1"; then continue; fi
      remote_exec "${host}" "
set -euo pipefail
rm -rf /opt/clusterguard/postgresql-client/software
install -d -m 0755 /opt/clusterguard/postgresql-client/software /usr/local/bin
tar -xf '/opt/clusterguard/packages/${package_name}' -C /opt/clusterguard/postgresql-client/software --strip-components=1
for binary in psql pg_basebackup pg_rewind pg_controldata; do
  test -x \"/opt/clusterguard/postgresql-client/software/bin/\${binary}\"
  ln -sfn \"/opt/clusterguard/postgresql-client/software/bin/\${binary}\" \"/usr/local/bin/\${binary}\"
done
psql --version
" >/dev/null
    fi
  done
}

generate_pki_and_ssh() {
  local pki="${work_dir}/pki-source" host fixed ext serial_args data_index subject_alt_name
  mkdir -p "${pki}" "${work_dir}/ssh"
  chmod 0700 "${pki}" "${work_dir}/ssh"
  if [[ ! -s "${pki}/api-ca.key" ]]; then
    openssl req -x509 -newkey rsa:3072 -nodes -sha256 -days 3650 -subj '/CN=ClusterGuard HA API CA' -keyout "${pki}/api-ca.key" -out "${pki}/api-ca.crt" >/dev/null 2>&1
    openssl req -x509 -newkey rsa:3072 -nodes -sha256 -days 3650 -subj '/CN=ClusterGuard HA Raft CA' -keyout "${pki}/raft-ca.key" -out "${pki}/raft-ca.crt" >/dev/null 2>&1
    chmod 0600 "${pki}"/*.key
  fi
  if [[ ! -s "${work_dir}/ssh/controller_ed25519" ]]; then
    ssh-keygen -q -t ed25519 -N '' -C "clusterguard-${cluster_name}" -f "${work_dir}/ssh/controller_ed25519"
  fi
  chmod 0600 "${work_dir}/ssh/controller_ed25519"
  for host in "${controller_nodes[@]}"; do
    fixed="$(node_name_for "${host}")"
    mkdir -p "${work_dir}/nodes/${host}/assets/tls" "${work_dir}/nodes/${host}/assets/pki" "${work_dir}/nodes/${host}/assets/ssh"
    ext="${work_dir}/nodes/${host}/api.ext"
    if valid_ipv4 "${host}"; then subject_alt_name="IP:${host},DNS:${fixed}"; else subject_alt_name="DNS:${host},DNS:${fixed}"; fi
    cat >"${ext}" <<EOF
subjectAltName=${subject_alt_name}
extendedKeyUsage=serverAuth,clientAuth
keyUsage=digitalSignature,keyEncipherment
EOF
    openssl req -new -newkey rsa:3072 -nodes -sha256 -subj "/CN=${fixed}" -keyout "${work_dir}/nodes/${host}/assets/tls/server.key" -out "${work_dir}/nodes/${host}/api.csr" >/dev/null 2>&1
    if [[ -s "${pki}/api-ca.srl" ]]; then serial_args=(-CAserial "${pki}/api-ca.srl"); else serial_args=(-CAcreateserial); fi
    openssl x509 -req -sha256 -days 825 -in "${work_dir}/nodes/${host}/api.csr" -CA "${pki}/api-ca.crt" -CAkey "${pki}/api-ca.key" "${serial_args[@]}" -extfile "${ext}" -out "${work_dir}/nodes/${host}/assets/tls/server.crt" >/dev/null 2>&1
    openssl req -new -newkey rsa:3072 -nodes -sha256 -subj "/CN=${fixed}-raft" -keyout "${work_dir}/nodes/${host}/assets/tls/raft.key" -out "${work_dir}/nodes/${host}/raft.csr" >/dev/null 2>&1
    if [[ -s "${pki}/raft-ca.srl" ]]; then serial_args=(-CAserial "${pki}/raft-ca.srl"); else serial_args=(-CAcreateserial); fi
    openssl x509 -req -sha256 -days 825 -in "${work_dir}/nodes/${host}/raft.csr" -CA "${pki}/raft-ca.crt" -CAkey "${pki}/raft-ca.key" "${serial_args[@]}" -extfile "${ext}" -out "${work_dir}/nodes/${host}/assets/tls/raft.crt" >/dev/null 2>&1
    install -m 0644 "${pki}/api-ca.crt" "${work_dir}/nodes/${host}/assets/tls/ca.crt"
    install -m 0644 "${pki}/raft-ca.crt" "${work_dir}/nodes/${host}/assets/tls/raft-ca.crt"
    install -m 0644 "${pki}/api-ca.crt" "${work_dir}/nodes/${host}/assets/pki/api-issuer.crt"
    install -m 0600 "${pki}/api-ca.key" "${work_dir}/nodes/${host}/assets/pki/api-issuer.key"
    install -m 0644 "${pki}/raft-ca.crt" "${work_dir}/nodes/${host}/assets/pki/raft-issuer.crt"
    install -m 0600 "${pki}/raft-ca.key" "${work_dir}/nodes/${host}/assets/pki/raft-issuer.key"
    install -m 0600 "${work_dir}/ssh/controller_ed25519" "${work_dir}/nodes/${host}/assets/ssh/controller_ed25519"
    install -m 0644 "${known_hosts_file}" "${work_dir}/nodes/${host}/assets/ssh/controller_known_hosts"
    if [[ -n "${fencer_file}" ]]; then
      mkdir -p "${work_dir}/nodes/${host}/assets/fencing"
      install -m 0750 "${fencer_file}" "${work_dir}/nodes/${host}/assets/fencing/clusterguard-fencer"
      if [[ -n "${fencer_assets_dir}" ]]; then
        while IFS= read -r fencer_asset; do
          install -m 0640 "${fencer_asset}" "${work_dir}/nodes/${host}/assets/fencing/$(basename "${fencer_asset}")"
        done < <(find "${fencer_assets_dir}" -mindepth 1 -maxdepth 1 -type f -print | sort)
      fi
    fi
  done
  for ((data_index=0; data_index<${#data_nodes[@]}; data_index++)); do
    host="${data_nodes[${data_index}]}"
    mkdir -p "${work_dir}/nodes/${host}/data-assets/tls"
    install -m 0644 "${pki}/api-ca.crt" "${work_dir}/nodes/${host}/data-assets/tls/ca.crt"
    remote_exec "${host}" "install -d -m 0700 /root/.ssh && touch /root/.ssh/authorized_keys && chmod 0600 /root/.ssh/authorized_keys"
    remote_copy "${work_dir}/ssh/controller_ed25519.pub" "${host}" "${remote_stage}/controller_ed25519.pub"
    remote_exec "${host}" "key=\$(cat '${remote_stage}/controller_ed25519.pub'); grep -Fqx \"\${key}\" /root/.ssh/authorized_keys || printf '%s\\n' \"\${key}\" >>/root/.ssh/authorized_keys; rm -f '${remote_stage}/controller_ed25519.pub'"
  done
}

peers_json() {
  local result='[]' host
  for host in "${controller_nodes[@]}"; do
    result="$("${jq_binary}" -nc --argjson current "${result}" --arg id "$(node_id_for "${host}")" --arg raft "${host}:${raft_port}" --arg api "https://${host}:${api_port}" '$current + [{resource_id:$id,address:$raft,api_address:$api}]')"
  done
  printf '%s\n' "${result}"
}

controller_urls_json() {
  local result='[]' host
  for host in "${controller_nodes[@]}"; do result="$("${jq_binary}" -nc --argjson current "${result}" --arg url "https://${host}:${api_port}" '$current+[$url]')"; done
  printf '%s\n' "${result}"
}

generate_controller_config() {
  local host="$1" bootstrap="$2" target="${work_dir}/nodes/${host}/clusterguard.json"
  "${jq_binary}" -n --arg http "0.0.0.0:${api_port}" --arg metadata "${controller_data_root}/metadata.json" --arg local_id "$(node_id_for "${host}")" --arg bind "0.0.0.0:${raft_port}" --arg advertise "${host}:${raft_port}" --arg raft_data "${controller_data_root}/raft" --arg mysql_root_remote_host "${mysql_root_remote_host}" --argjson bootstrap "${bootstrap}" --argjson peers "$(peers_json)" --argjson mysql_enabled "$([[ "${database_engine}" == "mysql" ]] && echo true || echo false)" --argjson mysql_automatic_failover "${mysql_automatic_failover}" --argjson postgresql_enabled "$([[ "${database_engine}" == "postgresql" ]] && echo true || echo false)" --argjson postgresql_automatic_failover "${postgresql_automatic_failover}" --argjson fencing_enabled "$([[ -n "${fencer_file}" ]] && echo true || echo false)" --argjson agent_quorum_fencing "${agent_quorum_fencing}" '
  {
    http_address:$http,tls_cert_file:"/etc/clusterguard/tls/server.crt",tls_key_file:"/etc/clusterguard/tls/server.key",tls_ca_file:"/etc/clusterguard/tls/ca.crt",metadata_path:$metadata,
    control_token_env:"CG_CONTROL_TOKEN",monitoring_token_env:"CG_MONITORING_TOKEN",approval_token_env:"CG_APPROVAL_TOKEN",
    consensus:{enabled:true,snapshot_cas_enabled:true,replicated_log_compression_enabled:true,local_id:$local_id,bind_address:$bind,advertise_address:$advertise,data_directory:$raft_data,tls_cert_file:"/etc/clusterguard/tls/raft.crt",tls_key_file:"/etc/clusterguard/tls/raft.key",tls_ca_file:"/etc/clusterguard/tls/raft-ca.crt",bootstrap:$bootstrap,apply_timeout_seconds:15,peers:$peers},
    mysql:{enabled:$mysql_enabled,semi_sync_required:true,discovery_interval_seconds:1,discovery_timeout_seconds:1,automatic_failover_enabled:$mysql_automatic_failover,automatic_failover_interval_seconds:1,automatic_failover_retry_seconds:30,discovery:{username:"cg_discovery",password_env:"CG_MYSQL_DISCOVERY_PASSWORD"},operation:{username:"cg_operator",password_env:"CG_MYSQL_OPERATION_PASSWORD"},replication:{username:"cg_replication",password_env:"CG_MYSQL_REPLICATION_PASSWORD"}},
    postgresql:{enabled:$postgresql_enabled,discovery_interval_seconds:1,discovery_timeout_seconds:1,automatic_failover_enabled:$postgresql_automatic_failover,automatic_failover_interval_seconds:1,automatic_failover_retry_seconds:30,discovery:{username:"postgres",database:"postgres",password_env:"CG_POSTGRESQL_DISCOVERY_PASSWORD"},operation:{username:"postgres",database:"postgres",password_env:"CG_POSTGRESQL_OPERATION_PASSWORD"},replication:{username:"clusterguard_repl",database:"postgres",password_env:"CG_POSTGRESQL_REPLICATION_PASSWORD"}},
    oracle:{enabled:false,discovery:{username:"CLUSTERGUARD_DG",database:"DB_UNIQUE_NAME",password_env:"CG_ORACLE_DISCOVERY_PASSWORD"}},
    sqlserver:{enabled:false,discovery:{username:"cg_monitor",database:"master",password_env:"CG_SQLSERVER_DISCOVERY_PASSWORD"}},
    agent:{enabled:true,user:"root",identity_file:"/etc/clusterguard/ssh/controller_ed25519",known_hosts_file:"/etc/clusterguard/ssh/controller_known_hosts",ssh_binary:"/usr/bin/ssh",agent_binary:"/usr/local/libexec/clusterguard-agent-stdio",agent_config_path:"/etc/clusterguard/agent.json",command_timeout_seconds:5,mutation_timeout_seconds:1800,max_concurrent_sessions:4,shared_secret_env:"CG_AGENT_SHARED_SECRET"},
    fencing:{enabled:$fencing_enabled,executable_path:"/usr/local/libexec/clusterguard-fencer",timeout_seconds:30,agent_quorum_enabled:$agent_quorum_fencing,agent_quorum_grace_seconds:15},
    node_lifecycle:{enabled:true,executor_path:"/usr/local/libexec/clusterguard-node-lifecycle.sh",package_repository:"/opt/clusterguard/packages",known_hosts_file:"/etc/clusterguard/ssh/controller_known_hosts",identity_file:"/etc/clusterguard/ssh/controller_ed25519",jq_binary:"/usr/local/libexec/jq-linux-amd64",adapter_runtime_helper:"/usr/local/libexec/clusterguard-adapter-runtime-install.sh",control_join_helper:"/usr/local/libexec/clusterguard-control-join.sh",control_api_issuer_cert_file:"/etc/clusterguard/pki/api-issuer.crt",control_api_issuer_key_file:"/etc/clusterguard/pki/api-issuer.key",control_raft_issuer_cert_file:"/etc/clusterguard/pki/raft-issuer.crt",control_raft_issuer_key_file:"/etc/clusterguard/pki/raft-issuer.key",control_certificate_validity_days:825,mysql_root_password_env:"CG_NODE_MYSQL_ROOT_PASSWORD",mysql_root_remote_host:$mysql_root_remote_host,replication_password_env:"CG_NODE_MYSQL_REPLICATION_PASSWORD",postgresql_install_helper:"/usr/local/libexec/clusterguard-postgresql-install.sh",postgresql_sync_helper:"/usr/local/libexec/clusterguard-postgresql-sync.sh",postgresql_admin_password_env:"CG_POSTGRESQL_ADMIN_PASSWORD",postgresql_replication_password_env:"CG_POSTGRESQL_REPLICATION_PASSWORD",clone_available:false,xtrabackup_versions:{},logical_dump_allowed:true,postgresql_basebackup_available:$postgresql_enabled,postgresql_rewind_available:$postgresql_enabled}
  }' >"${target}"
  chmod 0600 "${target}"
}

postgresql_peers_json() {
  local local_host="$1" result='[]' peer
  for peer in "${data_nodes[@]}"; do
    [[ "${peer}" == "${local_host}" ]] && continue
    result="$("${jq_binary}" -nc --argjson current "${result}" --arg instance_id "$(instance_id_for "${peer}")" --arg node_id "$(node_id_for "${peer}")" --arg hostname "$(node_hostname_for "${peer}")" --arg ip "${peer}" --argjson port "${database_port}" '$current+[{instance_id:$instance_id,node_id:$node_id,hostname:$hostname,ip_address:$ip,port:$port}]')"
  done
  printf '%s\n' "${result}"
}

generate_agent_config() {
  local host="$1" target="${work_dir}/nodes/${host}/agent.json"
  if [[ "${database_engine}" == "mysql" ]]; then
    "${jq_binary}" -n --argjson urls "$(controller_urls_json)" --arg cluster_id "$(state_cluster_id)" --arg instance_id "$(instance_id_for "${host}")" --arg vip "${vip}" --arg interface "${vip_interface}" --argjson prefix "${vip_prefix}" --argjson port "${database_port}" '{
      shared_secret_env:"CG_AGENT_SHARED_SECRET",controller_urls:$urls,controller_ca_file:"/etc/clusterguard/tls/ca.crt",reconcile_timeout_seconds:5,ip_binary:"/sbin/ip",arping_binary:"/usr/sbin/arping",mysql_binary:("/opt/clusterguard/mysql/"+($port|tostring)+"/software/bin/mysql"),role_state_directory:"/var/lib/clusterguard-agent/roles",decision_state_directory:"/var/lib/clusterguard-agent/decisions",mutation_state_directory:"/var/lib/clusterguard-agent/mutations",clusters:[{cluster_id:$cluster_id,instance_id:$instance_id,engine:"mysql",vip:$vip,interface:$interface,prefix:$prefix,mysql_port:$port,mysql_binary:("/opt/clusterguard/mysql/"+($port|tostring)+"/software/bin/mysql"),mysql_defaults_file:("/etc/clusterguard/mysql/"+($port|tostring)+"-client.cnf"),mysql_service:("clusterguard-mysql-"+($port|tostring)+".service"),mysql_server_binary:("/opt/clusterguard/mysql/"+($port|tostring)+"/software/bin/mysqld"),mysql_server_defaults_file:("/etc/clusterguard/mysql/"+($port|tostring)+".cnf")}]
    }' >"${target}"
  else
    "${jq_binary}" -n --argjson urls "$(controller_urls_json)" --arg cluster_id "$(state_cluster_id)" --arg instance_id "$(instance_id_for "${host}")" --arg node_id "$(node_id_for "${host}")" --arg hostname "$(node_hostname_for "${host}")" --arg vip "${vip}" --arg interface "${vip_interface}" --arg data_directory "${database_data_root}/postgresql/${database_port}/data" --argjson peers "$(postgresql_peers_json "${host}")" --argjson prefix "${vip_prefix}" --argjson port "${database_port}" '{
      shared_secret_env:"CG_AGENT_SHARED_SECRET",controller_urls:$urls,controller_ca_file:"/etc/clusterguard/tls/ca.crt",reconcile_timeout_seconds:5,ip_binary:"/sbin/ip",arping_binary:"/usr/sbin/arping",mysql_binary:"/usr/bin/false",role_state_directory:"/var/lib/clusterguard-agent/roles",decision_state_directory:"/var/lib/clusterguard-agent/decisions",mutation_state_directory:"/var/lib/clusterguard-agent/mutations",clusters:[{cluster_id:$cluster_id,instance_id:$instance_id,engine:"postgresql",postgresql_node_id:$node_id,postgresql_hostname:$hostname,vip:$vip,interface:$interface,prefix:$prefix,postgresql_port:$port,postgresql_service:("clusterguard-postgresql-"+($port|tostring)+".service"),postgresql_user:"postgres",postgresql_data_directory:$data_directory,postgresql_binary_directory:("/opt/clusterguard/postgresql/"+($port|tostring)+"/software/bin"),postgresql_passfile:("/etc/clusterguard/postgresql/"+($port|tostring)+".pass"),postgresql_database:"postgres",postgresql_replication_user:"clusterguard_repl",postgresql_peers:$peers}]
    }' >"${target}"
  fi
  chmod 0600 "${target}"
}

copy_and_configure_node() {
  local host="$1" role="$2" config_file="$3" agent_file="$4" assets="$5" env_file="$6" bundle remote_bundle configure_args
  bundle="$(mktemp "${work_dir}/node-bundle.XXXXXX.tar")"
  local stage_dir
  stage_dir="$(mktemp -d "${work_dir}/node-stage.XXXXXX")"
  [[ -z "${config_file}" ]] || install -m 0600 "${config_file}" "${stage_dir}/clusterguard.json"
  [[ -z "${agent_file}" ]] || install -m 0600 "${agent_file}" "${stage_dir}/agent.json"
  install -m 0600 "${env_file}" "${stage_dir}/clusterguard.env"
  cp -R "${assets}" "${stage_dir}/assets"
  tar -C "${stage_dir}" -cf "${bundle}" .
  rm -rf "${stage_dir}"
  remote_bundle="${remote_stage}/node-bundle.tar"
  remote_copy "${bundle}" "${host}" "${remote_bundle}"
  rm -f "${bundle}"
  remote_exec "${host}" "rm -rf '${remote_stage}/node-input' && install -d -m 0700 '${remote_stage}/node-input' && tar -xf '${remote_bundle}' -C '${remote_stage}/node-input' && chmod 0600 '${remote_stage}/node-input/clusterguard.env'"
  if [[ "${controller_data_root}" != "/var/lib/clusterguard" && ( "${role}" == "controller" || "${role}" == "mixed" ) ]]; then
    remote_exec "${host}" "install -d -o clusterguard -g clusterguard -m 0750 '${controller_data_root}' /etc/systemd/system/clusterguard-ha.service.d && printf '[Service]\\nReadWritePaths=${controller_data_root}\\n' >/etc/systemd/system/clusterguard-ha.service.d/20-site-data.conf"
  fi
  configure_args="--role '${role}' --node-name '$(node_name_for "${host}")' --node-id '$(node_id_for "${host}")' --env-file '${remote_stage}/node-input/clusterguard.env' --assets-dir '${remote_stage}/node-input/assets'"
  [[ -z "${config_file}" ]] || configure_args+=" --config '${remote_stage}/node-input/clusterguard.json'"
  [[ -z "${agent_file}" ]] || configure_args+=" --agent-config '${remote_stage}/node-input/agent.json'"
  remote_exec "${host}" "/usr/local/sbin/clusterguard-configure ${configure_args} --execute"
}

configure_controllers_first_pass() {
  local host index=0 controller_env="${work_dir}/controller.env"
  install -m 0600 "${secrets_file}" "${controller_env}"
  for host in "${controller_nodes[@]}"; do
    generate_controller_config "${host}" "$([[ ${index} -eq 0 ]] && echo true || echo false)"
    log "启动控制节点：${host}"
    copy_and_configure_node "${host}" controller "${work_dir}/nodes/${host}/clusterguard.json" "" "${work_dir}/nodes/${host}/assets" "${controller_env}" >/dev/null
    index=$((index + 1))
  done
}

controller_host_is_trusted() {
  local expected candidate="$1"
  for expected in "${controller_nodes[@]}"; do
    [[ "${candidate}" == "${expected}" ]] && return 0
  done
  return 1
}

leader_host_from_response_headers() {
  local header_file="$1" leader_api leader_raft authority leader_api_host leader_api_port leader_raft_host
  leader_api="$(awk 'tolower($0) ~ /^x-clusterguard-leader-api-address:/ { sub(/^[^:]+:[[:space:]]*/, ""); sub(/\r$/, ""); value=$0 } END { print value }' "${header_file}")"
  leader_raft="$(awk 'tolower($0) ~ /^x-clusterguard-leader-address:/ { sub(/^[^:]+:[[:space:]]*/, ""); sub(/\r$/, ""); value=$0 } END { print value }' "${header_file}")"
  [[ "${leader_api}" == https://* && -n "${leader_raft}" ]] || return 1
  authority="${leader_api#https://}"
  authority="${authority%/}"
  [[ "${authority}" != */* && "${authority}" == *:* ]] || return 1
  leader_api_host="${authority%:*}"
  leader_api_host="${leader_api_host#[}"
  leader_api_host="${leader_api_host%]}"
  leader_api_port="${authority##*:}"
  leader_raft_host="${leader_raft%:*}"
  leader_raft_host="${leader_raft_host#[}"
  leader_raft_host="${leader_raft_host%]}"
  [[ "${leader_api_port}" == "${api_port}" && "${leader_api_host}" == "${leader_raft_host}" ]] || return 1
  controller_host_is_trusted "${leader_api_host}" || return 1
  printf '%s' "${leader_api_host}"
}

api_request() {
  local method="$1" path="$2" body="${3:-}" ca="${work_dir}/pki-source/api-ca.crt" token
  local attempts="${CG_INSTALL_API_RETRY_ATTEMPTS:-4}" attempt target_host http_status suggested_host="" curl_error=""
  local body_file header_file error_file
  [[ "${attempts}" =~ ^[0-9]+$ ]] && (( attempts >= 1 && attempts <= 10 )) || die "安装器 API 重试次数必须在 1 到 10 之间"
  token="$(read_env_value CG_CONTROL_TOKEN "${secrets_file}")"
  body_file="$(mktemp "${work_dir}/api-body.XXXXXX")"
  header_file="$(mktemp "${work_dir}/api-headers.XXXXXX")"
  error_file="$(mktemp "${work_dir}/api-error.XXXXXX")"
  for attempt in $(seq 1 "${attempts}"); do
    target_host="${api_controller_host:-${controller_nodes[0]}}"
    : >"${body_file}"
    : >"${header_file}"
    : >"${error_file}"
    local -a arguments=(--silent --show-error --cacert "${ca}" --connect-timeout 5 --max-time 30 -X "${method}" -H "Authorization: Bearer ${token}" -H 'Content-Type: application/json' -D "${header_file}" -o "${body_file}" -w '%{http_code}')
    [[ -z "${body}" ]] || arguments+=(-d "${body}")
    if http_status="$(curl "${arguments[@]}" "https://${target_host}:${api_port}${path}" 2>"${error_file}")"; then
      if [[ "${http_status}" =~ ^2[0-9][0-9]$ ]]; then
        cat "${body_file}"
        rm -f "${body_file}" "${header_file}" "${error_file}"
        return 0
      fi
      if [[ "${http_status}" == "503" ]]; then
        suggested_host="$(leader_host_from_response_headers "${header_file}" 2>/dev/null || true)"
        if [[ -n "${suggested_host}" ]]; then
          api_controller_host="${suggested_host}"
        else
          api_controller_host=""
          wait_control_plane >/dev/null
        fi
      elif [[ "${method}" == "GET" && ( "${http_status}" == "502" || "${http_status}" == "504" ) ]]; then
        api_controller_host=""
        wait_control_plane >/dev/null
      else
        cat "${body_file}" >&2
        printf 'ClusterGuard API request failed: method=%s path=%s status=%s\n' "${method}" "${path}" "${http_status}" >&2
        rm -f "${body_file}" "${header_file}" "${error_file}"
        return 22
      fi
    else
      curl_error="$(<"${error_file}")"
      if [[ "${method}" != "GET" ]]; then
        printf '%s\n' "${curl_error}" >&2
        printf 'ClusterGuard API transport failed without retrying mutation: method=%s path=%s\n' "${method}" "${path}" >&2
        rm -f "${body_file}" "${header_file}" "${error_file}"
        return 1
      fi
      api_controller_host=""
      wait_control_plane >/dev/null
    fi
    (( attempt < attempts )) && sleep 1
  done
  cat "${body_file}" >&2
  [[ -z "${curl_error}" ]] || printf '%s\n' "${curl_error}" >&2
  printf 'ClusterGuard API request exhausted leader retries: method=%s path=%s attempts=%s\n' "${method}" "${path}" "${attempts}" >&2
  rm -f "${body_file}" "${header_file}" "${error_file}"
  return 1
}

wait_control_plane() {
  local attempt host token response leader_id="" observed_leader="" stable=0
  token="$(read_env_value CG_CONTROL_TOKEN "${secrets_file}")"
  for attempt in $(seq 1 120); do
    observed_leader=""
    for host in "${controller_nodes[@]}"; do
      if response="$(curl --silent --show-error --fail --cacert "${work_dir}/pki-source/api-ca.crt" \
        -H "Authorization: Bearer ${token}" "https://${host}:${api_port}/api/v1/control-plane/status" 2>/dev/null)" &&
        "${jq_binary}" -e --argjson voters "${#controller_nodes[@]}" '
          .status == "ok" and
          .result.role == "leader" and
          .result.leader_known == true and
          .result.voter_count == $voters and
          .result.quorum_confirmed == true and
          .result.mutation_authority == true and
          .result.ready == true and
          .result.commit_index == .result.applied_index
        ' <<<"${response}" >/dev/null 2>&1; then
        observed_leader="$("${jq_binary}" -r '.result.leader_id' <<<"${response}")"
        api_controller_host="${host}"
        break
      fi
    done
    if [[ -n "${observed_leader}" ]]; then
      if [[ "${observed_leader}" == "${leader_id}" ]]; then
        stable=$((stable + 1))
      else
        leader_id="${observed_leader}"
        stable=1
      fi
      (( stable >= 2 )) && return 0
    else
      leader_id=""
      stable=0
    fi
    sleep 2
  done
  die "控制面在 240 秒内未达到 Leader + quorum 就绪状态"
}

register_node_inventory() {
  local host resource_id role payload inventory
  inventory="$(api_request GET /api/v1/nodes)"
  for host in "${all_nodes[@]}"; do
    resource_id="$(node_id_for "${host}")"
    role="$(node_role_for "${host}")"
    payload="$("${jq_binary}" -nc \
      --arg resource_id "${resource_id}" \
      --arg node_name "$(node_name_for "${host}")" \
      --arg hostname "$(node_hostname_for "${host}")" \
      --arg ip_address "${host}" \
      --arg kind "${role}" \
      '{resource_id:$resource_id,node_name:$node_name,hostname:$hostname,ip_address:$ip_address,kind:$kind,active:true}')"
    if "${jq_binary}" -e --arg id "${resource_id}" '.result[]? | select(.resource_id==$id)' <<<"${inventory}" >/dev/null; then
      api_request PUT "/api/v1/nodes/${resource_id}" "${payload}" >/dev/null
    else
      api_request POST /api/v1/nodes "${payload}" >/dev/null
    fi
  done
}

register_and_discover_cluster() {
  [[ "${database_engine}" == "none" ]] && return
  local cluster_id endpoints='[]' host payload clusters response topology primary_id primary_count discovered_id
  cluster_id="$(state_cluster_id)"
  for host in "${data_nodes[@]}"; do
    endpoints="$("${jq_binary}" -nc --argjson current "${endpoints}" --arg hostname "$(node_hostname_for "${host}")" --arg ip "${host}" --argjson port "${database_port}" '$current+[{hostname:$hostname,ip_address:$ip,port:$port}]')"
  done
  clusters="$(api_request GET /api/v1/clusters)"
  if ! "${jq_binary}" -e --arg id "${cluster_id}" '.result[]? | select(.resource_id==$id)' <<<"${clusters}" >/dev/null; then
    payload="$("${jq_binary}" -nc --arg resource_id "${cluster_id}" --arg display_name "${cluster_name}" --arg engine "${database_engine}" --argjson endpoints "${endpoints}" '{resource_id:$resource_id,display_name:$display_name,engine:$engine,endpoints:$endpoints}')"
    api_request POST /api/v1/clusters "${payload}" >/dev/null
  fi
  response="$(api_request POST "/api/v1/clusters/${cluster_id}/discover" '{}')"
  topology="$("${jq_binary}" -c '.result' <<<"${response}")"
  for host in "${data_nodes[@]}"; do
    discovered_id="$("${jq_binary}" -r --arg ip "${host}" --arg hostname "$(node_hostname_for "${host}")" --argjson port "${database_port}" 'first(.instances[] | select((.ip_address==$ip or .hostname==$hostname) and .port==$port) | .resource_id) // empty' <<<"${topology}")"
    [[ -n "${discovered_id}" ]] || die "发现结果缺少 ${host}:${database_port} 的不可变实例身份"
    set_instance_id "${host}" "${discovered_id}"
    store_node_state "${host}" "$(node_role_for "${host}")" "$(node_hostname_for "${host}")" "$(node_id_for "${host}")" "$(node_name_for "${host}")" "${discovered_id}"
  done
  primary_count="$("${jq_binary}" '[.instances[] | select(.role=="primary")] | length' <<<"${topology}")"
  [[ "${primary_count}" == "1" ]] || die "发现结果的主库数量必须为 1，当前为 ${primary_count}"
  primary_id="$("${jq_binary}" -r 'first(.instances[] | select(.role=="primary") | .resource_id) // empty' <<<"${topology}")"
  [[ -n "${primary_id}" ]] || die "发现结果没有可识别主库"
  if [[ -n "${vip}" ]]; then
    local existing_ha
    existing_ha="$(api_request GET "/api/v1/clusters/${cluster_id}/ha-endpoints")"
    if ! "${jq_binary}" -e '.result[]? | select(.resource.kind=="vip" and .resource.active==true)' <<<"${existing_ha}" >/dev/null; then
      payload="$("${jq_binary}" -nc --arg ip "${vip}" --arg interface "${vip_interface}" --arg owner "${primary_id}" --argjson prefix "${vip_prefix}" '{kind:"vip",ip_address:$ip,interface:$interface,prefix:$prefix,owner_id:$owner,active:true}')"
      api_request POST "/api/v1/clusters/${cluster_id}/ha-endpoints" "${payload}" >/dev/null
    fi
  fi
  set_state_phase registered
}

configure_platform_updates() {
  if [[ -z "${patch_trust_key}" ]]; then
    warn "未提供补丁签名公钥；控制台版本更新入口将保持只读不可用"
    return 0
  fi
  local update_config="${work_dir}/update.json" host controllers_json='[]' data_nodes_json='[]'
  for host in "${controller_nodes[@]}"; do
    controllers_json="$("${jq_binary}" -nc --argjson current "${controllers_json}" --arg host "${host}" '$current + [$host]')"
  done
  for host in "${data_nodes[@]}"; do
    data_nodes_json="$("${jq_binary}" -nc --argjson current "${data_nodes_json}" --arg host "${host}" '$current + [$host]')"
  done
  "${jq_binary}" -n \
    --argjson controllers "${controllers_json}" \
    --argjson data_nodes "${data_nodes_json}" \
    --argjson ssh_port "${ssh_port}" \
    --argjson api_port "${api_port}" \
    --argjson retained_versions 3 \
    --arg ssh_user "${ssh_user}" \
    '{trust_key:"/etc/clusterguard/trust/patch-signing-public.pem",deployment_state:"/etc/clusterguard/deployment-state.json",ssh_user:$ssh_user,ssh_key:"/etc/clusterguard/ssh/controller_ed25519",known_hosts:"/etc/clusterguard/ssh/controller_known_hosts",ssh_port:$ssh_port,api_port:$api_port,retained_versions:$retained_versions,controllers:$controllers,data_nodes:$data_nodes}' \
    >"${update_config}"
  chmod 0600 "${update_config}"
  for host in "${controller_nodes[@]}"; do
    log "配置图形化滚动升级 Helper：${host}"
    remote_copy "${patch_trust_key}" "${host}" "${remote_stage}/patch-signing-public.pem"
    remote_copy "${state_file}" "${host}" "${remote_stage}/deployment-state.json"
    remote_copy "${update_config}" "${host}" "${remote_stage}/update.json"
    remote_exec_checked "${host}" "配置软件更新可信链与清单" "
set -euo pipefail
install -d -m 0750 -o root -g clusterguard /etc/clusterguard/trust
install -d -m 0750 -o clusterguard -g clusterguard /var/lib/clusterguard/updates
install -d -m 0700 -o root -g root /var/lib/clusterguard-update-private
install -m 0640 -o root -g clusterguard '${remote_stage}/patch-signing-public.pem' /etc/clusterguard/trust/patch-signing-public.pem
install -m 0600 -o root -g root '${remote_stage}/deployment-state.json' /etc/clusterguard/deployment-state.json
install -m 0600 -o root -g root '${remote_stage}/update.json' /etc/clusterguard/update.json
systemctl daemon-reload
systemctl enable --now clusterguard-update-helper.service
systemctl is-active --quiet clusterguard-update-helper.service
test -S /run/clusterguard/update-helper.sock
" >/dev/null
  done
}

configure_data_agents() {
  [[ "${database_engine}" == "none" ]] && return
  local host role config agent assets env_file controller_env="${work_dir}/controller.env" data_env="${work_dir}/data-agent.env"
  printf 'CG_AGENT_SHARED_SECRET=%s\n' "$(read_env_value CG_AGENT_SHARED_SECRET "${secrets_file}")" >"${data_env}"
  chmod 0600 "${data_env}"
  for host in "${data_nodes[@]}"; do
    generate_agent_config "${host}"
    role="$(node_role_for "${host}")"
    agent="${work_dir}/nodes/${host}/agent.json"
    if [[ "${role}" == "mixed" ]]; then
      config="${work_dir}/nodes/${host}/clusterguard.json"
      assets="${work_dir}/nodes/${host}/assets"
      env_file="${controller_env}"
    else
      config=""
      assets="${work_dir}/nodes/${host}/data-assets"
      env_file="${data_env}"
    fi
    log "登记数据节点 Agent：${host}"
    copy_and_configure_node "${host}" "${role}" "${config}" "${agent}" "${assets}" "${env_file}" >/dev/null
  done
}

verify_data_agent_readiness() {
  [[ "${database_engine}" == "none" ]] && return
  local host database_service
  case "${database_engine}" in
    mysql) database_service="clusterguard-mysql-${database_port}.service" ;;
    postgresql) database_service="clusterguard-postgresql-${database_port}.service" ;;
    *) die "无法验证未支持的数据库引擎 Agent 就绪状态：${database_engine}" ;;
  esac
  for host in "${data_nodes[@]}"; do
    remote_exec_checked "${host}" "验证数据节点 Agent 就绪" "
set -euo pipefail
test -s /etc/clusterguard/agent.json
systemctl is-enabled --quiet clusterguard-agent.service
systemctl is-active --quiet clusterguard-agent.service
systemctl is-enabled --quiet ${database_service}
systemctl is-active --quiet ${database_service}
"
  done
}

data_agents_remain_ready() {
  [[ "${database_engine}" == "none" ]] && return 0
  local host database_service
  case "${database_engine}" in
    mysql) database_service="clusterguard-mysql-${database_port}.service" ;;
    postgresql) database_service="clusterguard-postgresql-${database_port}.service" ;;
    *) return 1 ;;
  esac
  for host in "${data_nodes[@]}"; do
    remote_exec "${host}" "
test -s /etc/clusterguard/agent.json &&
systemctl is-active --quiet clusterguard-agent.service &&
systemctl is-active --quiet ${database_service}
" >/dev/null 2>&1 || return 1
  done
}

diagnose_vip_lease_readiness() {
  local host output status database_service database_config managed_data
  case "${database_engine}" in
    mysql)
      database_service="clusterguard-mysql-${database_port}.service"
      database_config="/etc/clusterguard/mysql/${database_port}-client.cnf"
      managed_data="${database_data_root}/mysql/${database_port}/.clusterguard-managed"
      ;;
    postgresql)
      database_service="clusterguard-postgresql-${database_port}.service"
      database_config="/etc/clusterguard/postgresql/${database_port}.pass"
      managed_data="${database_data_root}/postgresql/${database_port}/data/PG_VERSION"
      ;;
    *) return ;;
  esac
  printf 'VIP 租约启动诊断：\n' >&2
  for host in "${data_nodes[@]}"; do
    set +e
    output="$(remote_exec "${host}" "
printf 'agent_config='; test -s /etc/clusterguard/agent.json && printf present || printf missing
printf ' agent='; systemctl is-active clusterguard-agent.service 2>/dev/null || true
printf ' database='; systemctl is-active '${database_service}' 2>/dev/null || true
printf ' database_config='; test -s '${database_config}' && printf present || printf missing
printf ' managed_data='; test -f '${managed_data}' && printf present || printf missing
")"
    status=$?
    set -e
    if [[ "${status}" -ne 0 ]]; then
      printf '  %s: unreachable or SSH command failed (exit=%s)\n' "${host}" "${status}" >&2
    else
      printf '  %s: %s\n' "${host}" "${output//$'\n'/ }" >&2
    fi
  done
}

wait_for_initial_vip_ownership_lease() {
  [[ -n "${vip}" && "${activate_vip_reconcile}" == "true" ]] || return
  local cluster_id="$1" primary_id="$2"
  local timeout_seconds="${CG_INSTALL_VIP_LEASE_TIMEOUT_SECONDS:-300}"
  local deadline ownership="" last_summary="VIP ownership lease has not been observed" next_discovery=0 next_node_check=0
  [[ "${timeout_seconds}" =~ ^[0-9]+$ ]] || die "VIP 租约等待超时必须为整数"
  (( timeout_seconds >= 30 && timeout_seconds <= 600 )) || die "VIP 租约等待超时必须在 30 到 600 秒之间"
  deadline=$((SECONDS + timeout_seconds))
  while (( SECONDS < deadline )); do
    if (( SECONDS >= next_node_check )); then
      if ! data_agents_remain_ready; then
        diagnose_vip_lease_readiness
        die "VIP 自动收敛未启动：等待初始所有权租约期间发现数据节点服务或 Agent 不完整；部署已停止，禁止在不完整覆盖下分配 VIP"
      fi
      next_node_check=$((SECONDS + 5))
    fi
    # The first controller and agent start concurrently. Refresh discovery while
    # waiting so the lease keeper always evaluates a current primary snapshot.
    if (( SECONDS >= next_discovery )); then
      api_request POST "/api/v1/clusters/${cluster_id}/discover" '{}' >/dev/null 2>&1 || true
      next_discovery=$((SECONDS + 10))
    fi
    if ownership="$(api_request GET "/api/v1/clusters/${cluster_id}/ha-ownership" 2>/dev/null)"; then
      last_summary="$("${jq_binary}" -r '.message // "VIP ownership lease is not active"' <<<"${ownership}" 2>/dev/null || true)"
      if "${jq_binary}" -e --arg primary "${primary_id}" '
        .result.resource.owner_id == $primary and
        .result.endpoint.instance_id == $primary and
        .result.active_lease != null and
        .result.active_lease.active == true and
        .result.active_lease.owner_id == $primary and
        .result.active_lease.operation_id == .result.resource.resource_id
      ' <<<"${ownership}" >/dev/null 2>&1; then
        return 0
      fi
    fi
    sleep 2
  done
  diagnose_vip_lease_readiness
  die "VIP 自动收敛尚未启用：控制面在 ${timeout_seconds} 秒内未建立主库初始所有权租约（${last_summary}）；上方诊断列出了每个节点的 Agent、MySQL 服务和受管数据状态"
}

activate_vip_reconciliation() {
  [[ -n "${vip}" && "${activate_vip_reconcile}" == "true" ]] || return
  local cluster_id="$1" primary_id="$2" host
  wait_for_initial_vip_ownership_lease "${cluster_id}" "${primary_id}"
  for host in "${data_nodes[@]}"; do
    log "启用 VIP 自动收敛：${host}"
    remote_exec "${host}" "systemctl enable --now clusterguard-agent-reconcile.timer" >/dev/null
  done
}

wait_for_stable_database_topology() {
  local cluster_id="$1" expected_instances="$2"
  local timeout_seconds="${CG_INSTALL_VERIFY_TIMEOUT_SECONDS:-180}"
  local poll_seconds="${CG_INSTALL_VERIFY_POLL_SECONDS:-2}"
  local stable_required="${CG_INSTALL_VERIFY_STABLE_OBSERVATIONS:-3}"
  local deadline stable=0 topology="" last_summary="topology has not been observed"
  [[ "${timeout_seconds}" =~ ^[0-9]+$ && "${poll_seconds}" =~ ^[0-9]+$ && "${stable_required}" =~ ^[0-9]+$ ]] ||
    die "安装验收等待参数必须为整数"
  (( timeout_seconds >= 30 && timeout_seconds <= 900 )) || die "安装验收超时必须在 30 到 900 秒之间"
  (( poll_seconds >= 1 && poll_seconds <= 30 )) || die "安装验收轮询间隔必须在 1 到 30 秒之间"
  (( stable_required >= 2 && stable_required <= 10 )) || die "安装验收连续健康次数必须在 2 到 10 之间"
  deadline=$((SECONDS + timeout_seconds))
  while (( SECONDS < deadline )); do
    if topology="$(api_request GET "/api/v1/clusters/${cluster_id}/topology" 2>/dev/null)"; then
      last_summary="$("${jq_binary}" -r '.result.health.summary // .message // "topology is incomplete"' <<<"${topology}" 2>/dev/null || true)"
      if "${jq_binary}" -e --argjson count "${expected_instances}" '
        (.result.instances | length) == $count and
        ([.result.instances[] | select(.health.state == "healthy")] | length) == $count and
        ([.result.instances[] | select(.role == "primary")] | length) == 1 and
        (.result.health.state == "healthy")
      ' <<<"${topology}" >/dev/null 2>&1; then
        stable=$((stable + 1))
        if (( stable >= stable_required )); then
          printf '%s' "${topology}"
          return 0
        fi
      else
        stable=0
      fi
    else
      stable=0
    fi
    sleep "${poll_seconds}"
  done
  die "数据库拓扑未在 ${timeout_seconds} 秒内连续 ${stable_required} 次达到完整健康状态：${last_summary}"
}

verify_installation() {
  local host cluster_id topology primary_id vip_owners=0
  cluster_id="$(state_cluster_id)"
  api_request GET /api/v1/control-plane/status | "${jq_binary}" -e '.status=="ok" and .result.quorum_confirmed==true and .result.ready==true' >/dev/null
  if [[ "${database_engine}" != "none" ]]; then
    topology="$(wait_for_stable_database_topology "${cluster_id}" "${#data_nodes[@]}")"
    primary_id="$("${jq_binary}" -r 'first(.result.instances[] | select(.role == "primary") | .resource_id) // empty' <<<"${topology}")"
    [[ -n "${primary_id}" ]] || die "安装验收未找到唯一主库身份"
    if [[ -n "${vip}" && "${activate_vip_reconcile}" == "true" ]]; then
      activate_vip_reconciliation "${cluster_id}" "${primary_id}"
      sleep 5
      for host in "${data_nodes[@]}"; do
        if remote_exec "${host}" "ip -o -4 addr show dev '${vip_interface}' | grep -Fq ' ${vip}/${vip_prefix} '"; then vip_owners=$((vip_owners + 1)); fi
      done
      [[ "${vip_owners}" -eq 1 ]] || die "VIP 全集群唯一性验证失败，当前 owner 数量=${vip_owners}"
    fi
    for host in "${data_nodes[@]}"; do remote_exec "${host}" "systemctl is-enabled --quiet clusterguard-agent.service && systemctl is-active --quiet clusterguard-agent.service"; done
  fi
  for host in "${controller_nodes[@]}"; do remote_exec "${host}" "systemctl is-enabled --quiet clusterguard-ha.service && systemctl is-active --quiet clusterguard-ha.service"; done
  if [[ -n "${patch_trust_key}" ]]; then
    for host in "${controller_nodes[@]}"; do remote_exec "${host}" "systemctl is-enabled --quiet clusterguard-update-helper.service && systemctl is-active --quiet clusterguard-update-helper.service && test -S /run/clusterguard/update-helper.sock"; done
  fi
  log "安装验证通过"
}

bootstrap_password_from_leader() {
  local host response password token path="${controller_data_root}/bootstrap-admin-password"
  local quoted_path
  [[ "${path}" =~ ^/[A-Za-z0-9._/-]+$ ]] || return 1
  quoted_path="'${path}'"
  token="$(read_env_value CG_CONTROL_TOKEN "${secrets_file}")"
  for host in "${controller_nodes[@]}"; do
    response="$(curl --silent --show-error --fail --cacert "${work_dir}/pki-source/api-ca.crt" \
      --connect-timeout 5 --max-time 10 -H "Authorization: Bearer ${token}" \
      "https://${host}:${api_port}/api/v1/control-plane/status" 2>/dev/null)" || continue
    "${jq_binary}" -e '.status == "ok" and .result.role == "leader" and
      .result.ready == true and .result.quorum_confirmed == true and
      .result.mutation_authority == true' <<<"${response}" >/dev/null 2>&1 || continue
    password="$(remote_exec "${host}" "test -f ${quoted_path} && test ! -L ${quoted_path} && test \"\$(stat -c '%u:%a' ${quoted_path})\" = \"\$(id -u clusterguard):600\" && cat -- ${quoted_path}" 2>/dev/null)" || return 1
    [[ "${password}" =~ ^[A-Za-z0-9_-]{32}$ ]] || return 1
    printf '%s' "${password}"
    return 0
  done
  return 1
}

print_install_completion() {
  local password="" credential_file="${controller_data_root}/bootstrap-admin-password"
  printf '\n安装完成。\n控制台：https://%s:%s/\n初始账户：admin\n' "${controller_nodes[0]}" "${api_port}"
  if [[ -t 1 ]] && password="$(bootstrap_password_from_leader)"; then
    printf '首次密码：%s\n' "${password}"
  else
    printf '首次密码：未在非交互输出中显示或无法从当前 Leader 安全读取；请在生成口令的控制节点上以 root 查看 %s。若文件不存在，请使用管理员恢复流程，切勿使用旧版固定口令。\n' "${credential_file}"
  fi
  unset password
  printf '首次登录后必须立即重设；改密前禁止查看或操作平台数据。\n部署状态：%s\n站点秘密：%s\n\n' "${state_file}" "${secrets_file}"
}

confirm_execute() {
  [[ "${assume_yes}" == "true" ]] && return
  [[ -t 0 ]] || die "非交互执行必须追加 -y/--yes"
  printf '输入集群名称 %s 确认真实安装：' "${cluster_name}"
  local confirmation
  read -r confirmation
  [[ "${confirmation}" == "${cluster_name}" ]] || die "确认内容不匹配，未执行"
}

main() {
  parse_args "$@"
  discover_bundled_dependencies
  discover_bundled_patch_trust_key
  find_jq
  validate_inputs
  print_plan
  [[ "${execute}" == "true" ]] || { log "只读计划完成，未修改任何远端节点。确认后追加 --execute。"; exit 0; }
  command -v ssh >/dev/null 2>&1 || die "缺少 ssh"
  command -v scp >/dev/null 2>&1 || die "缺少 scp"
  command -v ssh-keyscan >/dev/null 2>&1 || die "缺少 ssh-keyscan"
  command -v ssh-keygen >/dev/null 2>&1 || die "缺少 ssh-keygen"
  command -v openssl >/dev/null 2>&1 || die "缺少 openssl"
  command -v curl >/dev/null 2>&1 || die "缺少 curl"
  command -v tar >/dev/null 2>&1 || die "缺少 tar"
  prompt_for_ssh_password
  confirm_execute
  umask 077
  mkdir -p "${work_dir}"
  prepare_known_hosts
  select_ssh_auth_mode
  initialize_state_and_secrets
  discover_node_identities
  remote_preflight
  configure_fixed_cluster_clock_source
  verify_cluster_clock_skew
  reconcile_database_installation_phase
  reconcile_mysql_replication_secret_policy
  local host
  for host in "${all_nodes[@]}"; do log "安装 ClusterGuard RPM：${host}"; install_rpm_on_node "${host}"; done
  configure_cluster_firewalls
  if [[ "${database_engine}" != "none" ]]; then
    prepare_postgresql_binary_package
    distribute_database_package
    initialize_database_cluster
    install_database_client_on_controllers
  fi
  generate_pki_and_ssh
  configure_controllers_first_pass
  wait_control_plane
  register_node_inventory
  register_and_discover_cluster
  configure_platform_updates
  configure_data_agents
  verify_data_agent_readiness
  wait_control_plane
  if [[ "${database_engine}" != "none" ]]; then api_request POST "/api/v1/clusters/$(state_cluster_id)/discover" '{}' >/dev/null; fi
  verify_installation
  print_install_completion
}

if [[ "${CG_INSTALLER_LIBRARY_ONLY:-false}" != "true" ]]; then
  main "$@"
fi
