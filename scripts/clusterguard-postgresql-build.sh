#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

# Build one PostgreSQL release-source archive into the exact installation
# prefix used by every ClusterGuard data node. Dependencies are installed in a
# disposable build root, either from the target host's signed online repos or
# from a separately delivered, verified PostgreSQL dependency repository.

source_package=""
output_dir=""
install_prefix=""
requested_version=""
jobs=""
build_root="/var/lib/clusterguard/postgresql-source-build"
dependency_repository=""
online_dependencies=false
keep_build=false
build_recipe="4"

usage() {
  cat <<'EOF'
用法：
  clusterguard-postgresql-build.sh \
    --source-package /opt/postgresql-16.4.tar.bz2 \
    --output-dir /opt/clusterguard/packages/source-build \
    --install-prefix /opt/clusterguard/postgresql/5432/software \
    --version 16.4 [--jobs 8] \
    [--online-dependencies | \
     --dependency-repository /path/to/verified/dependencies]

说明：
  仅接受单一安全顶层目录的 PostgreSQL 官方 release 源码包。
  --online-dependencies 使用目标节点当前已配置且启用 GPG 校验的软件源。
  --dependency-repository 使用单独上传的 ClusterGuard PostgreSQL 依赖包。
  两种模式都把工具链安装到一次性隔离根，不升级生产宿主机的软件包。
  两个参数都不提供时，要求宿主机已经具备源码编译依赖。
EOF
}

die() { printf 'PostgreSQL source build error: %s\n' "$*" >&2; exit 3; }
log() { printf '[postgresql-source-build] %s\n' "$*" >&2; }

while (($#)); do
  case "$1" in
    --source-package) source_package="${2:-}"; shift 2 ;;
    --output-dir) output_dir="${2:-}"; shift 2 ;;
    --install-prefix) install_prefix="${2:-}"; shift 2 ;;
    --version) requested_version="${2:-}"; shift 2 ;;
    --jobs) jobs="${2:-}"; shift 2 ;;
    --build-root) build_root="${2:-}"; shift 2 ;;
    --dependency-repository) dependency_repository="${2:-}"; shift 2 ;;
    --online-dependencies) online_dependencies=true; shift ;;
    --keep-build) keep_build=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ -f "${source_package}" && ! -L "${source_package}" ]] || die "source package is not a regular file: ${source_package}"
