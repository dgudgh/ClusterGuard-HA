#!/usr/bin/env bash
set -euo pipefail

jq_binary="${CG_JQ_BINARY:-$(command -v jq || true)}"
[[ -x "${jq_binary}" ]] || { echo "jq is required to prepare an adapter runtime" >&2; exit 2; }

payload_file="$(mktemp "${TMPDIR:-/tmp}/clusterguard-adapter-runtime.XXXXXX")"
work_directory=""
trap 'rm -f "${payload_file}"; [[ -z "${work_directory}" ]] || rm -rf "${work_directory}"' EXIT
chmod 0600 "${payload_file}"
cat >"${payload_file}"
"${jq_binary}" -e '.request.engine and .target' "${payload_file}" >/dev/null

engine="$("${jq_binary}" -r '.request.engine' "${payload_file}")"
package_path="$("${jq_binary}" -r '.target.package_path // ""' "${payload_file}")"
runtime_root="${CG_ADAPTER_RUNTIME_ROOT:-/opt/clusterguard/adapter-runtime}"
link_root="${CG_ADAPTER_RUNTIME_LINK_ROOT:-/usr/local}"
managed_root="${CG_MANAGED_DATABASE_ROOT:-/opt/clusterguard}"
marker="${CG_ADAPTER_RUNTIME_MARKER:-/var/lib/clusterguard/adapter-runtime-ready.json}"

for path in "${runtime_root}" "${link_root}" "${managed_root}" "${marker}"; do
  [[ "${path}" == /* ]] || { echo "adapter runtime paths must be absolute" >&2; exit 2; }
done
install -d -m 0755 "${runtime_root}" "${link_root}" "$(dirname "${marker}")"

publish_marker() {
  local binary="$1" version="$2" temporary
  temporary="${marker}.tmp.$$"
  "${jq_binary}" -nc \
    --arg engine "${engine}" --arg binary "${binary}" --arg version "${version}" \
    '{schema_version:1,ready:true,engine:$engine,binary:$binary,version:$version}' >"${temporary}"
  chmod 0644 "${temporary}"
  mv -f "${temporary}" "${marker}"
}

link_directory_runtime() {
  local name="$1" source_root="$2" required_binary="$3" link_path
  link_path="${link_root}/${name}"
  [[ -x "${source_root}/bin/${required_binary}" ]] || { echo "adapter runtime does not contain ${required_binary}" >&2; return 1; }
  if [[ -e "${link_path}" && ! -L "${link_path}" ]]; then
    [[ -x "${link_path}/bin/${required_binary}" ]] || { echo "refusing to replace unmanaged adapter runtime ${link_path}" >&2; return 1; }
    return 0
  fi
  ln -sfn "${source_root}" "${link_path}"
}

materialize_single_binary() {
  local name="$1" source_binary="$2" destination_root
  destination_root="${runtime_root}/${name}/system"
  install -d -m 0755 "${destination_root}/bin"
  ln -sfn "${source_binary}" "${destination_root}/bin/${name}"
  printf '%s\n' "${destination_root}"
}

extract_package_binary() {
  local name="$1" version="$2"
  [[ -f "${package_path}" ]] || return 1
  work_directory="$(mktemp -d "${runtime_root}/.${name}.XXXXXX")"
  tar -xf "${package_path}" -C "${work_directory}"
  local binary source_root destination
  binary="$(find "${work_directory}" -type f -path "*/bin/${name}" -perm -u+x -print | LC_ALL=C sort | head -n 1)"
  [[ -n "${binary}" ]] || { echo "verified package does not contain ${name}" >&2; return 1; }
  source_root="$(dirname "$(dirname "${binary}")")"
  [[ "${version}" =~ ^[0-9]+([.][0-9]+)*$ ]] || version="package"
  destination="${runtime_root}/${name}/${version}"
  if [[ ! -x "${destination}/bin/${name}" ]]; then
    rm -rf "${destination}.new"
    install -d -m 0755 "$(dirname "${destination}")"
    mv "${source_root}" "${destination}.new"
    mv "${destination}.new" "${destination}"
  fi
  printf '%s\n' "${destination}"
}

prepare_mysql() {
  local version candidate source_root linked_binary version_text
  version="$("${jq_binary}" -r '.target.mysql_version // ""' "${payload_file}")"
  candidate="${CG_MYSQL_CLIENT:-}"
  if [[ -z "${candidate}" && -x "${link_root}/mysql/bin/mysql" ]]; then
    candidate="${link_root}/mysql/bin/mysql"
  fi
  if [[ -z "${candidate}" ]]; then
    candidate="$(command -v mysql || true)"
  fi
  if [[ -z "${candidate}" && -d "${managed_root}/mysql" ]]; then
    candidate="$(find "${managed_root}/mysql" -type f -path '*/software/bin/mysql' -perm -u+x -print | LC_ALL=C sort | tail -n 1)"
  fi
  if [[ -n "${candidate}" && -x "${candidate}" ]]; then
    if [[ "${candidate}" == */software/bin/mysql ]]; then
      source_root="$(dirname "$(dirname "${candidate}")")"
    elif [[ "${candidate}" == "${link_root}/mysql/bin/mysql" ]]; then
      source_root="$(cd "$(dirname "${candidate}")/.." && pwd -P)"
    else
      source_root="$(materialize_single_binary mysql "${candidate}")"
    fi
  else
    source_root="$(extract_package_binary mysql "${version}")" || { echo "MySQL client runtime is unavailable" >&2; exit 4; }
  fi
  link_directory_runtime mysql "${source_root}" mysql
  linked_binary="${link_root}/mysql/bin/mysql"
  version_text="$("${linked_binary}" --version 2>&1)" || { echo "MySQL client runtime verification failed" >&2; exit 4; }
  [[ -n "${version_text}" ]] || { echo "MySQL client runtime returned no version" >&2; exit 4; }
  publish_marker "${linked_binary}" "${version_text}"
}

prepare_postgresql() {
  local candidate version_text destination
  candidate="${CG_POSTGRESQL_CLIENT:-$(command -v psql || true)}"
  if [[ -z "${candidate}" ]]; then
    candidate="$(find /usr/pgsql-* /usr/lib/postgresql "${managed_root}/postgresql" -type f -path '*/bin/psql' -perm -u+x -print 2>/dev/null | LC_ALL=C sort | tail -n 1)"
  fi
  [[ -n "${candidate}" && -x "${candidate}" ]] || { echo "PostgreSQL client runtime is unavailable" >&2; exit 4; }
  install -d -m 0755 "${link_root}/bin"
  destination="${link_root}/bin/psql"
  if [[ -e "${destination}" && ! -L "${destination}" && "${destination}" != "${candidate}" ]]; then
    [[ -x "${destination}" ]] || { echo "refusing to replace unmanaged PostgreSQL client" >&2; exit 4; }
  else
    ln -sfn "${candidate}" "${destination}"
  fi
  version_text="$("${destination}" --version 2>&1)" || { echo "PostgreSQL client runtime verification failed" >&2; exit 4; }
  publish_marker "${destination}" "${version_text}"
}

case "${engine}" in
  mysql) prepare_mysql ;;
  postgresql) prepare_postgresql ;;
  *) echo "adapter runtime installation is unsupported for ${engine}" >&2; exit 3 ;;
esac

cat "${marker}"
