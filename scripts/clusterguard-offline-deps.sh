#!/usr/bin/env bash
set -euo pipefail

output=""
application_rpm=""
postgresql_source_build=false
runtime_minimal=false

usage() {
  cat <<'EOF'
用法：
  clusterguard-offline-deps.sh --output DIR [--application-rpm FILE]
      [--runtime-minimal | --postgresql-source-build]

在与离线目标机操作系统大版本、CPU 架构一致的联网 RHEL/Rocky/Alma
主机上运行。脚本下载 ClusterGuard HA 的基础依赖及其传递依赖，并生成
SHA256SUMS。追加 --postgresql-source-build 时，同时收集 PostgreSQL
官方源码编译所需的开发 RPM。数据库源码或二进制包需要单独准备。

--runtime-minimal 只收集 MySQL 通用二进制在 EL8 基础系统上常缺少的
libaio、ncurses 5 兼容库和 NUMA 运行库，用于控制主离线包体积。
EOF
}

while (($#)); do
  case "$1" in
    --output) output="${2:-}"; shift 2 ;;
    --application-rpm) application_rpm="${2:-}"; shift 2 ;;
    --runtime-minimal) runtime_minimal=true; shift ;;
    --postgresql-source-build) postgresql_source_build=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数：$1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ "${runtime_minimal}" == "true" && "${postgresql_source_build}" == "true" ]]; then
  echo "--runtime-minimal 与 --postgresql-source-build 不能同时使用" >&2
  exit 2
fi

[[ -n "${output}" ]] || { echo "必须提供 --output" >&2; exit 2; }
command -v dnf >/dev/null 2>&1 || { echo "需要 dnf" >&2; exit 3; }
dnf download --help >/dev/null 2>&1 || {
  echo "当前构建机缺少 dnf download，请先安装 dnf-plugins-core" >&2
  exit 3
}
command -v rpm >/dev/null 2>&1 || { echo "需要 rpm" >&2; exit 3; }
command -v sha256sum >/dev/null 2>&1 || { echo "需要 sha256sum" >&2; exit 3; }
command -v createrepo_c >/dev/null 2>&1 || {
  echo "需要 createrepo_c，用于生成生产节点可直接使用的离线仓库元数据" >&2
  exit 3
}

if [[ -n "${application_rpm}" ]]; then
  [[ -f "${application_rpm}" && ! -L "${application_rpm}" ]] || {
    echo "ClusterGuard HA RPM 不存在或不是普通文件" >&2
    exit 2
  }
fi

# ClusterGuard control-plane dependencies plus the MySQL generic binary runtime
# set. MySQL 8.0/8.4 generic Linux packages are commonly linked against the
# ncurses 5 ABI even on EL8, where only the ncurses 6 ABI is installed by
# default. ncurses-compat-libs supplies libncurses.so.5 and libtinfo.so.5.
if [[ "${runtime_minimal}" == "true" ]]; then
  dependencies=(
    libaio
    ncurses-compat-libs
    numactl-libs
  )
else
  dependencies=(
    bash
    coreutils
    findutils
    gzip
    iproute
    iputils
    libaio
    ncurses-compat-libs
    numactl-libs
    openssh-clients
    openssl
    shadow-utils
    systemd
    tar
  )
fi

if [[ "${postgresql_source_build}" == "true" ]]; then
  dependencies+=(
    bzip2
    bison
    diffutils
    file
    flex
    gcc
    gcc-c++
    hostname
    libicu-devel
    libuuid-devel
    libxml2-devel
    libxslt-devel
    libzstd-devel
    lz4-devel
    make
    openldap-devel
    openssl-devel
    pam-devel
    perl-interpreter
    pkgconf-pkg-config
    readline-devel
    systemd-devel
    which
    xz
    zlib-devel
  )
fi

mkdir -p "${output}"
target_arch="$(rpm --eval '%{_arch}')"
download_arguments=(
  download
  --archlist="${target_arch},noarch"
  --destdir "${output}"
)
if [[ "${runtime_minimal}" != "true" ]]; then
  download_arguments+=(--resolve --alldeps)
fi
dnf "${download_arguments[@]}" "${dependencies[@]}"

signature_failures=0
: >"${output}/SIGNATURE-FAILURES.txt"
while IFS= read -r -d '' package; do
  package_arch="$(rpm -qp --qf '%{ARCH}' "${package}")"
  case "${package_arch}" in
    "${target_arch}"|noarch) ;;
    *)
      printf 'architecture mismatch: %s (%s)\n' "$(basename "${package}")" "${package_arch}" \
        >>"${output}/SIGNATURE-FAILURES.txt"
      signature_failures=1
      continue
      ;;
  esac
  signature_result="$(rpm --checksig "${package}" 2>&1 || true)"
  if [[ "${signature_result}" != *"signatures OK"* ]]; then
    printf '%s\n' "${signature_result}" >>"${output}/SIGNATURE-FAILURES.txt"
    signature_failures=1
  fi
done < <(find "${output}" -maxdepth 1 -type f -name '*.rpm' -print0 | sort -z)
if ((signature_failures != 0)); then
  cat "${output}/SIGNATURE-FAILURES.txt" >&2
  echo "离线依赖包含签名无效或架构不匹配的 RPM" >&2
  exit 4
fi
rm -f "${output}/SIGNATURE-FAILURES.txt"

rpm -qp --qf '%{NAME}\t%{EPOCHNUM}:%{VERSION}-%{RELEASE}\t%{ARCH}\n' \
  "${output}"/*.rpm | sort -u >"${output}/PACKAGE-MANIFEST.txt"

# The deployment nodes consume this directory as a disabled-by-default local
# repository. Keeping repodata in the deliverable lets dnf select only missing
# packages instead of force-installing every RPM in the closure.
rm -rf "${output}/repodata"
createrepo_c "${output}" >/dev/null

if [[ -n "${application_rpm}" ]]; then
  install -m 0644 "${application_rpm}" "${output}/$(basename "${application_rpm}")"
fi

{
  printf 'generated_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'os_release='; tr '\n' ' ' </etc/redhat-release; printf '\n'
  printf 'architecture=%s\n' "$(rpm --eval '%{_arch}')"
  printf 'packages='; printf '%s ' "${dependencies[@]}"; printf '\n'
  printf 'runtime_minimal=%s\n' "${runtime_minimal}"
  printf 'postgresql_source_build=%s\n' "${postgresql_source_build}"
  printf 'signature_verification=passed\n'
  printf 'repository_metadata=repodata/repomd.xml\n'
} >"${output}/COLLECTION-INFO"

(
  cd "${output}"
  find . -maxdepth 1 -type f -name '*.rpm' -print0 \
    | sort -z \
    | xargs -0 sha256sum >SHA256SUMS
)

printf '离线依赖已收集：%s\n' "${output}"
printf '请把整个目录复制到目标机，并先执行 sha256sum -c SHA256SUMS。\n'
