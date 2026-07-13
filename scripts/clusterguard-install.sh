#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
bundle_dir=""
role=""
node_name=""
node_id=""
config_file=""
environment_file=""
agent_config_file=""
assets_dir=""
execute=false

usage() {
  cat <<'EOF'
usage: clusterguard-install.sh --bundle-dir DIR --role controller|data|mixed
       --node-name NAME --node-id UUID [--config FILE] --env-file FILE
       [--agent-config FILE] [--assets-dir DIR] [--execute]

The default mode performs preflight and prints the installation plan. No host
state is changed until --execute is supplied.
EOF
}

preflight_arguments=()
while (($#)); do
  case "$1" in
    --bundle-dir) bundle_dir="${2:-}"; preflight_arguments+=("$1" "$2"); shift 2 ;;
    --role) role="${2:-}"; preflight_arguments+=("$1" "$2"); shift 2 ;;
    --node-name) node_name="${2:-}"; preflight_arguments+=("$1" "$2"); shift 2 ;;
    --node-id) node_id="${2:-}"; preflight_arguments+=("$1" "$2"); shift 2 ;;
    --config) config_file="${2:-}"; preflight_arguments+=("$1" "$2"); shift 2 ;;
    --env-file) environment_file="${2:-}"; preflight_arguments+=("$1" "$2"); shift 2 ;;
    --agent-config) agent_config_file="${2:-}"; preflight_arguments+=("$1" "$2"); shift 2 ;;
    --assets-dir) assets_dir="${2:-}"; preflight_arguments+=("$1" "$2"); shift 2 ;;
    --execute) execute=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown installer argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

preflight="${script_dir}/clusterguard-preflight.sh"
if [[ ! -x "${preflight}" ]]; then
  preflight="${bundle_dir}/scripts/clusterguard-preflight.sh"
fi
[[ -x "${preflight}" ]] || { echo "ClusterGuard preflight helper is unavailable" >&2; exit 2; }
"${preflight}" "${preflight_arguments[@]}"

cat <<EOF

ClusterGuard HA installation plan
mode: $([[ "${execute}" == "true" ]] && printf execute || printf preflight)
node: ${node_name} (${node_id})
role: ${role}
configuration: ${config_file:-not-required}
agent configuration: ${agent_config_file:-not-required}
runtime assets: ${assets_dir:-not-provided}
services: $([[ "${role}" == "controller" ]] && printf controller || [[ "${role}" == "data" ]] && printf agent || printf 'controller, agent, reconcile timer')
EOF

if [[ "${execute}" != "true" ]]; then
  printf '\nNo files were changed. Re-run the same command with --execute.\n'
  exit 0
fi

install_root="${CG_INSTALL_ROOT:-}"
if [[ -z "${install_root}" && "$(id -u)" -ne 0 ]]; then
  echo "real installation must run as root" >&2
  exit 4
fi
systemctl_binary="${CG_SYSTEMCTL:-systemctl}"

target_path() {
  printf '%s%s' "${install_root}" "$1"
}

install_file() {
  local mode="$1" source="$2" destination="$3"
  local target
  target="$(target_path "${destination}")"
  mkdir -p "$(dirname "${target}")"
  install -m "${mode}" "${source}" "${target}"
}

if [[ -z "${install_root}" ]]; then
  getent group clusterguard >/dev/null 2>&1 || groupadd --system clusterguard
  id clusterguard >/dev/null 2>&1 || useradd --system --gid clusterguard --home-dir /var/lib/clusterguard --shell /sbin/nologin clusterguard
fi

install_file 0755 "${bundle_dir}/bin/clusterguard" /usr/local/bin/clusterguard
install_file 0755 "${bundle_dir}/bin/cgctl" /usr/local/bin/cgctl
install_file 0755 "${bundle_dir}/bin/clusterguard-agent" /usr/local/bin/clusterguard-agent
for helper in clusterguard-node-lifecycle.sh clusterguard-mysql-install.sh clusterguard-mysql-sync.sh clusterguard-preflight.sh clusterguard-smoke.sh; do
  install_file 0750 "${bundle_dir}/scripts/${helper}" "/usr/local/libexec/${helper}"
