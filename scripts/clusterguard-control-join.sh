#!/usr/bin/env bash
set -euo pipefail

request_file="${1:-}"
target_index="${2:-}"
operation="${3:-join}"
[[ -f "${request_file}" && "${target_index}" =~ ^[0-9]+$ ]] || {
  echo "usage: clusterguard-control-join.sh REQUEST_FILE TARGET_INDEX [join|rollback]" >&2
  exit 2
}
case "${operation}" in join|rollback) ;; *) echo "control-node operation must be join or rollback" >&2; exit 2 ;; esac

jq_binary="${CG_JQ_BINARY:-$(command -v jq || true)}"
[[ -x "${jq_binary}" ]] || { echo "a verified jq binary is required" >&2; exit 2; }
source_config="${CG_CONTROL_CONFIG:-/etc/clusterguard/clusterguard.json}"
source_environment="${CG_CONTROL_ENVIRONMENT:-/etc/clusterguard/clusterguard.env}"
known_hosts="${CG_SSH_KNOWN_HOSTS:-/etc/clusterguard/known_hosts}"
identity_file="${CG_SSH_IDENTITY_FILE:-}"
ssh_command=()
scp_command=()
[[ -f "${source_config}" && -f "${source_environment}" ]] || { echo "local controller configuration and environment are required" >&2; exit 3; }
[[ -f "${known_hosts}" ]] || { echo "pinned SSH known_hosts file is required" >&2; exit 3; }