[[ "$(basename "${source_package}")" =~ ^[A-Za-z0-9._+-]+$ ]] || die "source package filename contains unsupported characters"
[[ "${output_dir}" == /* && "${output_dir}" != "/" ]] || die "--output-dir must be a safe absolute path"
[[ "${install_prefix}" == /opt/clusterguard/postgresql/*/software ]] || die "--install-prefix must be a ClusterGuard PostgreSQL software path"
[[ "${requested_version}" =~ ^[0-9]+([.][0-9]+)*$ ]] || die "--version is invalid"
[[ "${build_root}" == /* && "${build_root}" != "/" ]] || die "--build-root must be a safe absolute path"
if [[ -n "${dependency_repository}" ]]; then
  [[ "${dependency_repository}" == /* && "${dependency_repository}" != "/" ]] || die "--dependency-repository must be a safe absolute path"
  [[ -d "${dependency_repository}" && ! -L "${dependency_repository}" ]] || die "dependency repository is not a safe directory"
  [[ -f "${dependency_repository}/repodata/repomd.xml" && ! -L "${dependency_repository}/repodata/repomd.xml" ]] || die "dependency repository is missing repodata/repomd.xml"
fi
[[ -z "${dependency_repository}" || "${online_dependencies}" != "true" ]] || die "--online-dependencies and --dependency-repository are mutually exclusive"

if [[ -z "${jobs}" ]]; then
  jobs="$(command -v nproc >/dev/null 2>&1 && nproc || getconf _NPROCESSORS_ONLN 2>/dev/null || printf 1)"
  ((jobs > 16)) && jobs=16
fi
[[ "${jobs}" =~ ^[1-9][0-9]*$ && "${jobs}" -le 128 ]] || die "--jobs must be between 1 and 128"

required_commands=(tar awk sed grep find install sha256sum hostname uname)
if [[ -n "${dependency_repository}" || "${online_dependencies}" == "true" ]]; then
  required_commands+=(dnf rpm chroot cp)
else
  required_commands+=(make gcc perl)
fi
for command_name in "${required_commands[@]}"; do
  command -v "${command_name}" >/dev/null 2>&1 || die "missing offline build dependency: ${command_name}"
done

run_isolated_build() {
  local mode="$1" repository="${2:-}" isolated_root repository_release repository_arch package package_arch signature_result
  local source_name child_source child_builder child_output child_artifact child_name
  local -a build_packages=(
    bash bison bzip2 coreutils diffutils file findutils flex gawk gcc gcc-c++
    grep gzip hostname libicu-devel libuuid-devel libxml2-devel libxslt-devel
    libzstd-devel lz4-devel make openldap-devel openssl-devel pam-devel perl-interpreter
    pkgconf-pkg-config readline-devel sed systemd-devel tar which xz zlib-devel
  )

  case "${mode}" in
    offline)
      [[ -f "${repository}/PACKAGE-MANIFEST.txt" ]] || die "dependency repository is missing PACKAGE-MANIFEST.txt"
      [[ -f "${repository}/COLLECTION-INFO" ]] || die "dependency repository is missing COLLECTION-INFO"
      if [[ -f "${repository}/SHA256SUMS" ]]; then
        (cd "${repository}" && sha256sum -c SHA256SUMS >/dev/null) || die "dependency repository checksum verification failed"
      fi
      repository_arch="$(sed -n 's/^architecture=//p' "${repository}/COLLECTION-INFO" | head -n 1)"
      [[ "${repository_arch}" == "$(uname -m)" ]] || die "dependency repository architecture ${repository_arch:-unknown} does not match $(uname -m)"
      repository_release="$(sed -n 's/^os_release=.* release \([0-9][0-9]*\).*/\1/p' "${repository}/COLLECTION-INFO" | head -n 1)"
      [[ "${repository_release}" =~ ^[0-9]+$ ]] || die "dependency repository operating-system release is unknown"
      for package in "${build_packages[@]}"; do
        grep -q "^${package}[[:space:]]" "${repository}/PACKAGE-MANIFEST.txt" || die "dependency repository is missing build package: ${package}"
      done
      while IFS= read -r -d '' package; do
        package_arch="$(rpm -qp --qf '%{ARCH}' "${package}")"
        case "${package_arch}" in
          "${repository_arch}"|noarch) ;;
          *) die "dependency RPM architecture mismatch: $(basename "${package}") (${package_arch})" ;;
        esac
        signature_result="$(rpm --checksig "${package}" 2>&1 || true)"
        [[ "${signature_result}" == *"signatures OK"* ]] || die "dependency RPM signature verification failed: $(basename "${package}")"
      done < <(find "${repository}" -maxdepth 1 -type f -name '*.rpm' -print0 | sort -z)
      ;;
    online)
      [[ -r /etc/os-release ]] || die "cannot determine the target operating-system release"
      # shellcheck disable=SC1091
      source /etc/os-release
      repository_release="${VERSION_ID%%.*}"
      repository_arch="$(uname -m)"
      [[ "${repository_release}" =~ ^[0-9]+$ ]] || die "target operating-system release is invalid: ${VERSION_ID:-unknown}"
      case "${repository_arch}" in x86_64|aarch64) ;; *) die "unsupported online build architecture: ${repository_arch}" ;; esac
      ;;
    *) die "unknown isolated dependency mode: ${mode}" ;;
  esac

  install -d -m 0700 "$(dirname "${build_root}")"
  isolated_root="$(mktemp -d "${build_root}.installroot.XXXXXX")"
  cleanup_isolated() {
    [[ "${keep_build}" == "true" ]] || rm -rf "${isolated_root}"
  }
  trap cleanup_isolated EXIT
  if [[ "${mode}" == "offline" ]]; then
    log "creating offline isolated PostgreSQL build root (EL${repository_release}, ${repository_arch})"
    dnf --quiet \
      --installroot="${isolated_root}" \
      --releasever="${repository_release}" \
      --disablerepo='*' \
      --repofrompath="clusterguard-postgresql-build,file://${repository}" \
      --setopt=clusterguard-postgresql-build.gpgcheck=0 \
      --setopt=clusterguard-postgresql-build.repo_gpgcheck=0 \
      --setopt=clusterguard-postgresql-build.module_hotfixes=true \
      --setopt=install_weak_deps=False \
      --setopt=module_platform_id="platform:el${repository_release}" \
      --nogpgcheck install -y "${build_packages[@]}" >&2
  else
    log "creating online isolated PostgreSQL build root from the target node's signed repositories (EL${repository_release}, ${repository_arch})"
    if ! dnf --quiet \
      --installroot="${isolated_root}" \
      --releasever="${repository_release}" \
      --setopt=install_weak_deps=False \
      --setopt=module_platform_id="platform:el${repository_release}" \
      --setopt=timeout=30 \
      --setopt=retries=2 \
      install -y "${build_packages[@]}" >&2; then
      die "在线安装 PostgreSQL 编译依赖失败。请上传与目标节点发行版、主版本和架构完全匹配的 ClusterGuard PostgreSQL 依赖包，解压后通过 --postgresql-dependencies /path/to/dependencies 重新执行安装"
    fi
  fi

  source_name="$(basename "${source_package}")"
  install -d -m 0700 "${isolated_root}/clusterguard-input" "${isolated_root}/clusterguard-output"
  install -m 0640 "${source_package}" "${isolated_root}/clusterguard-input/${source_name}"
  install -m 0755 "$0" "${isolated_root}/clusterguard-input/builder.sh"
  child_source="/clusterguard-input/${source_name}"
  child_builder="/clusterguard-input/builder.sh"
  child_output="/clusterguard-output"
  log "compiling PostgreSQL inside isolated root; production host packages remain unchanged"
  child_artifact="$(chroot "${isolated_root}" /bin/bash "${child_builder}" \
    --source-package "${child_source}" \
    --output-dir "${child_output}" \
    --install-prefix "${install_prefix}" \
    --version "${requested_version}" \
    --jobs "${jobs}" \
    --build-root /clusterguard-build)"
  child_artifact="$(printf '%s\n' "${child_artifact}" | tail -n 1 | tr -d '\r')"
  [[ "${child_artifact}" == "${child_output}/"*.tar.gz ]] || die "isolated build returned an uncontrolled artifact path: ${child_artifact}"
  child_name="$(basename "${child_artifact}")"
  [[ -f "${isolated_root}${child_artifact}" && -f "${isolated_root}${child_artifact}.sha256" ]] || die "isolated build did not create the artifact and checksum"
  install -d -m 0750 "${output_dir}"
  install -m 0640 "${isolated_root}${child_artifact}" "${output_dir}/${child_name}"
  install -m 0640 "${isolated_root}${child_artifact}.sha256" "${output_dir}/${child_name}.sha256"
  (cd "${output_dir}" && sha256sum -c "${child_name}.sha256" >/dev/null) || die "isolated build artifact checksum verification failed"
  log "created ${output_dir}/${child_name} without modifying host packages"
  cleanup_isolated
  trap - EXIT
  printf '%s\n' "${output_dir}/${child_name}"
}

