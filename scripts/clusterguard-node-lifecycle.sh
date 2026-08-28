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
cat >"${request_file}"
jq -e '.request.cluster_id and (.plan.targets | length > 0)' "${request_file}" >/dev/null

script_dir="$(cd "$(dirname "$0")" && pwd)"
mysql_install_helper="${CG_MYSQL_INSTALL_HELPER:-${script_dir}/clusterguard-mysql-install.sh}"
mysql_sync_helper="${CG_MYSQL_SYNC_HELPER:-${script_dir}/clusterguard-mysql-sync.sh}"
postgresql_install_helper="${CG_POSTGRESQL_INSTALL_HELPER:-${script_dir}/clusterguard-postgresql-install.sh}"
postgresql_sync_helper="${CG_POSTGRESQL_SYNC_HELPER:-${script_dir}/clusterguard-postgresql-sync.sh}"
package_resolve_helper="${CG_PACKAGE_RESOLVE_HELPER:-${script_dir}/clusterguard-package-resolve.sh}"
adapter_runtime_helper="${CG_ADAPTER_RUNTIME_HELPER:-${script_dir}/clusterguard-adapter-runtime-install.sh}"
control_helper="${CG_CONTROL_JOIN_HELPER:-}"
package_repository="${CG_PACKAGE_REPOSITORY:-/opt/clusterguard/packages}"
known_hosts="${CG_SSH_KNOWN_HOSTS:-/etc/clusterguard/known_hosts}"
identity_file="${CG_SSH_IDENTITY_FILE:-}"
installed_control_indexes=()
ssh_command=()
scp_command=()

