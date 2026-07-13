#!/usr/bin/env bash
set -euo pipefail

bundle_dir=""
role=""
node_name=""
node_id=""
config_file=""
environment_file=""
agent_config_file=""
assets_dir=""

usage() {
  cat <<'EOF'
usage: clusterguard-preflight.sh --bundle-dir DIR --role controller|data|mixed
       --node-name NAME --node-id UUID [--config FILE] --env-file FILE
       [--agent-config FILE] [--assets-dir DIR]
EOF
}

while (($#)); do
  case "$1" in
    --bundle-dir) bundle_dir="${2:-}"; shift 2 ;;
    --role) role="${2:-}"; shift 2 ;;
    --node-name) node_name="${2:-}"; shift 2 ;;
    --node-id) node_id="${2:-}"; shift 2 ;;
    --config) config_file="${2:-}"; shift 2 ;;
    --env-file) environment_file="${2:-}"; shift 2 ;;
    --agent-config) agent_config_file="${2:-}"; shift 2 ;;
    --assets-dir) assets_dir="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown preflight argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[[ -d "${bundle_dir}" ]] || { echo "bundle directory is unavailable" >&2; exit 2; }
case "${role}" in controller|data|mixed) ;; *) echo "role must be controller, data, or mixed" >&2; exit 2 ;; esac
[[ "${node_name}" =~ ^[a-z][a-z0-9-]{2,62}$ ]] || { echo "node name must be a stable lowercase global name" >&2; exit 2; }
[[ "${node_id}" =~ ^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$ ]] || { echo "node ID must be a platform UUID" >&2; exit 2; }
[[ -f "${environment_file}" && ! -L "${environment_file}" ]] || { echo "protected environment file is required" >&2; exit 2; }
if [[ -n "${assets_dir}" ]]; then
  [[ -d "${assets_dir}" && ! -L "${assets_dir}" ]] || { echo "runtime assets directory is invalid" >&2; exit 2; }
  [[ -z "$(find "${assets_dir}" -mindepth 1 -type l -print -quit)" ]] || { echo "runtime assets must not contain symlinks" >&2; exit 3; }
  [[ -z "$(find "${assets_dir}" -mindepth 3 -print -quit)" ]] || { echo "runtime assets must use one supported subdirectory level" >&2; exit 3; }
  while IFS= read -r -d '' asset; do
    relative="${asset#"${assets_dir}"/}"
    case "${relative}" in
      tls/*.crt|tls/*.key|ssh/*known_hosts|ssh/*_ed25519|mysql/*-client.cnf) ;;
      *) echo "unsupported runtime asset: ${relative}" >&2; exit 3 ;;
    esac
  done < <(find "${assets_dir}" -mindepth 2 -maxdepth 2 -type f -print0 | sort -z)
fi

required=(
  bin/clusterguard
  bin/cgctl
  bin/clusterguard-agent
  scripts/clusterguard-node-lifecycle.sh
  scripts/clusterguard-mysql-install.sh
  scripts/clusterguard-mysql-sync.sh
  packaging/systemd/clusterguard-ha.service
  packaging/systemd/clusterguard-agent.service
  packaging/systemd/clusterguard-agent-reconcile.service
  packaging/systemd/clusterguard-agent-reconcile.timer
  packaging/logrotate/clusterguard-ha
)
if [[ "${role}" == "controller" || "${role}" == "mixed" ]]; then
  [[ -f "${config_file}" && ! -L "${config_file}" ]] || { echo "controller configuration file is required" >&2; exit 2; }
fi
if [[ "${role}" == "data" || "${role}" == "mixed" ]]; then
  [[ -f "${agent_config_file}" && ! -L "${agent_config_file}" ]] || { echo "data-node agent configuration file is required" >&2; exit 2; }
fi
for relative in "${required[@]}"; do
  [[ -f "${bundle_dir}/${relative}" && ! -L "${bundle_dir}/${relative}" ]] || {
    echo "bundle is missing ${relative}" >&2
    exit 3
  }
done

jq_binary="${CG_JQ_BINARY:-}"
if [[ -z "${jq_binary}" && -x "${bundle_dir}/bin/jq" ]]; then
  jq_binary="${bundle_dir}/bin/jq"
fi
if [[ -z "${jq_binary}" ]]; then
  jq_binary="$(command -v jq || true)"
fi
[[ -x "${jq_binary}" ]] || { echo "jq is required for JSON configuration validation" >&2; exit 3; }
if [[ -n "${config_file}" ]]; then
  "${jq_binary}" -e 'type == "object"' "${config_file}" >/dev/null
fi
if [[ -n "${agent_config_file}" ]]; then
  "${jq_binary}" -e 'type == "object"' "${agent_config_file}" >/dev/null
fi

if [[ -f "${bundle_dir}/SHA256SUMS" ]]; then
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "${bundle_dir}" && sha256sum -c SHA256SUMS >/dev/null)
  elif command -v shasum >/dev/null 2>&1; then
    (cd "${bundle_dir}" && shasum -a 256 -c SHA256SUMS >/dev/null)
  else
    echo "SHA-256 verification tool is required" >&2
    exit 3
  fi
fi

if [[ "${CG_PREFLIGHT_SKIP_RUNTIME:-0}" != "1" ]]; then
  for command_name in bash find install systemctl; do
    command -v "${command_name}" >/dev/null 2>&1 || { echo "required command ${command_name} is unavailable" >&2; exit 3; }
  done
  [[ -d /run/systemd/system ]] || { echo "systemd is not running" >&2; exit 3; }
  timedatectl show -p NTPSynchronized --value 2>/dev/null | grep -Eq '^(yes|true|1)$' || {
    echo "warning: host clock synchronization is not confirmed" >&2
  }
fi

printf 'preflight: pass\n'
printf 'node_name: %s\n' "${node_name}"
printf 'node_id: %s\n' "${node_id}"
printf 'role: %s\n' "${role}"
