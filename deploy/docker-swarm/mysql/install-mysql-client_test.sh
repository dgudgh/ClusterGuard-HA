#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
installer="${script_dir}/install-mysql-client.sh"

bash -n "${installer}"
grep -Fq 'MySQL package must contain one safe top-level directory' "${installer}" || {
  echo "installer must reject unsafe archives" >&2
  exit 1
}
grep -Fq 'refusing to replace unmanaged MySQL client link' "${installer}" || {
  echo "installer must preserve unmanaged clients" >&2
  exit 1
}
grep -Fq 'ldd "${staged_directory}/bin/mysql"' "${installer}" || {
  echo "installer must validate client runtime linkage" >&2
  exit 1
}
grep -Fq '# managed-by: clusterguard-ha mysql-client' "${installer}" || {
  echo "installer must use a managed launcher for relative MySQL libraries" >&2
  exit 1
}

echo "MySQL client installer safety test passed"
