#!/usr/bin/env bash
set -euo pipefail
umask 077

update_root="/var/lib/clusterguard/updates"
history_root="/var/lib/clusterguard/update-history"
retained_versions="${CG_UPDATE_RETAINED_VERSIONS:-3}"
protected_patch_id=""

die() { printf '升级包清理失败：%s\n' "$*" >&2; exit 1; }
need_value() { (($# >= 2)) && [[ -n "${2:-}" ]] || die "参数 $1 缺少值"; }

usage() {
  cat <<'EOF'
ClusterGuard HA 升级包保留清理器

选项：
  --update-root DIR          控制节点升级包目录
  --history-root DIR         节点回退材料目录
  --retain-versions COUNT    保留最近版本数，默认 3
  --protect PATCH_ID         无条件保护当前升级包
EOF
}

while (($#)); do
  case "$1" in
    --update-root) need_value "$@"; update_root="$2"; shift 2 ;;
    --history-root) need_value "$@"; history_root="$2"; shift 2 ;;
    --retain-versions) need_value "$@"; retained_versions="$2"; shift 2 ;;
    --protect) need_value "$@"; protected_patch_id="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数：$1" ;;
  esac
done

valid_root() {
  [[ "$1" =~ ^/[A-Za-z0-9._/-]+$ && "$1" != *"//"* && "$1" != *"/../"* && "$1" != */.. ]]
}

valid_patch_id() {
  [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]]
}

valid_root "${update_root}" || die "升级包目录必须是无空格、无相对跳转的绝对路径"
valid_root "${history_root}" || die "回退材料目录必须是无空格、无相对跳转的绝对路径"
[[ "${retained_versions}" =~ ^[1-9][0-9]*$ ]] || die "保留版本数必须为正整数"
[[ -z "${protected_patch_id}" ]] || valid_patch_id "${protected_patch_id}" || die "受保护升级包 ID 无效"

jq_binary="$(command -v jq 2>/dev/null || true)"
if [[ -z "${jq_binary}" && -x /usr/local/libexec/jq-linux-amd64 ]]; then
  jq_binary=/usr/local/libexec/jq-linux-amd64
fi

newest_directories() {
  local root="$1" directory patch_id modified
  [[ -d "${root}" && ! -L "${root}" ]] || return 0
  for directory in "${root}"/*; do
    [[ -d "${directory}" && ! -L "${directory}" ]] || continue
    patch_id="${directory##*/}"
    valid_patch_id "${patch_id}" || continue
    modified="$(stat -c '%Y' "${directory}" 2>/dev/null || stat -f '%m' "${directory}")" || continue
    printf '%s\t%s\n' "${modified}" "${patch_id}"
  done | LC_ALL=C sort -t $'\t' -k1,1nr -k2,2r | cut -f2-
}

update_directory_is_prunable() {
  local directory="$1" status_file="${directory}/status.json" summary status maintenance verification
  [[ -e "${status_file}" ]] || return 0
  [[ -f "${status_file}" && ! -L "${status_file}" && -n "${jq_binary}" ]] || return 1
  summary="$("${jq_binary}" -er '[.status // "", (.maintenance_active // false), (.verification_required // false)] | @tsv' "${status_file}" 2>/dev/null)" || return 1
  IFS=$'\t' read -r status maintenance verification <<<"${summary}"
  [[ "${maintenance}" == false && "${verification}" == false ]] || return 1
  case "${status}" in
    ""|uploaded|planned|succeeded|failed|rolled_back) return 0 ;;
    queued|running) return 1 ;;
    *) return 1 ;;
  esac
}

prune_root() {
  local root="$1" kind="$2" patch_id directory retained=0 removed=0
  while IFS= read -r patch_id; do
    [[ -n "${patch_id}" ]] || continue
    valid_patch_id "${patch_id}" || continue
    directory="${root}/${patch_id}"
    [[ -d "${directory}" && ! -L "${directory}" ]] || continue

    if [[ "${kind}" == update ]] && ! update_directory_is_prunable "${directory}"; then
      printf '保护未结束或需要复核的升级包：%s\n' "${patch_id}"
      continue
    fi

    retained=$((retained + 1))
    if ((retained <= retained_versions)) || [[ "${patch_id}" == "${protected_patch_id}" ]]; then
      continue
    fi
    rm -rf -- "${directory}"
    removed=$((removed + 1))
    printf '已清理旧升级材料：%s\n' "${directory}"
  done < <(newest_directories "${root}")
  printf '%s：保留最近 %s 个版本，清理 %s 个目录\n' "${root}" "${retained_versions}" "${removed}"
}

prune_root "${update_root}" update
prune_root "${history_root}" history
