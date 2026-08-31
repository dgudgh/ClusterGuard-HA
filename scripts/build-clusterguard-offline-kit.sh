#!/usr/bin/env bash
set -euo pipefail
export COPYFILE_DISABLE=1

script_dir="$(cd "$(dirname "$0")" && pwd)"
repository="$(cd "${script_dir}/.." && pwd)"
output=""
version="2.2"
release="1"
bundle_version=""
goarch="amd64"
nfpm_binary="${CG_NFPM_BINARY:-$(command -v nfpm 2>/dev/null || true)}"
jq_binary="${CG_JQ_BINARY:-}"
dependency_dir=""
patch_trust_key=""
database_packages=()
database_package_count=0

usage() {
  cat <<'EOF'
用法：build-clusterguard-offline-kit.sh [选项]

选项：
  --output DIR          输出目录；默认写入主项目 release/VERSION-RELEASE
  --version VERSION     RPM 版本，例如 2.2
  --release RELEASE     RPM release，例如 1
  --bundle-version VER  介质显示版本，默认使用 VERSION-RELEASE
  --goarch ARCH         amd64 或 arm64
  --nfpm-binary FILE    可信 nFPM 可执行文件
  --jq-binary FILE      与目标架构一致的静态 Linux jq
  --dependencies DIR    可选，复制已签名且与目标系统匹配的依赖 RPM
  --patch-trust-key FILE
                        可选，只打包补丁签名公钥；绝不打包签名私钥
  --database-package FILE
                        把批准的数据库 tar 包直接嵌入介质；可重复指定
EOF
}

while (($#)); do
  case "$1" in
    --output) output="${2:-}"; shift 2 ;;
    --version) version="${2:-}"; shift 2 ;;
    --release) release="${2:-}"; shift 2 ;;
    --bundle-version) bundle_version="${2:-}"; shift 2 ;;
    --goarch) goarch="${2:-}"; shift 2 ;;
    --nfpm-binary) nfpm_binary="${2:-}"; shift 2 ;;
    --jq-binary) jq_binary="${2:-}"; shift 2 ;;
    --dependencies) dependency_dir="${2:-}"; shift 2 ;;
    --patch-trust-key) patch_trust_key="${2:-}"; shift 2 ;;
    --database-package)
      database_packages[${database_package_count}]="${2:-}"
      database_package_count=$((database_package_count + 1))
      shift 2
      ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数：$1" >&2; usage >&2; exit 2 ;;
  esac
done

[[ -x "${nfpm_binary}" ]] || { echo "必须提供可信 nFPM" >&2; exit 3; }
[[ -x "${jq_binary}" ]] || { echo "必须提供静态 Linux jq" >&2; exit 3; }

# Child builders enter staging directories. Normalize executable paths here so
# a caller can safely pass either an absolute path or a path relative to the
# current checkout.
canonical_executable() {
  local candidate="$1" directory
  directory="$(cd "$(dirname "${candidate}")" && pwd)"
  printf '%s/%s\n' "${directory}" "$(basename "${candidate}")"
}
nfpm_binary="$(canonical_executable "${nfpm_binary}")"
jq_binary="$(canonical_executable "${jq_binary}")"

[[ -z "${dependency_dir}" || -d "${dependency_dir}" ]] || { echo "依赖 RPM 目录不存在：${dependency_dir}" >&2; exit 3; }
if [[ -n "${patch_trust_key}" ]]; then
  [[ -f "${patch_trust_key}" && ! -L "${patch_trust_key}" ]] || { echo "补丁签名公钥无效：${patch_trust_key}" >&2; exit 3; }
  openssl pkey -pubin -in "${patch_trust_key}" -noout >/dev/null 2>&1 || { echo "补丁签名公钥格式无效：${patch_trust_key}" >&2; exit 3; }
fi

