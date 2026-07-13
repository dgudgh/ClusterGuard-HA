#!/usr/bin/env bash
set -euo pipefail
export COPYFILE_DISABLE=1

script_dir="$(cd "$(dirname "$0")" && pwd)"
repository="$(cd "${script_dir}/.." && pwd)"
output="${repository}/dist"
version="$(git -C "${repository}" describe --always --dirty 2>/dev/null || printf dev)"
goos="${CG_BUNDLE_GOOS:-linux}"
goarch="${CG_BUNDLE_GOARCH:-amd64}"

while (($#)); do
  case "$1" in
    --output) output="${2:-}"; shift 2 ;;
    --version) version="${2:-}"; shift 2 ;;
    --goos) goos="${2:-}"; shift 2 ;;
    --goarch) goarch="${2:-}"; shift 2 ;;
    -h|--help) echo "usage: $0 [--output DIR] [--version VERSION] [--goos OS] [--goarch ARCH]"; exit 0 ;;
    *) echo "unknown bundle argument: $1" >&2; exit 2 ;;
  esac
done

[[ "${version}" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "invalid bundle version" >&2; exit 2; }
stage="$(mktemp -d /tmp/clusterguard-bundle.XXXXXX)"
trap 'rm -rf "${stage}"' EXIT
root="${stage}/clusterguard-ha-${version}-${goos}-${goarch}"
mkdir -p "${root}/bin" "${root}/scripts" "${root}/configs" "${root}/packaging/systemd" "${root}/packaging/logrotate"

build_targets=(
  "clusterguard:./cmd/clusterguard"
  "cgctl:./cmd/cgctl"
  "clusterguard-agent:./cmd/clusterguard-agent"
)
for target in "${build_targets[@]}"; do
  command_path="${target%%:*}"
  package_path="${target#*:}"
  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" go -C "${repository}" build -trimpath -ldflags "-s -w" -o "${root}/bin/${command_path}" "${package_path}"
done
for helper in clusterguard-install.sh clusterguard-preflight.sh clusterguard-smoke.sh clusterguard-ha-matrix.sh clusterguard-node-lifecycle.sh clusterguard-mysql-install.sh clusterguard-mysql-sync.sh clusterguard-agent-stdio.sh; do
  install -m 0755 "${repository}/scripts/${helper}" "${root}/scripts/${helper}"
done
cp "${repository}"/configs/*.json "${root}/configs/"
cp "${repository}"/packaging/systemd/* "${root}/packaging/systemd/"
cp "${repository}"/packaging/logrotate/* "${root}/packaging/logrotate/"
cp "${repository}/README.md" "${root}/README.md"
if [[ -n "${CG_JQ_BINARY:-}" ]]; then
  [[ -x "${CG_JQ_BINARY}" ]] || { echo "CG_JQ_BINARY is not executable" >&2; exit 3; }
  install -m 0755 "${CG_JQ_BINARY}" "${root}/bin/jq"
fi

if command -v sha256sum >/dev/null 2>&1; then
  (cd "${root}" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum >SHA256SUMS)
else
  (cd "${root}" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 shasum -a 256 >SHA256SUMS)
fi
mkdir -p "${output}"
archive="${output}/$(basename "${root}").tar.gz"
tar --no-xattrs -C "${stage}" -czf "${archive}" "$(basename "${root}")"
printf '%s\n' "${archive}"
