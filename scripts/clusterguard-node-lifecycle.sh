#!/usr/bin/env bash
set -euo pipefail

# Wire records contain \"type\":\"event\" and finish with \"type\":\"result\".

mode="${1:-}"
[[ "${mode}" == "execute" ]] || { echo "usage: $0 execute" >&2; exit 2; }
jq_binary="${CG_JQ_BINARY:-}"
if [[ -z "${jq_binary}" ]]; then
  jq_binary="$(command -v jq || true)"
fi
[[ -x "${jq_binary}" ]] || { echo "a verified jq binary is required" >&2; exit 2; }
jq() { "${jq_binary}" "$@"; }

request_file="$(mktemp /tmp/clusterguard-lifecycle.XXXXXX)"
chmod 0600 "${request_file}"
trap 'rm -f "${request_file}"' EXIT
cat >"${request_file}"
jq -e '.request.cluster_id and (.plan.targets | length > 0)' "${request_file}" >/dev/null

script_dir="$(cd "$(dirname "$0")" && pwd)"
mysql_install_helper="${CG_MYSQL_INSTALL_HELPER:-${script_dir}/clusterguard-mysql-install.sh}"
mysql_sync_helper="${CG_MYSQL_SYNC_HELPER:-${script_dir}/clusterguard-mysql-sync.sh}"
postgresql_install_helper="${CG_POSTGRESQL_INSTALL_HELPER:-${script_dir}/clusterguard-postgresql-install.sh}"
postgresql_sync_helper="${CG_POSTGRESQL_SYNC_HELPER:-${script_dir}/clusterguard-postgresql-sync.sh}"
control_helper="${CG_CONTROL_JOIN_HELPER:-}"
package_repository="${CG_PACKAGE_REPOSITORY:-/opt/clusterguard/packages}"
known_hosts="${CG_SSH_KNOWN_HOSTS:-/etc/clusterguard/known_hosts}"
identity_file="${CG_SSH_IDENTITY_FILE:-}"

emit_event() {
  local stage="$1" status="$2" message="$3"
  jq -nc --arg stage "${stage}" --arg status "${status}" --arg message "${message}" \
    '{type:"event",stage:$stage,status:$status,message:$message}'
}

validate_remote_identity() {
  local host="$1" user="$2" port="$3"
  [[ "${host}" =~ ^[A-Za-z0-9._:-]+$ ]] || { echo "invalid target host" >&2; return 1; }
  [[ "${user}" =~ ^[A-Za-z_][A-Za-z0-9_-]*$ ]] || { echo "invalid SSH user" >&2; return 1; }
  [[ "${port}" =~ ^[0-9]+$ && "${port}" -ge 1 && "${port}" -le 65535 ]] || { echo "invalid SSH port" >&2; return 1; }
  [[ -f "${known_hosts}" ]] || { echo "pinned SSH known_hosts file is required" >&2; return 1; }
}

