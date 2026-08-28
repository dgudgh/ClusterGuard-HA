#!/usr/bin/env bash
set -euo pipefail
export COPYFILE_DISABLE=1

script_dir="$(cd "$(dirname "$0")" && pwd)"
repository="$(cd "${script_dir}/.." && pwd)"
input=""
output=""
version="2.2"
release="1"
platform="rocky-8"
architecture="x86_64"

usage() {
  cat <<'EOF'
用法：build-clusterguard-postgresql-deps-pack.sh --input DIR [选项]

选项：
  --input DIR          通过 clusterguard-offline-deps.sh --postgresql-source-build 收集的仓库
  --output DIR         输出目录；默认写入主项目 release/VERSION-RELEASE
  --version VERSION    产品版本，默认 2.2
  --release RELEASE    产品 release，默认 1
  --platform NAME      目标平台标签，默认 rocky-8
  --arch ARCH          目标 RPM 架构，默认 x86_64
EOF
}

while (($#)); do
  case "$1" in
    --input) input="${2:-}"; shift 2 ;;
    --output) output="${2:-}"; shift 2 ;;
    --version) version="${2:-}"; shift 2 ;;
    --release) release="${2:-}"; shift 2 ;;
    --platform) platform="${2:-}"; shift 2 ;;
    --arch) architecture="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数：$1" >&2; usage >&2; exit 2 ;;
  esac
done

[[ -d "${input}" && ! -L "${input}" ]] || { echo "必须通过 --input 提供安全的依赖仓库目录" >&2; exit 3; }
[[ "${version}" =~ ^[A-Za-z0-9._-]+$ && "${release}" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "版本格式无效" >&2; exit 2; }
[[ "${platform}" =~ ^[a-z0-9._-]+$ ]] || { echo "平台标签格式无效" >&2; exit 2; }
case "${architecture}" in x86_64|aarch64) ;; *) echo "架构必须是 x86_64 或 aarch64" >&2; exit 2 ;; esac

input="$(cd "${input}" && pwd)"
[[ -f "${input}/repodata/repomd.xml" ]] || { echo "依赖仓库缺少 repodata/repomd.xml" >&2; exit 3; }
[[ -f "${input}/PACKAGE-MANIFEST.txt" ]] || { echo "依赖仓库缺少 PACKAGE-MANIFEST.txt" >&2; exit 3; }
[[ -f "${input}/COLLECTION-INFO" ]] || { echo "依赖仓库缺少 COLLECTION-INFO" >&2; exit 3; }
grep -q '^postgresql_source_build=true$' "${input}/COLLECTION-INFO" || { echo "输入目录不是 PostgreSQL 源码编译依赖仓库" >&2; exit 3; }
grep -q '^signature_verification=passed$' "${input}/COLLECTION-INFO" || { echo "依赖仓库没有通过 RPM 签名验证" >&2; exit 3; }
[[ "$(sed -n 's/^architecture=//p' "${input}/COLLECTION-INFO" | head -n 1)" == "${architecture}" ]] || { echo "依赖仓库架构与 --arch 不一致" >&2; exit 3; }

if find "${input}" -maxdepth 1 -type l -print -quit | grep -q .; then
  echo "依赖仓库不能包含符号链接" >&2
  exit 3
fi
if find "${input}" -maxdepth 1 -type f -name '*.i686.rpm' -print -quit | grep -q .; then
  echo "依赖仓库包含 i686 RPM" >&2
  exit 3
fi
if [[ -f "${input}/SHA256SUMS" ]]; then
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "${input}" && sha256sum -c SHA256SUMS >/dev/null)
  else
    (cd "${input}" && shasum -a 256 -c SHA256SUMS >/dev/null)
  fi
fi

