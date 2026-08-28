#!/usr/bin/env bash
set -euo pipefail

archive="${1:-}"
target="${2:-/opt/clusterguard/mysql-client}"
client_link="${3:-/usr/local/bin/mysql}"

[[ "${EUID}" -eq 0 ]] || { echo "run as root" >&2; exit 2; }
[[ -f "${archive}" && ! -L "${archive}" ]] || {
  echo "usage: $0 /absolute/path/mysql-VERSION-linux-x86_64.tar.xz [target-directory] [client-link]" >&2
  exit 2
}
[[ "${target}" == /* && "${target}" != "/" ]] || { echo "target directory must be an absolute non-root path" >&2; exit 2; }
[[ "${client_link}" == /* && "${client_link}" != "/" ]] || { echo "client link must be an absolute non-root path" >&2; exit 2; }
command -v ldd >/dev/null 2>&1 || { echo "ldd is required to validate the MySQL client" >&2; exit 3; }

target_parent="$(dirname "${target}")"
install -d -m 0755 "${target_parent}" "$(dirname "${client_link}")"
work_directory="$(mktemp -d "${target_parent}/.mysql-client.XXXXXX")"
archive_listing="${work_directory}/archive.list"
staged_directory="${work_directory}/runtime"
previous_directory="${target}.previous"
committed=false

cleanup() {
  status=$?
  if [[ "${status}" -ne 0 && "${committed}" == "true" ]]; then
    rm -rf "${target}"
    if [[ -d "${previous_directory}" ]]; then
      mv "${previous_directory}" "${target}" || true
    fi
  fi
  rm -rf "${work_directory}"
  exit "${status}"
}
trap cleanup EXIT

tar -tf "${archive}" >"${archive_listing}" 2>/dev/null || {
  echo "MySQL package is not a readable tar archive" >&2
  exit 3
}
archive_root="$(awk '
  /(^\/|(^|\/)\.\.($|\/))/ {bad=1}
  {
    original=$0
    path=$0
    sub(/^\.\//, "", path)
    sub(/\/$/, "", path)
    if (path == "") next
    count=split(path, parts, "/")
    if (root == "") root=parts[1]
    if (parts[1] != root) bad=1
    if (count == 1 && original !~ /\/$/) bad=1
  }
  END {
    if (root == "" || bad) exit 1
    print root
  }
' "${archive_listing}")" || {
  echo "MySQL package must contain one safe top-level directory" >&2
  exit 3
}

grep -Fqx "${archive_root}/bin/mysql" "${archive_listing}" || {
  echo "MySQL package does not contain bin/mysql" >&2
  exit 3
}
grep -Fqx "${archive_root}/lib/" "${archive_listing}" || {
  echo "MySQL package does not contain the client runtime libraries" >&2
  exit 3
}

mkdir -p "${staged_directory}"
members=("${archive_root}/bin/mysql" "${archive_root}/lib/")
for optional_member in share/charsets/ share/english/; do
  if grep -Fqx "${archive_root}/${optional_member}" "${archive_listing}"; then
    members+=("${archive_root}/${optional_member}")
  fi
done
tar --no-same-owner --no-same-permissions -xf "${archive}" \
  -C "${staged_directory}" --strip-components=1 "${members[@]}"
chmod 0755 "${staged_directory}/bin/mysql"

missing="$(ldd "${staged_directory}/bin/mysql" 2>&1 | awk '/not found/ {print $1}' | sort -u | paste -sd, -)"
if [[ -n "${missing}" ]]; then
  echo "MySQL client runtime dependencies are missing: ${missing}" >&2
  exit 3
fi
client_version="$(${staged_directory}/bin/mysql --version 2>&1)" || {
  echo "extracted MySQL client cannot start" >&2
  exit 3
}
printf 'managed_by=clusterguard-ha\nsource=%s\nversion=%s\n' \
  "$(basename "${archive}")" "${client_version}" >"${staged_directory}/.clusterguard-managed"
chmod 0644 "${staged_directory}/.clusterguard-managed"

if [[ -e "${client_link}" || -L "${client_link}" ]]; then
  current_link="$(readlink "${client_link}" 2>/dev/null || true)"
  if [[ -L "${client_link}" ]]; then
    case "${current_link}" in
      /opt/clusterguard/mysql-client*/bin/mysql|"${target}/bin/mysql") ;;
      *)
        echo "refusing to replace unmanaged MySQL client link: ${client_link}" >&2
        exit 3
        ;;
    esac
  elif ! grep -Fqx '# managed-by: clusterguard-ha mysql-client' "${client_link}" 2>/dev/null; then
    echo "refusing to replace unmanaged MySQL client link: ${client_link}" >&2
    exit 3
  fi
fi

rm -rf "${previous_directory}"
if [[ -d "${target}" ]]; then
  [[ -f "${target}/.clusterguard-managed" ]] || {
    echo "refusing to replace unmanaged target directory: ${target}" >&2
    exit 3
  }
  mv "${target}" "${previous_directory}"
fi
mv "${staged_directory}" "${target}"
committed=true
missing="$(ldd "${target}/bin/mysql" 2>&1 | awk '/not found/ {print $1}' | sort -u | paste -sd, -)"
if [[ -n "${missing}" ]]; then
  echo "installed MySQL client runtime dependencies are missing: ${missing}" >&2
  exit 3
fi
client_launcher="${client_link}.clusterguard-new"
cat >"${client_launcher}" <<EOF
#!/usr/bin/env bash
# managed-by: clusterguard-ha mysql-client
exec "${target}/bin/mysql" "\$@"
EOF
chmod 0755 "${client_launcher}"
mv -f "${client_launcher}" "${client_link}"
"${client_link}" --version >/dev/null
rm -rf "${previous_directory}"
committed=false

printf 'ClusterGuard MySQL client installed: %s\n' "${client_version}"
