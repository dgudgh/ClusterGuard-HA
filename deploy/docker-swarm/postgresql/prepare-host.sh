#!/usr/bin/env bash
set -euo pipefail

slot="${1:-}"
node_ip="${2:-}"
data_root="${3:-/data/clusterguard-swarm/postgresql/55432}"
[[ "${slot}" =~ ^0[123]$ ]] || { echo "usage: $0 01|02|03 NODE_IP [DATA_ROOT]" >&2; exit 2; }
[[ "${node_ip}" =~ ^[0-9.]+$ ]] || { echo "invalid node IP" >&2; exit 2; }
[[ "${data_root}" = /* ]] || { echo "data root must be absolute" >&2; exit 2; }
[[ "${EUID}" -eq 0 ]] || { echo "run as root" >&2; exit 2; }

install -d -m 0700 "${data_root}"
if [[ ! -s "${data_root}/PG_VERSION" ]] && find "${data_root}" -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
  echo "data root is non-empty but does not contain a PostgreSQL cluster: ${data_root}" >&2
  exit 3
fi

if command -v chcon >/dev/null 2>&1; then
  chcon -Rt container_file_t "${data_root}" 2>/dev/null || true
fi

node_id="$(docker info --format '{{.Swarm.NodeID}}' 2>/dev/null || true)"
[[ -n "${node_id}" ]] || { echo "host is not an active Swarm node" >&2; exit 3; }
docker node update --label-add "clusterguard.postgresql.slot=${slot}" "${node_id}" >/dev/null
printf 'slot=%s node_ip=%s node_id=%s data_root=%s\n' "${slot}" "${node_ip}" "${node_id}" "${data_root}"
