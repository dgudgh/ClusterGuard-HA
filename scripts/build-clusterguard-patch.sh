#!/usr/bin/env bash
set -euo pipefail
export COPYFILE_DISABLE=1

from_rpm=""
to_rpm=""
signing_key=""
output=""
channel="stable"
state_format=1
update_protocol=1

usage() {
  cat <<'EOF'
用法：build-clusterguard-patch.sh [选项]

  --from-rpm FILE       当前现场版本 RPM，作为自动回退介质
  --to-rpm FILE         目标版本 RPM
  --signing-key FILE    升级包发布私钥（PEM，必须离线保管）
  --output FILE         输出 .cgupgrade 签名升级包
  --channel NAME        发布通道，默认 stable
  --state-format N      元数据格式，默认 1
  --update-protocol N   升级协议，默认 1
EOF
}

die() { printf '升级包构建失败：%s\n' "$*" >&2; exit 1; }
need_value() { (($# >= 2)) && [[ -n "${2:-}" ]] || die "参数 $1 缺少值"; }

while (($#)); do
  case "$1" in
    --from-rpm) need_value "$@"; from_rpm="$2"; shift 2 ;;
    --to-rpm) need_value "$@"; to_rpm="$2"; shift 2 ;;
    --signing-key) need_value "$@"; signing_key="$2"; shift 2 ;;
    --output) need_value "$@"; output="$2"; shift 2 ;;
    --channel) need_value "$@"; channel="$2"; shift 2 ;;
    --state-format) need_value "$@"; state_format="$2"; shift 2 ;;
    --update-protocol) need_value "$@"; update_protocol="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数：$1" ;;
  esac
done

command -v jq >/dev/null 2>&1 || die "需要 jq"
command -v openssl >/dev/null 2>&1 || die "需要 openssl"
command -v tar >/dev/null 2>&1 || die "需要 tar"
[[ -f "${from_rpm}" && ! -L "${from_rpm}" ]] || die "回退 RPM 不存在或不是普通文件"
[[ -f "${to_rpm}" && ! -L "${to_rpm}" ]] || die "目标 RPM 不存在或不是普通文件"
[[ -f "${signing_key}" && ! -L "${signing_key}" ]] || die "补丁签名私钥不存在或不是普通文件"
[[ -n "${output}" ]] || die "必须指定 --output"
[[ "${channel}" =~ ^[A-Za-z0-9._-]+$ ]] || die "发布通道格式无效"
[[ "${state_format}" =~ ^[1-9][0-9]*$ ]] || die "state format 必须为正整数"
[[ "${update_protocol}" =~ ^[1-9][0-9]*$ ]] || die "update protocol 必须为正整数"

parse_rpm_name() {
  local path="$1" name body architecture release version
  name="$(basename "${path}")"
  [[ "${name}" == clusterguard-ha-*.rpm ]] || die "RPM 文件名不符合 clusterguard-ha-VERSION-RELEASE.ARCH.rpm：${name}"
  body="${name#clusterguard-ha-}"
  body="${body%.rpm}"
  architecture="${body##*.}"
  body="${body%.*}"
  release="${body##*-}"
  version="${body%-*}"
  [[ -n "${version}" && -n "${release}" && -n "${architecture}" && "${version}" != "${body}" ]] ||
    die "无法解析 RPM 版本：${name}"
  printf '%s\t%s\t%s\t%s\n' "${version}" "${release}" "${architecture}" "${name}"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

IFS=$'\t' read -r from_version from_release from_arch from_name < <(parse_rpm_name "${from_rpm}")
IFS=$'\t' read -r to_version to_release to_arch to_name < <(parse_rpm_name "${to_rpm}")
[[ "${from_arch}" == "${to_arch}" ]] || die "源 RPM 与目标 RPM 架构不一致"
[[ "${from_version}-${from_release}" != "${to_version}-${to_release}" ]] || die "源版本与目标版本不能相同"

output_dir="$(dirname "${output}")"
mkdir -p "${output_dir}"
output_dir="$(cd "${output_dir}" && pwd)"
output="${output_dir}/$(basename "${output}")"

stage="$(mktemp -d /tmp/clusterguard-patch.XXXXXX)"
trap 'rm -rf "${stage}"' EXIT
root="${stage}/clusterguard-patch"
mkdir -p "${root}/payload"
install -m 0644 "${from_rpm}" "${root}/payload/${from_name}"
install -m 0644 "${to_rpm}" "${root}/payload/${to_name}"

from_sha="$(sha256_file "${root}/payload/${from_name}")"
to_sha="$(sha256_file "${root}/payload/${to_name}")"
created_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
patch_id="cgupgrade-${from_version}-${from_release}-to-${to_version}-${to_release}-${from_arch}"

jq -n \
  --arg product "ClusterGuard HA" \
  --arg patch_id "${patch_id}" \
  --arg channel "${channel}" \
  --arg created_at "${created_at}" \
  --arg from_version "${from_version}" --arg from_release "${from_release}" \
  --arg to_version "${to_version}" --arg to_release "${to_release}" \
  --arg architecture "${from_arch}" \
  --arg from_rpm "${from_name}" --arg from_sha "${from_sha}" \
  --arg to_rpm "${to_name}" --arg to_sha "${to_sha}" \
  --argjson state_format "${state_format}" --argjson update_protocol "${update_protocol}" \
  '{
    schema_version: 1,
    product: $product,
    patch_id: $patch_id,
    channel: $channel,
    created_at: $created_at,
    source: {
      version: $from_version, release: $from_release, rpm_architecture: $architecture,
      state_format: $state_format, update_protocol: $update_protocol,
      rpm: $from_rpm, sha256: $from_sha
    },
    target: {
      version: $to_version, release: $to_release, rpm_architecture: $architecture,
      state_format: $state_format, update_protocol: $update_protocol,
      rpm: $to_rpm, sha256: $to_sha
    },
    compatibility: {
      minimum_state_format: $state_format,
      maximum_state_format: $state_format,
      target_state_format: $state_format,
      update_protocol: $update_protocol
    },
    policy: {
      rolling: true,
      rollback_supported: true,
      database_mutation: false,
      order: ["followers", "data-only", "leader"],
      require_quorum: true,
      require_idle_control_plane: true
    }
  }' >"${root}/PATCH-MANIFEST.json"

openssl dgst -sha256 -sign "${signing_key}" -out "${root}/PATCH-MANIFEST.sig" "${root}/PATCH-MANIFEST.json"
{
  printf '%s  %s\n' "$(sha256_file "${root}/PATCH-MANIFEST.json")" "PATCH-MANIFEST.json"
  printf '%s  %s\n' "${from_sha}" "payload/${from_name}"
  printf '%s  %s\n' "${to_sha}" "payload/${to_name}"
} >"${root}/SHA256SUMS"

temporary_output="${output}.tmp.$$"
rm -f "${temporary_output}"
tar --no-xattrs -C "${stage}" -czf "${temporary_output}" clusterguard-patch
chmod 0644 "${temporary_output}"
mv -f "${temporary_output}" "${output}"

printf 'upgrade_package=%s\n' "${output}"
printf 'patch=%s\n' "${output}"
printf 'source=%s-%s\n' "${from_version}" "${from_release}"
printf 'target=%s-%s\n' "${to_version}" "${to_release}"
printf 'signature=created\n'
printf 'rollback=embedded\n'