if [[ -n "${dependency_repository}" ]]; then
  run_isolated_build offline "${dependency_repository}"
  exit 0
fi
if [[ "${online_dependencies}" == "true" ]]; then
  run_isolated_build online
  exit 0
fi

install -d -m 0750 "${output_dir}"
[[ ! -L "${output_dir}" ]] || die "output directory must not be a symbolic link"
source_sha256="$(sha256sum "${source_package}" | awk '{print $1}')"
architecture="$(uname -m)"
case "${architecture}" in
  x86_64|aarch64) ;;
  *) die "unsupported Linux build architecture: ${architecture}" ;;
esac
database_port="$(basename "$(dirname "${install_prefix}")")"
builder_sha256="$(sha256sum "$0" | awk '{print $1}')"
cache_marker="${output_dir}/.clusterguard-postgresql-${source_sha256}-${requested_version}-${architecture}-p${database_port}.artifact"

reuse_cached_artifact() {
  local cached_builder cached_artifact_name cached_artifact expected actual
  [[ -f "${cache_marker}" && ! -L "${cache_marker}" ]] || return 1
  cached_builder="$(sed -n '1p' "${cache_marker}")"
  cached_artifact_name="$(sed -n '2p' "${cache_marker}")"
  [[ "${cached_builder}" == "${build_recipe}:${builder_sha256}" ]] || return 1
  [[ "${cached_artifact_name}" =~ ^postgresql-[A-Za-z0-9._+-]+-clusterguard-linux-(x86_64|aarch64)-p[0-9]+[.]tar[.]gz$ ]] || return 1
  cached_artifact="${output_dir}/${cached_artifact_name}"
  [[ -f "${cached_artifact}" && ! -L "${cached_artifact}" && -f "${cached_artifact}.sha256" && ! -L "${cached_artifact}.sha256" ]] || return 1
  expected="$(awk 'NR == 1 {print $1}' "${cached_artifact}.sha256")"
  [[ "${expected}" =~ ^[0-9a-f]{64}$ ]] || return 1
  actual="$(sha256sum "${cached_artifact}" | awk '{print $1}')"
  [[ "${actual}" == "${expected}" ]] || return 1
  log "reusing verified artifact ${cached_artifact_name}"
  printf '%s\n' "${cached_artifact}"
  return 0
}