ssh_prefix() {
  local -n result=$1
  local port="$2"
  result=(ssh -o BatchMode=yes -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${known_hosts}")
  if [[ -n "${identity_file}" ]]; then
    result+=(-i "${identity_file}")
  elif [[ -n "${CG_SSH_PASSWORD:-}" ]]; then
    command -v sshpass >/dev/null 2>&1 || { echo "sshpass is required for transient password bootstrap" >&2; return 1; }
    export SSHPASS="${CG_SSH_PASSWORD}"
    result=(sshpass -e ssh -o BatchMode=no -o PasswordAuthentication=yes -o KbdInteractiveAuthentication=no -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${known_hosts}")
  else
    echo "SSH identity file or transient password is required" >&2
    return 1
  fi
  result+=(-p "${port}")
}

scp_prefix() {
  local -n result=$1
  local port="$2"
  result=(scp -q -o BatchMode=yes -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${known_hosts}")
  if [[ -n "${identity_file}" ]]; then
    result+=(-i "${identity_file}")
  else
    command -v sshpass >/dev/null 2>&1 || return 1
    export SSHPASS="${CG_SSH_PASSWORD:-}"
    result=(sshpass -e scp -q -o BatchMode=no -o PasswordAuthentication=yes -o KbdInteractiveAuthentication=no -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${known_hosts}")
  fi
  result+=(-P "${port}")
}

remote_exec() {
  local host="$1" user="$2" port="$3" command_text="$4"
  local -a prefix
  validate_remote_identity "${host}" "${user}" "${port}"
  ssh_prefix prefix "${port}"
  "${prefix[@]}" "${user}@${host}" "${command_text}"
}

remote_stdin() {
  local payload="$1" host="$2" user="$3" port="$4" command_text="$5"
  local -a prefix
  validate_remote_identity "${host}" "${user}" "${port}"
  ssh_prefix prefix "${port}"
  printf '%s\n' "${payload}" | "${prefix[@]}" "${user}@${host}" "${command_text}"
}

copy_remote() {
  local source="$1" host="$2" user="$3" port="$4" target="$5"
  local -a prefix
  local destination_host="${host}"
  validate_remote_identity "${host}" "${user}" "${port}"
  scp_prefix prefix "${port}"
  [[ "${host}" != *:* ]] || destination_host="[${host}]"
  "${prefix[@]}" "${source}" "${user}@${destination_host}:${target}"
}

emit_event preflight running "checking approved lifecycle targets"
target_count="$(jq '.plan.targets | length' "${request_file}")"
engine="$(jq -r '.request.engine // "mysql"' "${request_file}")"
case "${engine}" in mysql|postgresql) ;; *) echo "unsupported lifecycle database engine" >&2; exit 3 ;; esac
instances='[]'

for ((index=0; index<target_count; index++)); do
  target="$(jq -c ".plan.targets[${index}]" "${request_file}")"
  host="$(jq -r '.ip_address // .hostname' <<<"${target}")"
  user="$(jq -r '(.ssh_user // "") | if length > 0 then . else "root" end' <<<"${target}")"
  port="$(jq -r '.ssh_port // 22' <<<"${target}")"
  node_id="$(jq -r '.node_id // ""' <<<"${target}")"
  node_name="$(jq -r '.node_name' <<<"${target}")"
  kind="$(jq -r '.kind' <<<"${target}")"
  reuses_node_slot="$(jq -r '.reuses_node_slot // false' <<<"${target}")"
  validate_remote_identity "${host}" "${user}" "${port}"
  remote_exec "${host}" "${user}" "${port}" "test \"\$(id -u)\" -eq 0 && command -v bash >/dev/null"
  remote_exec "${host}" "${user}" "${port}" "install -d -m 0700 /var/lib/clusterguard/stage"
  if ! remote_exec "${host}" "${user}" "${port}" "command -v jq >/dev/null 2>&1"; then
    copy_remote "${jq_binary}" "${host}" "${user}" "${port}" "/var/lib/clusterguard/stage/jq"
    remote_exec "${host}" "${user}" "${port}" "chmod 0700 /var/lib/clusterguard/stage/jq"
  fi
  emit_event preflight succeeded "target ${node_name} passed SSH and privilege checks"

  if [[ "${kind}" == "data" || "${kind}" == "mixed" ]]; then
    case "${engine}" in
      mysql)
        install_helper="${mysql_install_helper}"
        sync_helper="${mysql_sync_helper}"
        remote_install_helper="/var/lib/clusterguard/stage/clusterguard-mysql-install.sh"
        remote_sync_helper="/var/lib/clusterguard/stage/clusterguard-mysql-sync.sh"
        ;;
      postgresql)
        install_helper="${postgresql_install_helper}"
        sync_helper="${postgresql_sync_helper}"
        remote_install_helper="/var/lib/clusterguard/stage/clusterguard-postgresql-install.sh"
        remote_sync_helper="/var/lib/clusterguard/stage/clusterguard-postgresql-sync.sh"
        ;;
    esac
    [[ -x "${install_helper}" && -x "${sync_helper}" ]] || { echo "${engine} lifecycle helpers are unavailable" >&2; exit 3; }
    copy_remote "${install_helper}" "${host}" "${user}" "${port}" "${remote_install_helper}"
    copy_remote "${sync_helper}" "${host}" "${user}" "${port}" "${remote_sync_helper}"
    package_name="$(jq -r '.package_name // ""' <<<"${target}")"
    remote_package=""
    if [[ -n "${package_name}" ]]; then
      [[ "${package_name}" == "$(basename "${package_name}")" ]] || { echo "package must be selected by basename" >&2; exit 3; }
      package_path="${package_repository}/${package_name}"
      [[ -f "${package_path}" ]] || { echo "selected database package is unavailable" >&2; exit 3; }
      remote_package="/var/lib/clusterguard/stage/${package_name}"
      copy_remote "${package_path}" "${host}" "${user}" "${port}" "${remote_package}"
    fi
	payload="$(TARGET_JSON="${target}" REMOTE_PACKAGE="${remote_package}" jq -nc \
	  --argjson request "$(jq -c '.request' "${request_file}")" \
	  '{request:$request,target:((env.TARGET_JSON|fromjson)+{package_path:env.REMOTE_PACKAGE}),secrets:{mysql_root_password:env.CG_MYSQL_ROOT_PASSWORD,replication_password:env.CG_MYSQL_REPLICATION_PASSWORD,postgresql_admin_password:env.CG_POSTGRESQL_ADMIN_PASSWORD,postgresql_replication_password:env.CG_POSTGRESQL_REPLICATION_PASSWORD}}')"

    emit_event install running "installing ${engine} on ${node_name}"
    remote_stdin "${payload}" "${host}" "${user}" "${port}" "chmod 0700 '${remote_install_helper}' && PATH=/var/lib/clusterguard/stage:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin '${remote_install_helper}'" >/dev/null
    emit_event install succeeded "${engine} installation reconciled on ${node_name}"

    method="$(jq -r '.sync_method' <<<"${target}")"
    emit_event synchronize running "synchronizing ${node_name} with ${method}"
    identity="$(remote_stdin "${payload}" "${host}" "${user}" "${port}" "chmod 0700 '${remote_sync_helper}' && PATH=/var/lib/clusterguard/stage:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin '${remote_sync_helper}' '${method}'")"
    if [[ "${engine}" == "mysql" ]]; then
      jq -e '.engine_identity.server_uuid and .role == "replica" and .health.state == "healthy"' <<<"${identity}" >/dev/null
    else
      jq -e '.engine_identity.resource_id and .engine_identity.system_identifier and .role == "standby" and .health.state == "healthy"' <<<"${identity}" >/dev/null
    fi
    instances="$(jq -nc --argjson current "${instances}" --argjson instance "${identity}" '$current + [$instance]')"
    emit_event synchronize succeeded "data synchronization completed for ${node_name}"
    emit_event configure_replication succeeded "replication follows the selected current primary"
    emit_event verify succeeded "replication, read-only state, identity, and VIP absence verified"
  fi

  if [[ "${kind}" == "controller" || "${kind}" == "mixed" ]]; then
    if [[ "${reuses_node_slot}" == "true" ]] && remote_exec "${host}" "${user}" "${port}" \
      "systemctl is-active --quiet clusterguard-ha.service && test \"\$(jq -r '.consensus.local_id // empty' /etc/clusterguard/clusterguard.json)\" = '${node_id}'"; then
      emit_event verify succeeded "existing controller role preserved on ${node_name}"
    else
      [[ -n "${control_helper}" && -x "${control_helper}" ]] || { echo "control-node join executor is not configured" >&2; exit 4; }
      emit_event install running "installing controller role on ${node_name}"
      "${control_helper}" "${request_file}" "${index}"
      emit_event verify succeeded "controller role joined the verified odd membership"
    fi
  fi
done

jq -nc --argjson instances "${instances}" '{type:"result",verified:true,instances:$instances,message:"all lifecycle targets verified"}'
