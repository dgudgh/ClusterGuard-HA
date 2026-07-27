#!/usr/bin/env bash
set -euo pipefail
export COPYFILE_DISABLE=1

script_dir="$(cd "$(dirname "$0")" && pwd)"
repository="$(cd "${script_dir}/.." && pwd)"
output="${repository}/dist"
version="1.0.0"
release="1"
goarch="${CG_RPM_GOARCH:-amd64}"
nfpm_binary="${CG_NFPM_BINARY:-$(command -v nfpm 2>/dev/null || true)}"
jq_binary="${CG_JQ_BINARY:-}"

usage() {
  cat <<'EOF'
usage: build-clusterguard-rpm.sh [options]

Options:
  --output DIR          RPM output directory
  --version VERSION     RPM version, for example 1.0.0
  --release RELEASE     RPM release, for example 1 or 0.1.rc1
  --goarch ARCH         amd64 or arm64
  --nfpm-binary FILE    trusted nFPM executable
  --jq-binary FILE      static Linux jq matching --goarch
EOF
}

while (($#)); do
  case "$1" in
    --output) output="${2:-}"; shift 2 ;;
    --version) version="${2:-}"; shift 2 ;;
    --release) release="${2:-}"; shift 2 ;;
    --goarch) goarch="${2:-}"; shift 2 ;;
    --nfpm-binary) nfpm_binary="${2:-}"; shift 2 ;;
    --jq-binary) jq_binary="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown RPM build argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[[ "${version}" =~ ^[0-9][0-9A-Za-z._+~]*$ ]] || { echo "invalid RPM version" >&2; exit 2; }
[[ "${release}" =~ ^[0-9][0-9A-Za-z._+~]*$ ]] || { echo "invalid RPM release" >&2; exit 2; }
case "${goarch}" in
  amd64) rpm_arch="x86_64" ;;
  arm64) rpm_arch="aarch64" ;;
  *) echo "RPM architecture must be amd64 or arm64" >&2; exit 2 ;;
esac
[[ -x "${nfpm_binary}" ]] || { echo "trusted nFPM binary is required" >&2; exit 3; }
[[ -x "${jq_binary}" ]] || { echo "static Linux jq binary is required" >&2; exit 3; }
command -v file >/dev/null 2>&1 || { echo "file is required" >&2; exit 3; }
command -v go >/dev/null 2>&1 || { echo "Go toolchain is required" >&2; exit 3; }

jq_format="$(file -b "${jq_binary}")"
[[ "${jq_format}" == *ELF* ]] || { echo "jq binary is not a Linux ELF executable" >&2; exit 3; }
case "${goarch}" in
  amd64)
    [[ "${jq_format}" == *x86-64* || "${jq_format}" == *x86_64* ]] ||
      { echo "jq binary architecture is not amd64" >&2; exit 3; }
    ;;
  arm64)
    [[ "${jq_format}" == *aarch64* || "${jq_format}" == *ARM64* ]] ||
      { echo "jq binary architecture is not arm64" >&2; exit 3; }
    ;;
esac

stage="$(mktemp -d /tmp/clusterguard-rpm.XXXXXX)"
trap 'rm -rf "${stage}"' EXIT
root="${stage}/root"
mkdir -p \
  "${root}/bin" \
  "${root}/scripts" \
  "${root}/configs" \
  "${root}/packaging" \
  "${root}/docs"

build_targets=(
  "clusterguard:./cmd/clusterguard"
  "cgctl:./cmd/cgctl"
  "clusterguard-agent:./cmd/clusterguard-agent"
)
for target in "${build_targets[@]}"; do
  command_path="${target%%:*}"
  package_path="${target#*:}"
  CGO_ENABLED=0 GOOS=linux GOARCH="${goarch}" \
    go -C "${repository}" build -trimpath -ldflags "-s -w" \
      -o "${root}/bin/${command_path}" "${package_path}"
done
install -m 0755 "${jq_binary}" "${root}/bin/jq"

helpers=(
  clusterguard-configure.sh
  clusterguard-agent-stdio.sh
  clusterguard-node-lifecycle.sh
  clusterguard-mysql-install.sh
  clusterguard-mysql-sync.sh
  clusterguard-mysql-probe-cleanup.sh
  clusterguard-postgresql-install.sh
  clusterguard-postgresql-sync.sh
  clusterguard-preflight.sh
  clusterguard-smoke.sh
  clusterguard-ha-matrix.sh
)
for helper in "${helpers[@]}"; do
  install -m 0755 "${repository}/scripts/${helper}" "${root}/scripts/${helper}"
done

install -m 0644 "${repository}/configs/clusterguard.example.json" "${root}/configs/"
install -m 0644 "${repository}/configs/clusterguard-agent.example.json" "${root}/configs/"
install -m 0644 "${repository}/packaging/systemd/clusterguard.env.example" "${root}/packaging/"
install -m 0644 "${repository}/packaging/systemd/"*.service "${root}/packaging/"
install -m 0644 "${repository}/packaging/systemd/"*.timer "${root}/packaging/"
install -m 0644 "${repository}/packaging/logrotate/clusterguard-ha" "${root}/packaging/clusterguard-ha.logrotate"
install -m 0644 "${repository}/README.md" "${root}/docs/README.md"
install -m 0644 "${repository}/docs/zh-CN/offline-rpm-install.md" "${root}/docs/"
install -m 0644 "${repository}/docs/zh-CN/database-preparation.md" "${root}/docs/"
install -m 0644 "${repository}/docs/zh-CN/operations-manual.md" "${root}/docs/"

commit="$(git -C "${repository}" rev-parse HEAD 2>/dev/null || printf unknown)"
build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
cat >"${root}/BUILD-INFO" <<EOF
product=ClusterGuard HA
version=${version}
release=${release}
architecture=${rpm_arch}
commit=${commit}
built_at=${build_time}
EOF

mkdir -p "${output}"
package="${output}/clusterguard-ha-${version}-${release}.${rpm_arch}.rpm"
source_date_epoch="$(git -C "${repository}" show -s --format=%ct HEAD 2>/dev/null || date +%s)"
(
  cd "${repository}/packaging/rpm"
  CG_RPM_STAGE="${root}" \
  CG_RPM_VERSION="${version}" \
  CG_RPM_RELEASE="${release}" \
  CG_RPM_GOARCH="${goarch}" \
  SOURCE_DATE_EPOCH="${source_date_epoch}" \
    "${nfpm_binary}" package --config nfpm.yaml --packager rpm --target "${package}"
)

package_format="$(file -b "${package}")"
[[ "${package_format}" == *RPM* ]] || { echo "nFPM did not produce an RPM package" >&2; exit 4; }
if command -v sha256sum >/dev/null 2>&1; then
  (cd "${output}" && sha256sum "$(basename "${package}")" >"$(basename "${package}").sha256")
else
  (cd "${output}" && shasum -a 256 "$(basename "${package}")" >"$(basename "${package}").sha256")
fi

printf 'rpm: %s\n' "${package}"
printf 'checksum: %s.sha256\n' "${package}"