if reuse_cached_artifact; then
  exit 0
fi

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

install -d -m 0700 "$(dirname "${build_root}")"
workspace="$(mktemp -d "${build_root}.XXXXXX")"
cleanup() {
  if [[ "${keep_build}" != "true" ]]; then rm -rf "${workspace}"; fi
}
trap cleanup EXIT
chmod 0700 "${workspace}"
listing="${workspace}/archive.list"
tar -tf "${source_package}" >"${listing}" 2>/dev/null || die "source package is not a readable tar archive"
awk '$0 ~ /^\// || $0 ~ /(^|\/)\.\.(\/|$)/ {bad=1} END {exit bad}' "${listing}" || die "source package contains an absolute or parent path"
archive_has_single_root "${listing}" || die "source package must contain exactly one top-level directory"

source_root_name="$(awk '{path=$0; sub(/^\.\//, "", path); sub(/\/$/, "", path); if (path != "") {split(path, parts, "/"); print parts[1]; exit}}' "${listing}")"
for marker in configure src/backend/Makefile src/bin/initdb/Makefile contrib/Makefile; do
  grep -Eq "^\.?/?${source_root_name}/${marker}$" "${listing}" || die "source package is missing ${marker}"
done

source_parent="${workspace}/source"
destdir="${workspace}/destdir"
package_parent="${workspace}/package"
install -d -m 0700 "${source_parent}" "${destdir}" "${package_parent}" "${output_dir}"
tar -xf "${source_package}" -C "${source_parent}"
source_root="${source_parent}/${source_root_name}"
[[ -x "${source_root}/configure" ]] || chmod 0755 "${source_root}/configure"

configure_args=("--prefix=${install_prefix}" "--with-openssl")
if command -v pkg-config >/dev/null 2>&1; then
  pkg-config --exists icu-uc icu-i18n 2>/dev/null && configure_args+=(--with-icu)
  pkg-config --exists libxml-2.0 2>/dev/null && configure_args+=(--with-libxml)
  pkg-config --exists libxslt 2>/dev/null && configure_args+=(--with-libxslt)
  pkg-config --exists liblz4 2>/dev/null && configure_args+=(--with-lz4)
  pkg-config --exists libzstd 2>/dev/null && configure_args+=(--with-zstd)
  pkg-config --exists libsystemd 2>/dev/null && configure_args+=(--with-systemd)
fi
[[ -f /usr/include/security/pam_appl.h ]] && configure_args+=(--with-pam)
[[ -f /usr/include/ldap.h || -f /usr/include/openldap/ldap.h ]] && configure_args+=(--with-ldap)
[[ -f /usr/include/uuid/uuid.h ]] && configure_args+=(--with-uuid=e2fs)

