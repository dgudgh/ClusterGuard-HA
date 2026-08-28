#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
bundle_dir=""
role=""
node_name=""
node_id=""
config_file=""
environment_file=""
agent_environment_file=""
agent_config_file=""
assets_dir=""
execute=false
activate_agent_reconcile=false

usage() {
  cat <<'EOF'
usage: clusterguard-install.sh --bundle-dir DIR --role controller|data|mixed
       --node-name NAME --node-id UUID [--config FILE] --env-file FILE
       [--agent-env-file FILE] [--agent-config FILE] [--assets-dir DIR]
       [--activate-agent-reconcile] [--execute]

The default mode performs preflight and prints the installation plan. No host
state is changed until --execute is supplied.

Agent reconciliation is installed but kept disabled by default. Activate it
only after every managed VIP has canonical owner metadata and a majority lease.
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
    --agent-env-file) agent_environment_file="${2:-}"; preflight_arguments+=("$1" "$2"); shift 2 ;;
    --agent-config) agent_config_file="${2:-}"; preflight_arguments+=("$1" "$2"); shift 2 ;;
    --assets-dir) assets_dir="${2:-}"; preflight_arguments+=("$1" "$2"); shift 2 ;;
    --activate-agent-reconcile) activate_agent_reconcile=true; shift ;;
    --execute) execute=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown installer argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ -z "${agent_environment_file}" ]]; then
  agent_environment_file="${environment_file}"
fi

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
agent environment: ${agent_environment_file:-not-required}
runtime assets: ${assets_dir:-not-provided}
services: $([[ "${role}" == "controller" ]] && printf controller || [[ "${role}" == "data" ]] && printf agent || printf 'controller, agent')
agent reconciliation: $([[ "${activate_agent_reconcile}" == "true" ]] && printf activated || printf 'installed, deferred until VIP bootstrap completes')
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
for helper in clusterguard-node-lifecycle.sh clusterguard-package-resolve.sh clusterguard-adapter-runtime-install.sh clusterguard-control-join.sh clusterguard-mysql-install.sh clusterguard-mysql-sync.sh clusterguard-mysql-probe-cleanup.sh clusterguard-mysql-qualification.sh clusterguard-postgresql-build.sh clusterguard-postgresql-install.sh clusterguard-postgresql-sync.sh clusterguard-preflight.sh clusterguard-smoke.sh clusterguard-ha-matrix.sh clusterguard-cluster-shutdown.sh clusterguard-cluster-restore.sh clusterguard-cluster-finalize.sh; do
	install_file 0755 "${bundle_dir}/scripts/${helper}" "/usr/local/libexec/${helper}"
done
install_file 0750 "${bundle_dir}/scripts/clusterguard-agent-stdio.sh" /usr/local/libexec/clusterguard-agent-stdio
if [[ -x "${bundle_dir}/bin/jq" ]]; then
  install_file 0755 "${bundle_dir}/bin/jq" /usr/local/libexec/jq-linux-amd64
fi

install -d -m 0751 "$(target_path /etc/clusterguard)"
install -d -m 0750 "$(target_path /var/lib/clusterguard)" "$(target_path /var/lib/clusterguard-agent)"
install -d -m 0751 "$(target_path /var/log/clusterguard)"
install -d -m 0700 "$(target_path /etc/clusterguard/power-snapshots)"
install_file 0640 "${environment_file}" /etc/clusterguard/clusterguard.env
install_file 0600 "${agent_environment_file}" /etc/clusterguard/agent.env
printf '{"resource_id":"%s","node_name":"%s","role":"%s"}\n' "${node_id}" "${node_name}" "${role}" >"$(target_path /etc/clusterguard/node.json)"
chmod 0640 "$(target_path /etc/clusterguard/node.json)"