target="$(${jq_binary} -c ".plan.targets[${target_index}]" "${request_file}")"
node_id="$(${jq_binary} -r '.node_id // ""' <<<"${target}")"
node_name="$(${jq_binary} -r '.node_name // ""' <<<"${target}")"
target_hostname="$(${jq_binary} -r '.hostname // ""' <<<"${target}")"
target_ip="$(${jq_binary} -r '.ip_address // ""' <<<"${target}")"
host="$(${jq_binary} -r '.ip_address // .hostname // ""' <<<"${target}")"
ssh_user="$(${jq_binary} -r '(.ssh_user // "") | if length > 0 then . else "root" end' <<<"${target}")"
ssh_port="$(${jq_binary} -r '.ssh_port // 22' <<<"${target}")"
kind="$(${jq_binary} -r '.kind // ""' <<<"${target}")"
[[ "${node_id}" =~ ^[0-9A-Fa-f-]{36}$ && "${node_name}" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "planned controller identity is invalid" >&2; exit 3; }
[[ "${kind}" == "controller" || "${kind}" == "mixed" ]] || { echo "target is not a controller role" >&2; exit 3; }
[[ "${host}" =~ ^[A-Za-z0-9._:-]+$ && "${ssh_user}" =~ ^[A-Za-z_][A-Za-z0-9_-]*$ ]] || { echo "target controller transport is invalid" >&2; exit 3; }
[[ "${ssh_port}" =~ ^[0-9]+$ && "${ssh_port}" -ge 1 && "${ssh_port}" -le 65535 ]] || { echo "target SSH port is invalid" >&2; exit 3; }

api_issuer_cert="${CG_CONTROL_API_ISSUER_CERT:-}"
api_issuer_key="${CG_CONTROL_API_ISSUER_KEY:-}"
raft_issuer_cert="${CG_CONTROL_RAFT_ISSUER_CERT:-}"
raft_issuer_key="${CG_CONTROL_RAFT_ISSUER_KEY:-}"
certificate_validity_days="${CG_CONTROL_CERT_VALIDITY_DAYS:-825}"
issuer_values=("${api_issuer_cert}" "${api_issuer_key}" "${raft_issuer_cert}" "${raft_issuer_key}")
issuer_count=0
for issuer_value in "${issuer_values[@]}"; do [[ -z "${issuer_value}" ]] || issuer_count=$((issuer_count + 1)); done
[[ "${issuer_count}" -eq 0 || "${issuer_count}" -eq 4 ]] || { echo "control API and Raft issuer certificate/key paths must be configured together" >&2; exit 3; }
[[ "${certificate_validity_days}" =~ ^[0-9]+$ && "${certificate_validity_days}" -ge 1 && "${certificate_validity_days}" -le 3650 ]] || { echo "control certificate validity is invalid" >&2; exit 3; }
openssl_binary="${CG_OPENSSL_BINARY:-$(command -v openssl || true)}"
[[ -x "${openssl_binary}" ]] || { echo "openssl is required for control-node identity validation" >&2; exit 3; }

raft_port="$(python_port="$(${jq_binary} -r '.consensus.advertise_address' "${source_config}")"; printf '%s' "${python_port##*:}")"
api_port="$(python_port="$(${jq_binary} -r '.http_address' "${source_config}")"; printf '%s' "${python_port##*:}")"
[[ "${raft_port}" =~ ^[0-9]+$ && "${api_port}" =~ ^[0-9]+$ ]] || { echo "controller ports are invalid" >&2; exit 3; }
api_cert_path="$(${jq_binary} -r '.tls_cert_file // ""' "${source_config}")"
api_key_path="$(${jq_binary} -r '.tls_key_file // ""' "${source_config}")"
api_ca_path="$(${jq_binary} -r '.tls_ca_file // ""' "${source_config}")"
raft_cert_path="$(${jq_binary} -r '.consensus.tls_cert_file // ""' "${source_config}")"
raft_key_path="$(${jq_binary} -r '.consensus.tls_key_file // ""' "${source_config}")"
raft_ca_path="$(${jq_binary} -r '.consensus.tls_ca_file // ""' "${source_config}")"
api_scheme="http"
if [[ -n "${api_cert_path}" ]]; then
  api_scheme="https"
fi

for control_path in "${api_cert_path}" "${api_key_path}" "${api_ca_path}" "${raft_cert_path}" "${raft_key_path}" "${raft_ca_path}"; do
  [[ -z "${control_path}" || ("${control_path}" == /etc/clusterguard/* && -f "${control_path}" && ! -L "${control_path}") ]] || {
    echo "control TLS assets must be regular files below /etc/clusterguard" >&2
    exit 3
  }
done

certificate_covers_host() {
  local certificate="$1" identity="$2"
  if [[ "${identity}" == *:* || "${identity}" =~ ^[0-9]+(\.[0-9]+){3}$ ]]; then
    "${openssl_binary}" x509 -in "${certificate}" -noout -checkip "${identity}" >/dev/null 2>&1
  else
    "${openssl_binary}" x509 -in "${certificate}" -noout -checkhost "${identity}" >/dev/null 2>&1
  fi
}

validate_issuer() {
  local issuer_cert="$1" issuer_key="$2" trust_bundle="$3" label="$4" cert_public key_public
  for path in "${issuer_cert}" "${issuer_key}"; do
    [[ "${path}" == /etc/clusterguard/* && -f "${path}" && ! -L "${path}" ]] || { echo "${label} issuer must be a regular file below /etc/clusterguard" >&2; return 1; }
  done
  [[ -z "$(find "${issuer_key}" -perm /007 -print -quit 2>/dev/null)" ]] || { echo "${label} issuer private key is readable by other users" >&2; return 1; }
  "${openssl_binary}" verify -CAfile "${trust_bundle}" "${issuer_cert}" >/dev/null || { echo "${label} issuer is not trusted by the configured CA bundle" >&2; return 1; }
  cert_public="$(mktemp "${TMPDIR:-/tmp}/clusterguard-cert-public.XXXXXX")"
  key_public="$(mktemp "${TMPDIR:-/tmp}/clusterguard-key-public.XXXXXX")"
  "${openssl_binary}" x509 -in "${issuer_cert}" -pubkey -noout >"${cert_public}"
  "${openssl_binary}" pkey -in "${issuer_key}" -pubout >"${key_public}" 2>/dev/null
  cmp -s "${cert_public}" "${key_public}" || { rm -f "${cert_public}" "${key_public}"; echo "${label} issuer certificate and private key do not match" >&2; return 1; }
  rm -f "${cert_public}" "${key_public}"
}

if [[ "${issuer_count}" -eq 4 ]]; then
  [[ -n "${api_cert_path}" && -n "${raft_cert_path}" ]] || { echo "control TLS must be enabled before dynamic controller enrollment" >&2; exit 3; }
  validate_issuer "${api_issuer_cert}" "${api_issuer_key}" "${api_ca_path}" "API"
  validate_issuer "${raft_issuer_cert}" "${raft_issuer_key}" "${raft_ca_path}" "Raft"
else
  [[ -z "${api_cert_path}" ]] || certificate_covers_host "${api_cert_path}" "${host}" || {
    echo "API certificate does not cover ${host}; configure the control API and Raft issuer files before adding this controller" >&2
    exit 3
  }
  [[ -z "${raft_cert_path}" ]] || certificate_covers_host "${raft_cert_path}" "${host}" || {
    echo "Raft certificate does not cover ${host}; configure the control API and Raft issuer files before adding this controller" >&2
    exit 3
  }
fi

ssh_prefix() {
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
  ssh_command+=(-p "${ssh_port}")
}

scp_prefix() {
  scp_command=(scp -q -o BatchMode=yes -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${known_hosts}")
  if [[ -n "${identity_file}" ]]; then
    scp_command+=(-i "${identity_file}")
  else
    command -v sshpass >/dev/null 2>&1 || return 1
    export SSHPASS="${CG_SSH_PASSWORD:-}"
    scp_command=(sshpass -e scp -q -o BatchMode=no -o PasswordAuthentication=yes -o KbdInteractiveAuthentication=no -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=${known_hosts}")
  fi
  scp_command+=(-P "${ssh_port}")
}

remote_exec() {
  ssh_prefix
  "${ssh_command[@]}" "${ssh_user}@${host}" "$1"
}

copy_remote() {
  local source="$1" destination="$2" destination_host="${host}"
  scp_prefix
  [[ "${host}" != *:* ]] || destination_host="[${host}]"
  "${scp_command[@]}" "${source}" "${ssh_user}@${destination_host}:${destination}"
}

enrollment_marker="/var/lib/clusterguard/stage/control-enrollment-${node_id}.new"
if [[ "${operation}" == "rollback" ]]; then
  remote_exec "set -e; marker='${enrollment_marker}'; \
    if ! test -f \"\${marker}\"; then exit 0; fi; \
    test \"\$(cat \"\${marker}\")\" = '${node_id}'; \
    if test -f /etc/clusterguard/clusterguard.json; then \
      jq_bin=/usr/local/libexec/jq-linux-amd64; test -x \"\${jq_bin}\" || jq_bin=\$(command -v jq); \
      test \"\$(\"\${jq_bin}\" -r '.consensus.local_id // empty' /etc/clusterguard/clusterguard.json)\" = '${node_id}'; \
    fi; \
    systemctl disable --now clusterguard-ha.service; \
    ! systemctl is-active --quiet clusterguard-ha.service"
  printf '{"rolled_back":true,"resource_id":"%s","new_install":true}\n' "${node_id}"
  exit 0
fi

new_install="$(remote_exec "set -e; marker='${enrollment_marker}'; \
  if test -f \"\${marker}\"; then test \"\$(cat \"\${marker}\")\" = '${node_id}'; printf true; exit 0; fi; \
  if test -f /etc/clusterguard/clusterguard.json; then \
    jq_bin=/usr/local/libexec/jq-linux-amd64; test -x \"\${jq_bin}\" || jq_bin=\$(command -v jq); \
    existing=\$(\"\${jq_bin}\" -r '.consensus.local_id // empty' /etc/clusterguard/clusterguard.json); \
    test -z \"\${existing}\" -o \"\${existing}\" = '${node_id}' || { echo 'target belongs to another controller identity' >&2; exit 4; }; \
    printf false; \
  else printf true; fi")"
[[ "${new_install}" == "true" || "${new_install}" == "false" ]] || { echo "invalid target control-node installation state" >&2; exit 4; }
if [[ "${new_install}" == "true" ]]; then
  remote_exec "install -d -m 0700 /var/lib/clusterguard/stage; printf '%s\\n' '${node_id}' > '${enrollment_marker}'; chmod 0600 '${enrollment_marker}'"
fi

stage_control_path() {
  local configured_path="$1"
  [[ "${configured_path}" == /etc/clusterguard/* ]] || return 1
  printf '%s/etc/clusterguard/%s' "${root}" "${configured_path#/etc/clusterguard/}"
}

issue_control_certificate() {
  local destination_cert="$1" destination_key="$2" issuer_cert="$3" issuer_key="$4" usages="$5" label="$6"
  local request_file extension_file certificate_serial san="" work
  work="$(mktemp -d "${stage}/${label}.XXXXXX")"
  request_file="${work}/identity.csr"
  extension_file="${work}/identity.ext"
  if [[ -n "${target_hostname}" ]]; then san="DNS:${target_hostname}"; fi
  if [[ -n "${target_ip}" ]]; then
    [[ -z "${san}" ]] || san+=","
    san+="IP:${target_ip}"
  fi
  [[ -n "${san}" ]] || { echo "control certificate requires a target hostname or IP" >&2; return 1; }
  install -d -m 0750 "$(dirname "${destination_cert}")" "$(dirname "${destination_key}")"
  "${openssl_binary}" req -new -newkey rsa:3072 -nodes -sha256 -subj "/CN=${node_name}" -keyout "${destination_key}" -out "${request_file}" >/dev/null 2>&1
  cat >"${extension_file}" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=${usages}
subjectAltName=${san}
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid,issuer
EOF
  certificate_serial="0x$("${openssl_binary}" rand -hex 16)"
  "${openssl_binary}" x509 -req -sha256 -days "${certificate_validity_days}" -in "${request_file}" -CA "${issuer_cert}" -CAkey "${issuer_key}" -set_serial "${certificate_serial}" -extfile "${extension_file}" -out "${destination_cert}" >/dev/null 2>&1
  chmod 0644 "${destination_cert}"
  chmod 0640 "${destination_key}"
  certificate_covers_host "${destination_cert}" "${host}" || { echo "issued ${label} certificate does not cover ${host}" >&2; return 1; }
}

stage="$(mktemp -d "${TMPDIR:-/tmp}/clusterguard-control-join.XXXXXX")"
trap 'rm -rf "${stage}"' EXIT
root="${stage}/root"
install -d -m 0755 "${root}/usr/local/bin" "${root}/usr/local/libexec" "${root}/etc/clusterguard" "${root}/etc/systemd/system"
for binary in clusterguard cgctl clusterguard-agent; do
  [[ -x "/usr/local/bin/${binary}" ]] || { echo "installed ${binary} binary is required" >&2; exit 3; }
  install -m 0755 "/usr/local/bin/${binary}" "${root}/usr/local/bin/${binary}"
done
while IFS= read -r helper; do
  install -m 0755 "${helper}" "${root}/usr/local/libexec/$(basename "${helper}")"
done < <(find /usr/local/libexec -maxdepth 1 -type f -name 'clusterguard-*.sh' -perm -u+x -print | LC_ALL=C sort)
if [[ -x "${jq_binary}" ]]; then
  install -m 0755 "${jq_binary}" "${root}/usr/local/libexec/jq-linux-amd64"
fi
service_source=""
for candidate in /etc/systemd/system/clusterguard-ha.service /usr/lib/systemd/system/clusterguard-ha.service /lib/systemd/system/clusterguard-ha.service; do
  if [[ -f "${candidate}" ]]; then service_source="${candidate}"; break; fi
done
[[ -n "${service_source}" ]] || { echo "clusterguard-ha systemd unit is required" >&2; exit 3; }
install -m 0644 "${service_source}" "${root}/etc/systemd/system/clusterguard-ha.service"
install -m 0640 "${source_environment}" "${root}/etc/clusterguard/clusterguard.env"

peer_additions='[]'
target_count="$(${jq_binary} '.plan.targets | length' "${request_file}")"
for ((index=0; index<target_count; index++)); do
  candidate="$(${jq_binary} -c ".plan.targets[${index}]" "${request_file}")"
  candidate_kind="$(${jq_binary} -r '.kind' <<<"${candidate}")"
  [[ "${candidate_kind}" == "controller" || "${candidate_kind}" == "mixed" ]] || continue
  candidate_id="$(${jq_binary} -r '.node_id' <<<"${candidate}")"
  candidate_host="$(${jq_binary} -r '.ip_address // .hostname' <<<"${candidate}")"
  peer_additions="$(${jq_binary} -nc --argjson current "${peer_additions}" --arg id "${candidate_id}" \
    --arg address "${candidate_host}:${raft_port}" --arg api "${api_scheme}://${candidate_host}:${api_port}" \
    '$current + [{resource_id:$id,address:$address,api_address:$api}]')"
done

${jq_binary} --arg local_id "${node_id}" --arg advertise "${host}:${raft_port}" --argjson additions "${peer_additions}" '
  .consensus.local_id = $local_id |
  .consensus.bind_address = ("0.0.0.0:" + ($advertise | split(":") | last)) |
  .consensus.advertise_address = $advertise |
  .consensus.bootstrap = false |
  .consensus.peers = ((.consensus.peers + $additions) | unique_by(.resource_id) | sort_by(.resource_id)) |
  .metadata_path = "/var/lib/clusterguard/metadata.json"
' "${source_config}" >"${root}/etc/clusterguard/clusterguard.json"
chmod 0640 "${root}/etc/clusterguard/clusterguard.json"
printf '{"resource_id":"%s","node_name":"%s","role":"%s"}\n' "${node_id}" "${node_name}" "${kind}" >"${root}/etc/clusterguard/node.json"
chmod 0640 "${root}/etc/clusterguard/node.json"

while IFS= read -r configured_path; do
  [[ -n "${configured_path}" && "${configured_path}" == /etc/clusterguard/* && -f "${configured_path}" ]] || continue
  relative="${configured_path#/etc/clusterguard/}"
  install -d -m 0750 "${root}/etc/clusterguard/$(dirname "${relative}")"
  mode=0640
  [[ "${relative}" == *.crt || "${relative}" == *known_hosts ]] && mode=0644
  [[ "${relative}" == *.key || "${relative}" == *_ed25519 ]] && mode=0640
  [[ "${relative}" == *.cnf || "${relative}" == *.pass ]] && mode=0600
  install -m "${mode}" "${configured_path}" "${root}/etc/clusterguard/${relative}"
done < <(${jq_binary} -r '[.tls_cert_file,.tls_key_file,.tls_ca_file,.consensus.tls_cert_file,.consensus.tls_key_file,.consensus.tls_ca_file,.agent.identity_file,.agent.known_hosts_file,.node_lifecycle.identity_file,.node_lifecycle.known_hosts_file] | map(select(type == "string" and length > 0)) | unique[]' "${source_config}")

if [[ "${issuer_count}" -eq 4 ]]; then
  for issuer_asset in "${api_issuer_cert}:0644" "${api_issuer_key}:0640" "${raft_issuer_cert}:0644" "${raft_issuer_key}:0640"; do
    issuer_source="${issuer_asset%%:*}"
    issuer_mode="${issuer_asset##*:}"
    issuer_destination="$(stage_control_path "${issuer_source}")"
    install -d -m 0750 "$(dirname "${issuer_destination}")"
    install -m "${issuer_mode}" "${issuer_source}" "${issuer_destination}"
  done
  issue_control_certificate "$(stage_control_path "${api_cert_path}")" "$(stage_control_path "${api_key_path}")" "${api_issuer_cert}" "${api_issuer_key}" "serverAuth" "api"
  issue_control_certificate "$(stage_control_path "${raft_cert_path}")" "$(stage_control_path "${raft_key_path}")" "${raft_issuer_cert}" "${raft_issuer_key}" "serverAuth,clientAuth" "raft"
  "${openssl_binary}" verify -CAfile "$(stage_control_path "${api_ca_path}")" "$(stage_control_path "${api_cert_path}")" >/dev/null
  "${openssl_binary}" verify -CAfile "$(stage_control_path "${raft_ca_path}")" "$(stage_control_path "${raft_cert_path}")" >/dev/null
fi

archive="${stage}/controller.tar.gz"
tar -C "${root}" -czf "${archive}" .
remote_exec "install -d -m 0700 /var/lib/clusterguard/stage"
copy_remote "${archive}" "/var/lib/clusterguard/stage/controller.tar.gz"
remote_exec "set -e; \
  if test -f /etc/clusterguard/clusterguard.json; then existing=\$(/usr/local/libexec/jq-linux-amd64 -r '.consensus.local_id // empty' /etc/clusterguard/clusterguard.json 2>/dev/null || true); test -z \"\${existing}\" -o \"\${existing}\" = '${node_id}' || { echo 'target belongs to another controller identity' >&2; exit 4; }; fi; \
  getent group clusterguard >/dev/null || groupadd --system clusterguard; \
  id clusterguard >/dev/null 2>&1 || useradd --system --gid clusterguard --home-dir /var/lib/clusterguard --shell /sbin/nologin clusterguard; \
  tar --no-same-owner -xzf /var/lib/clusterguard/stage/controller.tar.gz -C /; \
  install -d -o clusterguard -g clusterguard -m 0750 /var/lib/clusterguard; \
  install -d -o clusterguard -g clusterguard -m 0751 /var/log/clusterguard; \
  chown -R root:clusterguard /etc/clusterguard; chmod 0751 /etc/clusterguard; chmod 0640 /etc/clusterguard/clusterguard.env /etc/clusterguard/clusterguard.json; \
  chown -R clusterguard:clusterguard /var/lib/clusterguard; \
  chown clusterguard:clusterguard /var/log/clusterguard; chmod 0751 /var/log/clusterguard; \
  find /var/log/clusterguard -maxdepth 1 -type f -exec chown clusterguard:clusterguard {} +; \
  systemctl daemon-reload; systemctl enable --now clusterguard-ha.service; \
  deadline=30; while ! systemctl is-active --quiet clusterguard-ha.service; do deadline=\$((deadline-1)); test \${deadline} -gt 0 || { journalctl -u clusterguard-ha.service -n 50 --no-pager >&2; exit 5; }; sleep 1; done; \
  test \"\$(/usr/local/libexec/jq-linux-amd64 -r '.consensus.local_id' /etc/clusterguard/clusterguard.json)\" = '${node_id}'"

if [[ "${new_install}" == "true" ]]; then
  printf '{"ready":true,"resource_id":"%s","node_name":"%s","address":"%s:%s","new_install":true}\n' "${node_id}" "${node_name}" "${host}" "${raft_port}"
else
  printf '{"ready":true,"resource_id":"%s","node_name":"%s","address":"%s:%s","new_install":false}\n' "${node_id}" "${node_name}" "${host}" "${raft_port}"
fi
