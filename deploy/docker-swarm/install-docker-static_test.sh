#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
installer="${script_dir}/install-docker-static.sh"
grep -qE '^  /\^docker\\/\$/' "${installer}" || {
  echo "installer must allow the official archive directory entry" >&2
  exit 1
}
grep -q 'containerd-shim-runc-v2' "${installer}" || {
  echo "installer static binary allowlist is incomplete" >&2
  exit 1
}
if grep -Eq 'printf .*live-restore.*true' "${installer}"; then
  echo "installer must not enable Docker live-restore for Swarm" >&2
  exit 1
fi

echo "Docker static archive allowlist test passed"