if [[ -n "${assets_dir}" ]]; then
  while IFS= read -r -d '' asset; do
    relative="${asset#"${assets_dir}"/}"
    case "${relative}" in
      ._*|*/._*|.DS_Store|*/.DS_Store) continue ;;
      tls/*.crt|pki/*.crt|ssh/*known_hosts) mode=0644 ;;
      tls/*.key|pki/*.key) mode=0640 ;;
      ssh/*_ed25519) mode=0600 ;;
      mysql/*-client.cnf|postgresql/*.pass) mode=0600 ;;
      fencing/clusterguard-fencer) mode=0750 ;;
      fencing/*) mode=0640 ;;
      *) echo "unsupported runtime asset: ${relative}" >&2; exit 3 ;;
    esac
    if [[ "${relative}" == "fencing/clusterguard-fencer" ]]; then
      install_file "${mode}" "${asset}" /usr/local/libexec/clusterguard-fencer
    else
      install_file "${mode}" "${asset}" "/etc/clusterguard/${relative}"
    fi
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
# Planned cluster shutdown restore flow: both units are enabled on every node;
# their scripts are no-ops when the per-cluster snapshot directory is empty.
install_file 0644 "${bundle_dir}/packaging/systemd/clusterguard-cluster-restore.service" /etc/systemd/system/clusterguard-cluster-restore.service
install_file 0644 "${bundle_dir}/packaging/systemd/clusterguard-cluster-finalize.service" /etc/systemd/system/clusterguard-cluster-finalize.service
install_file 0644 "${bundle_dir}/packaging/logrotate/clusterguard-ha" /etc/logrotate.d/clusterguard-ha

if [[ -z "${install_root}" ]]; then
  chown root:clusterguard /etc/clusterguard
  chown root:clusterguard /etc/clusterguard/clusterguard.env /etc/clusterguard/node.json
  if [[ "${role}" == "controller" || "${role}" == "mixed" ]]; then
    chown root:clusterguard /etc/clusterguard/clusterguard.json
  fi
  [[ ! -d /etc/clusterguard/tls ]] || chown -R root:clusterguard /etc/clusterguard/tls
  [[ ! -d /etc/clusterguard/pki ]] || chown -R root:clusterguard /etc/clusterguard/pki
  [[ ! -d /etc/clusterguard/ssh ]] || chown -R root:clusterguard /etc/clusterguard/ssh
  [[ ! -d /etc/clusterguard/fencing ]] || chown -R root:clusterguard /etc/clusterguard/fencing
  [[ ! -f /usr/local/libexec/clusterguard-fencer ]] || chown root:clusterguard /usr/local/libexec/clusterguard-fencer
  if [[ -d /etc/clusterguard/ssh ]]; then
    find /etc/clusterguard/ssh -maxdepth 1 -type f -name '*_ed25519' -exec chown clusterguard:clusterguard {} +
    find /etc/clusterguard/ssh -maxdepth 1 -type f -name '*_ed25519' -exec chmod 0600 {} +
	find /etc/clusterguard/ssh -maxdepth 1 -type f -name '*known_hosts' -exec chown root:clusterguard {} +
	find /etc/clusterguard/ssh -maxdepth 1 -type f -name '*known_hosts' -exec chmod 0644 {} +
  fi
  chown -R clusterguard:clusterguard /var/lib/clusterguard
  chown clusterguard:clusterguard /var/log/clusterguard
  find /var/log/clusterguard -maxdepth 1 -type f -exec chown clusterguard:clusterguard {} +
fi

"${systemctl_binary}" daemon-reload
if [[ "${role}" == "controller" || "${role}" == "mixed" ]]; then
  "${systemctl_binary}" enable clusterguard-ha.service
  "${systemctl_binary}" restart clusterguard-ha.service
fi
if [[ "${role}" == "data" || "${role}" == "mixed" ]]; then
  "${systemctl_binary}" enable clusterguard-agent.service
  "${systemctl_binary}" restart clusterguard-agent.service
  if [[ "${activate_agent_reconcile}" == "true" ]]; then
    "${systemctl_binary}" enable --now clusterguard-agent-reconcile.timer
  else
    "${systemctl_binary}" disable --now clusterguard-agent-reconcile.timer
  fi
fi
"${systemctl_binary}" enable clusterguard-cluster-restore.service clusterguard-cluster-finalize.service

printf '\ninstallation: complete\n'
printf 'node_name: %s\n' "${node_name}"
printf 'node_id: %s\n' "${node_id}"
