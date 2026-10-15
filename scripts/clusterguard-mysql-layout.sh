#!/usr/bin/env bash
# Shared path/identity rules. Sourcing this file never changes the host.

cg_mysql_safe_path() {
  [[ "$1" =~ ^/[A-Za-z0-9_./+-]+$ && "$1" != / ]] || return 1
  case "/${1#/}/" in */../*|*/./*|*//*) return 1 ;; esac
}

cg_mysql_server_id() {
  local address="$1" port="$2" octet
  [[ "${address}" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ && "${port}" =~ ^[1-9][0-9]{0,4}$ ]] || {
    echo 'MySQL server_id requires an IPv4 address and port, or an explicit server_id' >&2; return 2;
  }
  local -a octets
  IFS=. read -r -a octets <<<"${address}"
  for octet in "${octets[@]}"; do (( 10#${octet} <= 255 )) || return 2; done
  (( port <= 65535 )) || return 2
  octet="${octets[3]}"
  if (( 10#${octet} == 0 )); then printf '%s\n' "${port}"; else printf '%s\n' "$((10#${octet}))${port}"; fi
}

cg_mysql_layout() {
  local root="${1:-/data}" port="$2" software="${3:-}"
  root="${root%/}"
  cg_mysql_safe_path "${root}" && [[ "${port}" =~ ^[1-9][0-9]{0,4}$ ]] && (( port <= 65535 )) || {
    echo 'invalid MySQL data root or port' >&2; return 2;
  }
  cg_mysql_instance="${root}/mysql/${port}"
  cg_mysql_software="${software:-/usr/local/mysql}"
  cg_mysql_software="${cg_mysql_software%/}"
  cg_mysql_safe_path "${cg_mysql_software}" || { echo 'invalid MySQL software directory' >&2; return 2; }
  case "${cg_mysql_instance}/" in "${cg_mysql_software}/"*) echo 'MySQL software directory cannot contain the instance root' >&2; return 2 ;; esac
  local reserved
  for reserved in data conf log binlog relaylog run tmp backup; do
    case "${cg_mysql_software}/" in "${cg_mysql_instance}/${reserved}/"*) echo 'MySQL software directory overlaps instance state' >&2; return 2 ;; esac
  done
  cg_mysql_data="${cg_mysql_instance}/data"
  cg_mysql_conf="${cg_mysql_instance}/conf"
  cg_mysql_log="${cg_mysql_instance}/log"
  cg_mysql_run="${cg_mysql_instance}/run"
}

# Preserve every original non-path parameter, duplicate and comment. The only
# non-path substitutions are the explicitly approved node identity and guard.
cg_mysql_render_cnf() {
  local template="$1" output="$2" root="$3" software="$4" port="$5" server_id="$6"
  [[ -f "${template}" && ! -L "${template}" ]] || { echo 'production MySQL cnf template is unavailable' >&2; return 2; }
  [[ "${server_id}" =~ ^[1-9][0-9]*$ ]] && (( server_id <= 4294967295 )) || return 2
  cg_mysql_layout "${root}" "${port}" "${software}" || return
  awk -v instance="${cg_mysql_instance}" -v software="${cg_mysql_software}" -v port="${port}" -v sid="${server_id}" '
    /^[[:space:]]*[#;]/ { print; next }
    /^[[:space:]]*\[/ { section=$0; gsub(/[[:space:]\r]/,"",section); section=tolower(section); print; next }
    {
      key=$0; sub(/=.*/,"",key); gsub(/[[:space:]]/,"",key); key=tolower(key); gsub(/-/,"_",key)
      value=""; replace=0
      if (section=="[mysqld]" || section=="[client]") {
        if (key=="socket") { value=instance "/run/mysql.sock"; replace=1 }
        if (key=="port") { value=port; replace=1 }
      }
      if (section=="[mysqld]") {
        if (key=="server_id") { value=sid; replace=1 }
        if (key=="basedir") { value=software; replace=1 }
        if (key=="datadir") { value=instance "/data"; replace=1 }
        if (key=="pid_file") { value=instance "/run/mysqld.pid"; replace=1 }
        if (key=="plugin_dir") { value=software "/lib/plugin/"; replace=1 }
        if (key=="log_error") { value=instance "/log/error.log"; replace=1 }
        if (key=="general_log_file") { value=instance "/log/general.log"; replace=1 }
        if (key=="slow_query_log_file") { value=instance "/log/slow.log"; replace=1 }
        if (key=="log_bin") { value=instance "/binlog/mysql-bin"; replace=1 }
        if (key=="log_bin_index") { value=instance "/binlog/mysql-bin.index"; replace=1 }
        if (key=="relay_log") { value=instance "/relaylog/relay-bin"; replace=1; relay=1 }
        if (key=="relay_log_index") { value=instance "/relaylog/relay-bin.index"; replace=1; relay_index=1 }
        if (key=="tmpdir") { value=instance "/tmp"; replace=1; tmp=1 }
        if (key=="read_only" || key=="super_read_only") { value="ON"; replace=1 }
      }
      if (replace) { prefix=$0; sub(/=.*/,"",prefix); print prefix "=" value }
      else print
    }
    END {
      print "\n[mysqld]"
      print "# ClusterGuard managed paths and read-only startup; writer lease controls writes."
      if (!relay) print "relay_log=" instance "/relaylog/relay-bin"
      if (!relay_index) print "relay_log_index=" instance "/relaylog/relay-bin.index"
      if (!tmp) print "tmpdir=" instance "/tmp"
      print "read_only=ON\nsuper_read_only=ON"
    }
  ' "${template}" >"${output}"
}