done
if [[ -x "${bundle_dir}/bin/jq" ]]; then
  install_file 0755 "${bundle_dir}/bin/jq" /usr/local/libexec/jq-linux-amd64
fi

install -d -m 0750 "$(target_path /etc/clusterguard)" "$(target_path /var/lib/clusterguard)" "$(target_path /var/log/clusterguard)" "$(target_path /var/lib/clusterguard-agent)"
install_file 0640 "${environment_file}" /etc/clusterguard/clusterguard.env
install_file 0600 "${environment_file}" /etc/clusterguard/agent.env
printf '{"resource_id":"%s","node_name":"%s","role":"%s"}\n' "${node_id}" "${node_name}" "${role}" >"$(target_path /etc/clusterguard/node.json)"
chmod 0640 "$(target_path /etc/clusterguard/node.json)"

if [[ -n "${assets_dir}" ]]; then
  while IFS= read -r -d '' asset; do
    relative="${asset#"${assets_dir}"/}"
    case "${relative}" in
      tls/*.crt|ssh/*known_hosts) mode=0644 ;;
      tls/*.key|ssh/*_ed25519) mode=0640 ;;
      mysql/*-client.cnf) mode=0600 ;;
      *) echo "unsupported runtime asset: ${relative}" >&2; exit 3 ;;
    esac
    install_file "${mode}" "${asset}" "/etc/clusterguard/${relative}"
  done < <(find "${assets_dir}" -mindepth 2 -maxdepth 2 -type f -print0 | sort -z)
fi

if [[ "${role}" == "controller" || "${role}" == "mixed" ]]; then
  install_file 0640 "${config_file}" /etc/clusterguard/clusterguard.json
  install_file 0644 "${bundle_dir}/packaging/systemd/clusterguard-ha.service" /etc/systemd/system/clusterguard-ha.service
fi
if [[ "${role}" == "data" || "${role}" == "mixed" ]]; then
  install_file 0640 "${agent_config_file}" /etc/clusterguard/agent.json
  install_file 0644 "${bundle_dir}/packaging/systemd/clusterguard-agent.service" /etc/systemd/system/clusterguard-agent.service
  install_file 0644 "${bundle_dir}/packaging/systemd/clusterguard-agent-reconcile.service" /etc/systemd/system/clusterguard-agent-reconcile.service
  install_file 0644 "${bundle_dir}/packaging/systemd/clusterguard-agent-reconcile.timer" /etc/systemd/system/clusterguard-agent-reconcile.timer
fi
install_file 0644 "${bundle_dir}/packaging/logrotate/clusterguard-ha" /etc/logrotate.d/clusterguard-ha

if [[ -z "${install_root}" ]]; then
  chown root:clusterguard /etc/clusterguard
  chown root:clusterguard /etc/clusterguard/clusterguard.env /etc/clusterguard/node.json
  if [[ "${role}" == "controller" || "${role}" == "mixed" ]]; then
    chown root:clusterguard /etc/clusterguard/clusterguard.json
  fi
  [[ ! -d /etc/clusterguard/tls ]] || chown -R root:clusterguard /etc/clusterguard/tls
  [[ ! -d /etc/clusterguard/ssh ]] || chown -R root:clusterguard /etc/clusterguard/ssh
  chown -R clusterguard:clusterguard /var/lib/clusterguard /var/log/clusterguard
fi

"${systemctl_binary}" daemon-reload
if [[ "${role}" == "controller" || "${role}" == "mixed" ]]; then
  "${systemctl_binary}" enable --now clusterguard-ha.service
fi
if [[ "${role}" == "data" || "${role}" == "mixed" ]]; then
  "${systemctl_binary}" enable --now clusterguard-agent.service
  "${systemctl_binary}" enable --now clusterguard-agent-reconcile.timer
fi

printf '\ninstallation: complete\n'
printf 'node_name: %s\n' "${node_name}"
printf 'node_id: %s\n' "${node_id}"