required_packages=(
  bash bison bzip2 coreutils diffutils file findutils flex gawk gcc gcc-c++ grep gzip hostname
  libicu-devel libuuid-devel libxml2-devel libxslt-devel libzstd-devel lz4-devel make
  openldap-devel openssl-devel pam-devel perl-interpreter pkgconf-pkg-config readline-devel
  sed systemd-devel tar which xz zlib-devel
)
for package in "${required_packages[@]}"; do
  grep -q "^${package}[[:space:]]" "${input}/PACKAGE-MANIFEST.txt" || {
    echo "依赖仓库缺少 PostgreSQL 编译包：${package}" >&2
    exit 3
  }
done

if command -v rpm >/dev/null 2>&1; then
  while IFS= read -r -d '' package; do
    package_arch="$(rpm -qp --qf '%{ARCH}' "${package}")"
    case "${package_arch}" in "${architecture}"|noarch) ;; *) echo "RPM 架构不匹配：$(basename "${package}")" >&2; exit 3 ;; esac
    signature_result="$(rpm --checksig "${package}" 2>&1 || true)"
    [[ "${signature_result}" == *"signatures OK"* ]] || { echo "RPM 签名无效：$(basename "${package}")" >&2; exit 3; }
  done < <(find "${input}" -maxdepth 1 -type f -name '*.rpm' -print0 | sort -z)
fi

bundle_version="${version}-${release}"
if [[ -z "${output}" ]]; then
  git_common_dir="$(git -C "${repository}" rev-parse --git-common-dir 2>/dev/null || true)"
  if [[ -n "${git_common_dir}" ]]; then
    [[ "${git_common_dir}" == /* ]] || git_common_dir="${repository}/${git_common_dir}"
    release_repository="$(cd "$(dirname "${git_common_dir}")" && pwd)"
  else
    release_repository="${repository}"
  fi
  output="${release_repository}/release/${bundle_version}"
fi

stage="$(mktemp -d /tmp/clusterguard-postgresql-deps.XXXXXX)"
trap 'rm -rf "${stage}"' EXIT
pack_name="clusterguard-ha-${bundle_version}-postgresql-build-deps-${platform}-${architecture}"
pack="${stage}/${pack_name}"
mkdir -p "${pack}/dependencies" "${output}"
cp -a "${input}/." "${pack}/dependencies/"

cat >"${pack}/README-中文.txt" <<EOF
ClusterGuard HA PostgreSQL 源码编译依赖包

此附加包不需要随主安装包上传。明确选择 PostgreSQL 源码安装时，安装器默认
使用构建节点已经配置的软件源联网获取依赖，并只写入一次性隔离构建根。

仅当联网软件源不可用或安装器明确提示依赖获取失败时，才使用此包：

  tar -xzf ${pack_name}.tar.gz
  ./install_clusterguard.sh [原参数] \\
    --postgresql-dependencies ./${pack_name}/dependencies \\
    --execute

本包只能用于 ${platform} ${architecture}。目标系统发行版、主版本或 CPU 架构
不一致时必须重新收集，不能强行安装。
EOF
cat >"${pack}/RELEASE-INFO" <<EOF
product=ClusterGuard HA PostgreSQL build dependencies
version=${version}
release=${release}
platform=${platform}
architecture=${architecture}
built_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF
(
  cd "${pack}"
  if command -v sha256sum >/dev/null 2>&1; then
    find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum >SHA256SUMS
  else
    find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 shasum -a 256 >SHA256SUMS
  fi
)

archive="${output}/${pack_name}.tar.gz"
tar --no-xattrs -C "${stage}" -czf "${archive}" "${pack_name}"
if command -v sha256sum >/dev/null 2>&1; then
  (cd "${output}" && sha256sum "$(basename "${archive}")" >"$(basename "${archive}").sha256")
else
  (cd "${output}" && shasum -a 256 "$(basename "${archive}")" >"$(basename "${archive}").sha256")
fi
printf 'PostgreSQL 依赖附加包：%s\n' "${archive}"
printf '附加包摘要：%s.sha256\n' "${archive}"