normalized_database_packages=()
normalized_database_package_count=0
for ((database_package_index=0; database_package_index<database_package_count; database_package_index++)); do
  database_package="${database_packages[${database_package_index}]}"
  [[ -n "${database_package}" ]] || { echo "--database-package 不能为空" >&2; exit 2; }
  [[ -f "${database_package}" && ! -L "${database_package}" ]] || {
    echo "数据库介质必须是普通文件且不能是符号链接：${database_package}" >&2
    exit 3
  }
  database_package_dir="$(cd "$(dirname "${database_package}")" && pwd)"
  database_package="${database_package_dir}/$(basename "${database_package}")"
  database_package_name="$(basename "${database_package}")"
  [[ "${database_package_name}" =~ ^[A-Za-z0-9._+-]+$ ]] || {
    echo "数据库介质文件名包含不支持的字符：${database_package_name}" >&2
    exit 3
  }
  case "${database_package_name}" in
    *.tar|*.tar.gz|*.tgz|*.tar.xz|*.tar.bz2|*.tbz2) ;;
    *) echo "数据库介质必须是 tar/tar.gz/tgz/tar.xz/tar.bz2/tbz2：${database_package_name}" >&2; exit 3 ;;
  esac
  for ((existing_database_package_index=0; existing_database_package_index<normalized_database_package_count; existing_database_package_index++)); do
    existing_database_package="${normalized_database_packages[${existing_database_package_index}]}"
    [[ "$(basename "${existing_database_package}")" != "${database_package_name}" ]] || {
      echo "数据库介质文件名重复：${database_package_name}" >&2
      exit 3
    }
  done
  if [[ -f "${database_package}.sha256" ]]; then
    expected_database_digest="$(awk 'NR==1 {print $1}' "${database_package}.sha256")"
    [[ "${expected_database_digest}" =~ ^[0-9a-fA-F]{64}$ ]] || {
      echo "数据库介质摘要格式无效：${database_package}.sha256" >&2
      exit 3
    }
    if command -v sha256sum >/dev/null 2>&1; then
      actual_database_digest="$(sha256sum "${database_package}" | awk '{print $1}')"
    else
      actual_database_digest="$(shasum -a 256 "${database_package}" | awk '{print $1}')"
    fi
    [[ "${actual_database_digest}" == "${expected_database_digest}" ]] || {
      echo "数据库介质摘要不匹配：${database_package}" >&2
      exit 3
    }
  fi
  normalized_database_packages[${normalized_database_package_count}]="${database_package}"
  normalized_database_package_count=$((normalized_database_package_count + 1))
done
database_packages=()
for ((database_package_index=0; database_package_index<normalized_database_package_count; database_package_index++)); do
  database_packages[${database_package_index}]="${normalized_database_packages[${database_package_index}]}"
done
database_package_count=${normalized_database_package_count}

case "${goarch}" in
  amd64) media_arch="x86_64" ;;
  arm64) media_arch="aarch64" ;;
  *) echo "架构必须是 amd64 或 arm64" >&2; exit 2 ;;
esac

# The main kit carries only the small control-plane/MySQL runtime repository.
# PostgreSQL development dependencies are intentionally shipped as a separate
# optional add-on because their transitive closure is much larger.
if [[ -z "${dependency_dir}" ]]; then
  bundled_dependency_dir="${repository}/packaging/offline-dependencies/rocky-8-${media_arch}-runtime"
  if find "${bundled_dependency_dir}" -maxdepth 1 -type f -name '*.rpm' -print -quit 2>/dev/null | grep -q .; then
    dependency_dir="${bundled_dependency_dir}"
    printf '自动使用内置 Rocky 8 基础运行依赖：%s\n' "${dependency_dir}"
  fi
fi

verify_dependency_directory() {
  local directory="$1" package
  local required_runtime_dependencies=(libaio ncurses-compat-libs numactl-libs)

  if find "${directory}" -maxdepth 1 -type f -name '*.i686.rpm' -print -quit | grep -q .; then
    echo "依赖目录包含 i686 RPM，禁止混入 ${media_arch} 正式介质" >&2
    exit 3
  fi
  if [[ -f "${directory}/SHA256SUMS" ]]; then
    if command -v sha256sum >/dev/null 2>&1; then
      (cd "${directory}" && sha256sum -c SHA256SUMS >/dev/null) || {
        echo "依赖目录 SHA256SUMS 校验失败" >&2
        exit 3
      }
    else
      (cd "${directory}" && shasum -a 256 -c SHA256SUMS >/dev/null) || {
        echo "依赖目录 SHA256SUMS 校验失败" >&2
        exit 3
      }
    fi
  fi
  [[ -f "${directory}/repodata/repomd.xml" ]] || {
    echo "正式离线介质缺少本地仓库元数据：repodata/repomd.xml" >&2
    exit 3
  }
  if [[ "${media_arch}" != "x86_64" ]]; then
    return
  fi
  for package in "${required_runtime_dependencies[@]}"; do
    if [[ -f "${directory}/PACKAGE-MANIFEST.txt" ]]; then
      grep -q "^${package}[[:space:]]" "${directory}/PACKAGE-MANIFEST.txt" || {
        echo "正式离线介质缺少基础运行依赖：${package}" >&2
        exit 3
      }
    elif ! find "${directory}" -maxdepth 1 -type f -name "${package}-*.rpm" -print -quit | grep -q .; then
      echo "正式离线介质缺少基础运行依赖：${package}" >&2
      exit 3
    fi
  done
}