rollback_installed_controls() {
  [[ -n "${control_helper}" && -x "${control_helper}" ]] || return 0
  local array_index target_index
  for ((array_index=${#installed_control_indexes[@]}-1; array_index>=0; array_index--)); do
    target_index="${installed_control_indexes[${array_index}]}"
    "${control_helper}" "${request_file}" "${target_index}" rollback >/dev/null 2>&1 ||
      printf 'control-node rollback requires operator review for target index %s\n' "${target_index}" >&2
  done
}

cleanup_lifecycle() {
  local exit_status=$?
  trap - EXIT
  if [[ "${exit_status}" -ne 0 && ${#installed_control_indexes[@]} -gt 0 ]]; then
    rollback_installed_controls
  fi
  rm -f "${request_file}"
  exit "${exit_status}"
}
trap cleanup_lifecycle EXIT

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
  local port="$1"
  ssh_command=(ssh -o BatchMode=yes -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${known_hosts}")
  if [[ -n "${identity_file}" ]]; then
    ssh_command+=(-i "${identity_file}")
  elif [[ -n "${CG_SSH_PASSWORD:-}" ]]; then
    command -v sshpass >/dev/null 2>&1 || { echo "sshpass is required for transient password bootstrap" >&2; return 1; }
    export SSHPASS="${CG_SSH_PASSWORD}"
    ssh_command=(sshpass -e ssh -o BatchMode=no -o PasswordAuthentication=yes -o KbdInteractiveAuthentication=no -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${known_hosts}")
  else
    echo "SSH identity file or transient password is required" >&2
    return 1
  fi
  ssh_command+=(-p "${port}")
}

scp_prefix() {
  local port="$1"
  scp_command=(scp -q -o BatchMode=yes -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${known_hosts}")
  if [[ -n "${identity_file}" ]]; then
    scp_command+=(-i "${identity_file}")
  else
    command -v sshpass >/dev/null 2>&1 || return 1
    export SSHPASS="${CG_SSH_PASSWORD:-}"
    scp_command=(sshpass -e scp -q -o BatchMode=no -o PasswordAuthentication=yes -o KbdInteractiveAuthentication=no -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${known_hosts}")
  fi
  scp_command+=(-P "${port}")
}

remote_exec() {
  local host="$1" user="$2" port="$3" command_text="$4"
  validate_remote_identity "${host}" "${user}" "${port}"
  ssh_prefix "${port}"
  "${ssh_command[@]}" "${user}@${host}" "${command_text}"
}

remote_stdin() {
  local payload="$1" host="$2" user="$3" port="$4" command_text="$5"
  validate_remote_identity "${host}" "${user}" "${port}"
  ssh_prefix "${port}"
  printf '%s\n' "${payload}" | "${ssh_command[@]}" "${user}@${host}" "${command_text}"
}

copy_remote() {
  local source="$1" host="$2" user="$3" port="$4" target="$5"
  local destination_host="${host}"
  validate_remote_identity "${host}" "${user}" "${port}"
  scp_prefix "${port}"
  [[ "${host}" != *:* ]] || destination_host="[${host}]"
  "${scp_command[@]}" "${source}" "${user}@${destination_host}:${target}"
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
  package_name="$(jq -r '.package_name // ""' <<<"${target}")"
  remote_package=""
  case "${engine}" in
    mysql) database_version="$(jq -r '.mysql_version // ""' <<<"${target}")" ;;
    postgresql) database_version="$(jq -r '.postgresql_version // ""' <<<"${target}")" ;;
  esac
  validate_remote_identity "${host}" "${user}" "${port}"
  remote_exec "${host}" "${user}" "${port}" "test \"\$(id -u)\" -eq 0 && command -v bash >/dev/null"
  remote_exec "${host}" "${user}" "${port}" "install -d -m 0700 /var/lib/clusterguard/stage"
	# Every helper consumes the same controller-validated jq path. Always stage it
	# so behavior does not depend on which packages happen to exist on the target.
	copy_remote "${jq_binary}" "${host}" "${user}" "${port}" "/var/lib/clusterguard/stage/jq"
	remote_exec "${host}" "${user}" "${port}" "chmod 0700 /var/lib/clusterguard/stage/jq"
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
    package_manifest="${CG_PACKAGE_MANIFEST:-${package_repository}/manifest.json}"
    if [[ -n "${package_name}" || -f "${package_manifest}" ]]; then
      [[ -x "${package_resolve_helper}" ]] || { echo "package resolver is unavailable" >&2; exit 3; }
      package_path="$(CG_PACKAGE_REPOSITORY="${package_repository}" CG_PACKAGE_MANIFEST="${package_manifest}" CG_JQ_BINARY="${jq_binary}" "${package_resolve_helper}" "${engine}" "${database_version}" "${package_name}")"
      package_name="$(basename "${package_path}")"
      remote_package="/var/lib/clusterguard/stage/${package_name}"
      copy_remote "${package_path}" "${host}" "${user}" "${port}" "${remote_package}"
      emit_event install running "verified ${engine} ${database_version} package selected for ${node_name}"
    fi
	payload="$(TARGET_JSON="${target}" REMOTE_PACKAGE="${remote_package}" jq -nc \
	  --argjson request "$(jq -c '.request' "${request_file}")" \
	  '{request:$request,target:((env.TARGET_JSON|fromjson)+{package_path:env.REMOTE_PACKAGE,mysql_root_remote_host:(env.CG_MYSQL_ROOT_REMOTE_HOST // "")}),secrets:{mysql_root_password:env.CG_MYSQL_ROOT_PASSWORD,mysql_discovery_username:env.CG_MYSQL_DISCOVERY_USERNAME,mysql_discovery_password:env.CG_MYSQL_DISCOVERY_PASSWORD,mysql_operation_username:env.CG_MYSQL_OPERATION_USERNAME,mysql_operation_password:env.CG_MYSQL_OPERATION_PASSWORD,mysql_replication_username:env.CG_MYSQL_REPLICATION_USERNAME,replication_password:env.CG_MYSQL_REPLICATION_PASSWORD,postgresql_admin_password:env.CG_POSTGRESQL_ADMIN_PASSWORD,postgresql_replication_password:env.CG_POSTGRESQL_REPLICATION_PASSWORD}}')"

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

    # A rebuilt host may retain a valid data-node Agent installation while its
    # systemd enablement is lost (for example after an operating-system repair).
    # Reactivate only a complete, already-enrolled Agent; first-time enrollment
    # remains a separate fail-closed step because it needs node-specific policy.
    if remote_exec "${host}" "${user}" "${port}" \
      "test -x /usr/local/bin/clusterguard-agent && test -s /etc/clusterguard/agent.json && test -s /etc/clusterguard/agent.env && systemctl cat clusterguard-agent.service >/dev/null 2>&1"; then
      emit_event install running "reactivating the enrolled data-node agent on ${node_name}"
      remote_exec "${host}" "${user}" "${port}" \
        "systemctl daemon-reload && systemctl enable --now clusterguard-agent.service && systemctl is-active --quiet clusterguard-agent.service"
      emit_event install succeeded "enrolled data-node agent is active on ${node_name}"
      emit_event verify succeeded "data-node agent configuration and boot activation verified"
    fi
  fi

  if [[ "${kind}" == "controller" || "${kind}" == "mixed" ]]; then
    [[ -x "${adapter_runtime_helper}" ]] || { echo "adapter runtime installer is unavailable" >&2; exit 4; }
    remote_adapter_helper="/var/lib/clusterguard/stage/clusterguard-adapter-runtime-install.sh"
    copy_remote "${adapter_runtime_helper}" "${host}" "${user}" "${port}" "${remote_adapter_helper}"
    if [[ -z "${remote_package}" ]]; then
      case "${engine}" in
        mysql)
          runtime_probe="command -v mysql >/dev/null 2>&1 || test -x /usr/local/mysql/bin/mysql || find /opt/clusterguard/mysql -type f -path '*/software/bin/mysql' -perm -u+x -print -quit 2>/dev/null | grep -q ."
          ;;
        postgresql)
          runtime_probe="command -v psql >/dev/null 2>&1 || test -x /usr/local/bin/psql || find /usr/pgsql-* /usr/lib/postgresql /opt/clusterguard/postgresql -type f -path '*/bin/psql' -perm -u+x -print -quit 2>/dev/null | grep -q ."
          ;;
      esac
      if ! remote_exec "${host}" "${user}" "${port}" "${runtime_probe}"; then
        package_manifest="${CG_PACKAGE_MANIFEST:-${package_repository}/manifest.json}"
        [[ -x "${package_resolve_helper}" ]] || { echo "package resolver is unavailable" >&2; exit 4; }
        package_path="$(CG_PACKAGE_REPOSITORY="${package_repository}" CG_PACKAGE_MANIFEST="${package_manifest}" CG_JQ_BINARY="${jq_binary}" "${package_resolve_helper}" "${engine}" "${database_version}" "${package_name}")"
        package_name="$(basename "${package_path}")"
        remote_package="/var/lib/clusterguard/stage/${package_name}"
        copy_remote "${package_path}" "${host}" "${user}" "${port}" "${remote_package}"
        emit_event install running "verified ${engine} ${database_version} adapter package selected for ${node_name}"
      fi
    fi
    runtime_payload="$(TARGET_JSON="${target}" REMOTE_PACKAGE="${remote_package}" jq -nc \
      --argjson request "$(jq -c '.request' "${request_file}")" \
      '{request:$request,target:((env.TARGET_JSON|fromjson)+{package_path:env.REMOTE_PACKAGE})}')"
	    emit_event install running "preparing ${engine} adapter runtime on ${node_name}"
	    runtime_result="$(remote_stdin "${runtime_payload}" "${host}" "${user}" "${port}" "chmod 0700 '${remote_adapter_helper}' && CG_JQ_BINARY=/var/lib/clusterguard/stage/jq '${remote_adapter_helper}'")"
	    jq -e --arg engine "${engine}" '.ready == true and .engine == $engine and (.binary | length > 0)' <<<"${runtime_result}" >/dev/null
	    emit_event install succeeded "${engine} adapter runtime is ready on ${node_name}"
	    emit_event verify succeeded "controller adapter runtime is ready on ${node_name}"
    if [[ "${reuses_node_slot}" == "true" ]] && remote_exec "${host}" "${user}" "${port}" \
      "test \"\$(jq -r '.consensus.local_id // empty' /etc/clusterguard/clusterguard.json)\" = '${node_id}'"; then
      if remote_exec "${host}" "${user}" "${port}" "systemctl is-active --quiet clusterguard-ha.service"; then
        emit_event verify succeeded "existing controller role preserved on ${node_name}"
	      else
	        emit_event install running "reactivating the existing controller role on ${node_name}"
	        remote_exec "${host}" "${user}" "${port}" "systemctl enable --now clusterguard-ha.service && systemctl is-active --quiet clusterguard-ha.service"
	        emit_event install succeeded "existing controller role reactivated on ${node_name}"
	        emit_event verify succeeded "existing controller role reactivated after adapter readiness on ${node_name}"
      fi
    else
      [[ -n "${control_helper}" && -x "${control_helper}" ]] || { echo "control-node join executor is not configured" >&2; exit 4; }
      emit_event install running "installing controller role on ${node_name}"
      installed_control_indexes+=("${index}")
      control_result="$("${control_helper}" "${request_file}" "${index}" join)"
      jq -e '.ready == true and (.new_install | type == "boolean")' <<<"${control_result}" >/dev/null
	      if ! jq -e '.new_install == true' <<<"${control_result}" >/dev/null; then
	        last_installed_index=$((${#installed_control_indexes[@]} - 1))
	        unset "installed_control_indexes[${last_installed_index}]"
	      fi
	      emit_event install succeeded "controller role installed on ${node_name}"
	      emit_event verify succeeded "controller identity is running and ready for the leader to commit Raft membership"
    fi
  fi
done

jq -nc --argjson instances "${instances}" '{type:"result",verified:true,instances:$instances,message:"all lifecycle targets verified"}'