log "configuring ${source_root_name} for ${install_prefix}"
(
  cd "${source_root}"
  if ! ./configure "${configure_args[@]}" >"${workspace}/configure.log" 2>&1; then
    tail -n 80 "${workspace}/configure.log" >&2 || true
    die "configure failed; provide the matching offline development RPM set"
  fi
  make -j "${jobs}"
  make -C contrib -j "${jobs}"
  make DESTDIR="${destdir}" install
  make -C contrib DESTDIR="${destdir}" install
)

payload_root="${destdir}${install_prefix}"
for binary in initdb postgres psql pg_basebackup pg_rewind pg_controldata pg_config; do
  [[ -x "${payload_root}/bin/${binary}" ]] || die "compiled artifact is missing bin/${binary}"
done
reported_version="$(${payload_root}/bin/pg_config --version | awk '{print $2}')"
[[ "${reported_version}" =~ ^[0-9]+([.][0-9]+)*$ ]] || die "pg_config --version returned an invalid version"
requested_major="${requested_version%%.*}"
reported_major="${reported_version%%.*}"
[[ "${requested_major}" == "${reported_major}" ]] || die "source version ${reported_version} does not match requested major ${requested_version}"
if [[ "${requested_version}" == *.* && "${reported_version}" != "${requested_version}" ]]; then
  die "source version ${reported_version} does not exactly match requested version ${requested_version}"
fi

architecture="$(uname -m)"
artifact_root_name="postgresql-${reported_version}-clusterguard-linux-${architecture}-p$(basename "$(dirname "${install_prefix}")")"
artifact_root="${package_parent}/${artifact_root_name}"
install -d -m 0755 "${artifact_root}"
cp -a "${payload_root}/." "${artifact_root}/"

os_id="unknown"
os_version="unknown"
if [[ -r /etc/os-release ]]; then
  # shellcheck disable=SC1091
  source /etc/os-release
  os_id="${ID:-unknown}"
  os_version="${VERSION_ID:-unknown}"
fi
glibc_version="$(getconf GNU_LIBC_VERSION 2>/dev/null || printf unknown)"
configure_text="$(printf '%s ' "${configure_args[@]}")"
cat >"${artifact_root}/BUILD-MANIFEST.json" <<EOF
{
  "product": "ClusterGuard HA PostgreSQL build artifact",
  "postgresql_version": "${reported_version}",
  "source_file": "$(basename "${source_package}")",
  "source_sha256": "${source_sha256}",
  "build_recipe": "${build_recipe}",
  "builder_sha256": "${builder_sha256}",
  "install_prefix": "${install_prefix}",
  "build_host": "$(hostname -f 2>/dev/null || hostname)",
  "architecture": "${architecture}",
  "os_id": "${os_id}",
  "os_version": "${os_version}",
  "glibc": "${glibc_version}",
  "configure": "${configure_text% }"
}
EOF
chmod 0644 "${artifact_root}/BUILD-MANIFEST.json"

artifact="${output_dir}/${artifact_root_name}.tar.gz"
temporary_artifact="${artifact}.tmp.$$"
if tar --help 2>/dev/null | grep -q -- '--sort'; then
  tar --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner -C "${package_parent}" -czf "${temporary_artifact}" "${artifact_root_name}"
else
  tar -C "${package_parent}" -czf "${temporary_artifact}" "${artifact_root_name}"
fi
mv "${temporary_artifact}" "${artifact}"
chmod 0640 "${artifact}"
(
  cd "${output_dir}"
  sha256sum "$(basename "${artifact}")" >"$(basename "${artifact}").sha256"
)
chmod 0640 "${artifact}.sha256"
temporary_marker="${cache_marker}.tmp.$$"
printf '%s\n%s\n' "${build_recipe}:${builder_sha256}" "$(basename "${artifact}")" >"${temporary_marker}"
chmod 0600 "${temporary_marker}"
mv "${temporary_marker}" "${cache_marker}"
log "created ${artifact}"
printf '%s\n' "${artifact}"