if [[ -n "${dependency_dir}" ]]; then
  verify_dependency_directory "${dependency_dir}"
fi
if [[ -z "${bundle_version}" ]]; then
  bundle_version="${version}-${release}"
fi
[[ "${bundle_version}" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "介质版本格式无效" >&2; exit 2; }

if [[ -z "${output}" ]]; then
  # A build may run from a linked worktree. Formal deliverables belong to the
  # visible primary repository, never under .worktrees or a temporary dist.
  git_common_dir="$(git -C "${repository}" rev-parse --git-common-dir 2>/dev/null || true)"
  if [[ -n "${git_common_dir}" ]]; then
    [[ "${git_common_dir}" == /* ]] || git_common_dir="${repository}/${git_common_dir}"
    release_repository="$(cd "$(dirname "${git_common_dir}")" && pwd)"
  else
    release_repository="${repository}"
  fi
  output="${release_repository}/release/${bundle_version}"
fi

stage="$(mktemp -d /tmp/clusterguard-offline-kit.XXXXXX)"
trap 'rm -rf "${stage}"' EXIT
artifacts="${stage}/artifacts"
kit_name="clusterguard-ha-${bundle_version}-offline-linux-${media_arch}"
kit="${stage}/${kit_name}"
mkdir -p \
  "${artifacts}" \
  "${kit}/packages/database" \
  "${kit}/dependencies" \
  "${kit}/docs" \
  "${kit}/tools" \
  "${kit}/trust" \
  "${kit}/examples/fencing" \
  "${kit}/examples/docker-swarm/mysql" \
  "${kit}/examples/docker-swarm/postgresql" \
  "${output}"

"${script_dir}/build-clusterguard-bundle.sh" \
  --output "${artifacts}" \
  --version "${bundle_version}" \
  --goos linux \
  --goarch "${goarch}" \
  --jq-binary "${jq_binary}"

"${script_dir}/build-clusterguard-rpm.sh" \
  --output "${artifacts}" \
  --version "${version}" \
  --release "${release}" \
  --goarch "${goarch}" \
  --nfpm-binary "${nfpm_binary}" \
  --jq-binary "${jq_binary}"

install -m 0644 "${artifacts}"/*.rpm "${kit}/packages/"
install -m 0644 "${artifacts}"/*.rpm.sha256 "${kit}/packages/"
install -m 0644 "${artifacts}"/*-linux-"${goarch}".tar.gz "${kit}/packages/"
install -m 0755 "${script_dir}/install_clusterguard.sh" "${kit}/install_clusterguard.sh"
install -m 0755 "${jq_binary}" "${kit}/tools/jq-linux-amd64"
install -m 0755 "${script_dir}/clusterguard-clock-mesh.sh" "${kit}/tools/clusterguard-clock-mesh.sh"
install -m 0755 "${script_dir}/clusterguard-postgresql-build.sh" "${kit}/tools/编译PostgreSQL源码.sh"
install -m 0644 "${repository}/docs/zh-CN/offline-rpm-install.md" "${kit}/docs/ClusterGuard-HA-离线安装与部署手册.md"
install -m 0644 "${repository}/docs/zh-CN/database-preparation.md" "${kit}/docs/数据库接入手册.md"
install -m 0644 "${repository}/docs/zh-CN/operations-manual.md" "${kit}/docs/运维操作手册.md"
install -m 0644 "${repository}/docs/zh-CN/update-and-patch.md" "${kit}/docs/版本升级与回退手册.md"
install -m 0644 "${repository}/docs/zh-CN/docker-swarm-mysql.md" "${kit}/docs/Docker-Swarm-MySQL.md"
install -m 0644 "${repository}/docs/zh-CN/postgresql-ha.md" "${kit}/docs/PostgreSQL-高可用手册.md"
install -m 0644 "${repository}/docs/zh-CN/postgresql-production-qualification-2026-08-23.md" "${kit}/docs/PostgreSQL-生产验收报告.md"
release_notes="${repository}/docs/zh-CN/release-${version}.${release}.md"
if [[ -f "${release_notes}" ]]; then
  install -m 0644 "${release_notes}" "${kit}/docs/ClusterGuard-HA-${version}.${release}-发布说明.md"
fi
install -m 0755 "${script_dir}/clusterguard-upgrade.sh" "${kit}/tools/clusterguard-upgrade.sh"
install -m 0755 "${script_dir}/clusterguard-update-prune.sh" "${kit}/tools/clusterguard-update-prune.sh"
install -m 0755 "${script_dir}/clusterguard-offline-deps.sh" "${kit}/tools/收集RHEL离线依赖.sh"
install -m 0755 "${script_dir}/build-clusterguard-postgresql-deps-pack.sh" "${kit}/tools/构建PostgreSQL依赖包.sh"
install -m 0755 "${script_dir}/fencing/vmware-workstation-ssh-fencer" "${kit}/examples/fencing/vmware-workstation-ssh-fencer"
install -m 0644 "${repository}/configs/fencing/vmware-workstation.conf.example" "${kit}/examples/fencing/vmware-workstation.conf.example"
install -m 0644 "${repository}/configs/fencing/vmware-targets.tsv.example" "${kit}/examples/fencing/vmware-targets.tsv.example"
install -m 0644 "${repository}/configs/clusterguard.example.json" "${kit}/clusterguard.json.example"
install -m 0644 "${repository}/configs/clusterguard-agent.example.json" "${kit}/agent.json.example"
install -m 0644 "${repository}/configs/clusterguard-agent.docker-swarm.example.json" "${kit}/agent.docker-swarm.json.example"
install -m 0644 "${repository}/configs/clusterguard-update.example.json" "${kit}/update.json.example"
install -m 0755 "${repository}/deploy/docker-swarm/install-docker-static.sh" "${kit}/examples/docker-swarm/"
install -m 0755 "${repository}/deploy/docker-swarm/mysql/prepare-host.sh" "${kit}/examples/docker-swarm/mysql/"
install -m 0755 "${repository}/deploy/docker-swarm/mysql/install-mysql-client.sh" "${kit}/examples/docker-swarm/mysql/"
install -m 0755 "${repository}/deploy/docker-swarm/mysql/bootstrap-replication.sh" "${kit}/examples/docker-swarm/mysql/"
install -m 0644 "${repository}/deploy/docker-swarm/mysql/mysql-stack.yml" "${kit}/examples/docker-swarm/mysql/"
install -m 0755 "${repository}/deploy/docker-swarm/postgresql/10-clusterguard-init.sh" "${kit}/examples/docker-swarm/postgresql/"
install -m 0755 "${repository}/deploy/docker-swarm/postgresql/clusterguard-postgres-entrypoint.sh" "${kit}/examples/docker-swarm/postgresql/"
install -m 0755 "${repository}/deploy/docker-swarm/postgresql/maintenance-rollout.sh" "${kit}/examples/docker-swarm/postgresql/"
install -m 0755 "${repository}/deploy/docker-swarm/postgresql/prepare-host.sh" "${kit}/examples/docker-swarm/postgresql/"
install -m 0755 "${repository}/deploy/docker-swarm/postgresql/verify-replication.sh" "${kit}/examples/docker-swarm/postgresql/"
install -m 0644 "${repository}/deploy/docker-swarm/postgresql/postgresql-stack.yml" "${kit}/examples/docker-swarm/postgresql/"
install -m 0644 "${repository}/packaging/systemd/clusterguard.env.example" "${kit}/clusterguard.env.example"
if [[ -n "${patch_trust_key}" ]]; then
  install -m 0644 "${patch_trust_key}" "${kit}/trust/patch-signing-public.pem"
fi
if [[ -n "${dependency_dir}" ]]; then
  cp -a "${dependency_dir}/." "${kit}/dependencies/"
fi
for ((database_package_index=0; database_package_index<database_package_count; database_package_index++)); do
  database_package="${database_packages[${database_package_index}]}"
  database_package_name="$(basename "${database_package}")"
  install -m 0644 "${database_package}" "${kit}/packages/database/${database_package_name}"
  if command -v sha256sum >/dev/null 2>&1; then
    database_digest="$(sha256sum "${kit}/packages/database/${database_package_name}" | awk '{print $1}')"
  else
    database_digest="$(shasum -a 256 "${kit}/packages/database/${database_package_name}" | awk '{print $1}')"
  fi
  printf '%s  %s\n' "${database_digest}" "${database_package_name}" >"${kit}/packages/database/${database_package_name}.sha256"
done

cat >"${kit}/packages/database/README.txt" <<'EOF'
本目录保存构建正式介质时通过 --database-package 明确嵌入的、已经批准的
MySQL、UPSQL 原厂二进制包，或 PostgreSQL 原厂二进制/官方 release 源码包。
每个嵌入包均附带同名 .sha256，安装器会在连接远端前再次校验。

如果本目录没有所需版本，也可以在现场放入批准的软件包；ClusterGuard HA
不会联网下载数据库软件、补丁或绕过数据库厂商许可证要求。

PostgreSQL 源码包会在第一个数据节点只编译一次，生成带构建清单和 SHA256
的统一二进制制品，再分发到全部控制/数据节点；不会在三台机器分别编译。
EOF
if ((database_package_count)); then
  {
    printf '\n本介质已嵌入：\n'
    for ((database_package_index=0; database_package_index<database_package_count; database_package_index++)); do
      database_package="${database_packages[${database_package_index}]}"
      printf '  %s\n' "$(basename "${database_package}")"
    done
  } >>"${kit}/packages/database/README.txt"
fi
cat >"${kit}/dependencies/README.txt" <<'EOF'
本目录是与离线目标系统匹配、由目标系统 RPM 信任链验证的基础运行仓库。
主介质只内置 ClusterGuard 和 MySQL 启动所需的小型依赖闭包及 repodata。
安装器会先校验 SHA256 和 RPM 签名，再让 DNF 仅安装实际缺失的运行库。
这些 RPM 必须由目标系统已信任的 Rocky GPG 公钥验签通过。

MySQL 8.0/8.4 通用二进制包在 EL8 上通常需要 ncurses 5 ABI。完整离线介质
必须包含 ncurses-compat-libs（提供 libncurses.so.5 和 libtinfo.so.5），不能以
libncurses.so.6 的软链接替代。

如需重新制作介质，请在与目标机同发行版、同主版本、同架构的联网镜像机上执行
tools/收集RHEL离线依赖.sh；构建介质时通过 --dependencies 指向收集目录。

PostgreSQL 源码编译依赖不在主介质内。明确选择 PostgreSQL 源码时，安装器
默认从构建节点已配置且启用签名校验的软件源联网安装到一次性隔离根，不会
升级生产宿主机。若软件源不可用，请上传与目标发行版、主版本和架构匹配的
clusterguard-ha-*-postgresql-build-deps-*.tar.gz，解压后通过
--postgresql-dependencies /path/to/dependencies 重试。
EOF

commit="$(git -C "${repository}" rev-parse HEAD 2>/dev/null || printf unknown)"
source_tree_dirty=unknown
source_tracked_diff_sha256=unavailable
source_untracked_count=unknown
if git -C "${repository}" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  source_tree_status="$(git -C "${repository}" status --porcelain=v1 --untracked-files=normal)"
  source_tree_dirty=false
  [[ -z "${source_tree_status}" ]] || source_tree_dirty=true
  if command -v sha256sum >/dev/null 2>&1; then
    source_tracked_diff_sha256="$(git -C "${repository}" diff --binary HEAD -- . | sha256sum | awk '{print $1}')"
  else
    source_tracked_diff_sha256="$(git -C "${repository}" diff --binary HEAD -- . | shasum -a 256 | awk '{print $1}')"
  fi
  source_untracked_count="$(git -C "${repository}" ls-files --others --exclude-standard | awk 'END {print NR + 0}')"
fi
if [[ "${source_tree_dirty}" == "false" ]]; then
  release_channel="stable"
else
  release_channel="candidate"
fi
cat >"${kit}/RELEASE-INFO" <<EOF
product=ClusterGuard HA
version=${version}
release=${release}
bundle_version=${bundle_version}
release_channel=${release_channel}
architecture=${media_arch}
commit=${commit}
source_tree_dirty=${source_tree_dirty}
source_tracked_diff_sha256=${source_tracked_diff_sha256}
source_untracked_count=${source_untracked_count}
built_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
database_package_count=${database_package_count}
postgresql_build_dependencies=separate-online-first
EOF

(
  cd "${kit}"
  if command -v sha256sum >/dev/null 2>&1; then
    find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum >SHA256SUMS
  else
    find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 shasum -a 256 >SHA256SUMS
  fi
)

archive="${output}/${kit_name}.tar.gz"
tar --no-xattrs -C "${stage}" -czf "${archive}" "${kit_name}"
if command -v sha256sum >/dev/null 2>&1; then
  (cd "${output}" && sha256sum "$(basename "${archive}")" >"$(basename "${archive}").sha256")
else
  (cd "${output}" && shasum -a 256 "$(basename "${archive}")" >"$(basename "${archive}").sha256")
fi

cp "${kit}/packages/"*.rpm "${output}/"
cp "${kit}/packages/"*.rpm.sha256 "${output}/"
mkdir -p "${output}/docs"
cp "${kit}/docs/"*.md "${output}/docs/"
cp "${kit}/RELEASE-INFO" "${output}/RELEASE-INFO"
printf '离线介质：%s\n' "${archive}"
printf '介质摘要：%s.sha256\n' "${archive}"
