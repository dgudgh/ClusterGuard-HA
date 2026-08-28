#!/usr/bin/env bash
set -euo pipefail

engine="${1:-}"
version="${2:-}"
selected_name="${3:-}"
package_repository="${CG_PACKAGE_REPOSITORY:-/opt/clusterguard/packages}"
manifest="${CG_PACKAGE_MANIFEST:-${package_repository}/manifest.json}"
jq_binary="${CG_JQ_BINARY:-}"
if [[ -z "${jq_binary}" ]]; then
  jq_binary="$(command -v jq || true)"
fi

[[ "${engine}" =~ ^[a-z0-9_]+$ ]] || { echo "valid database engine is required" >&2; exit 2; }
[[ -n "${version}" ]] || { echo "database version is required for package resolution" >&2; exit 2; }
[[ -x "${jq_binary}" ]] || { echo "a verified jq binary is required" >&2; exit 2; }
[[ -f "${manifest}" && ! -L "${manifest}" ]] || { echo "package manifest is unavailable" >&2; exit 3; }
if [[ -n "${selected_name}" && "${selected_name}" != "$(basename "${selected_name}")" ]]; then
  echo "package must be selected by basename" >&2
  exit 3
fi

records="$(${jq_binary} -c \
  --arg engine "${engine}" \
  --arg version "${version}" \
  --arg selected "${selected_name}" '
    if .schema_version != 1 then error("unsupported package manifest schema") else . end
    | [.packages[]?
       | select(.engine == $engine)
       | (.version_prefix // "") as $prefix
       | select((.version // "") == $version or ($prefix != "" and ($version | startswith($prefix))))
       | select($selected == "" or .filename == $selected)]
  ' "${manifest}")"
count="$(${jq_binary} -r 'length' <<<"${records}")"
[[ "${count}" == "1" ]] || {
  echo "package manifest must contain exactly one compatible package for ${engine} ${version}; found ${count}" >&2
  exit 3
}

record="$(${jq_binary} -c '.[0]' <<<"${records}")"
filename="$(${jq_binary} -r '.filename // ""' <<<"${record}")"
expected_sha="$(${jq_binary} -r '(.sha256 // "") | ascii_downcase' <<<"${record}")"
[[ -n "${filename}" && "${filename}" == "$(basename "${filename}")" ]] || { echo "invalid package filename in manifest" >&2; exit 3; }
[[ "${expected_sha}" =~ ^[0-9a-f]{64}$ ]] || { echo "invalid package SHA256 in manifest" >&2; exit 3; }

package_path="${package_repository}/${filename}"
[[ -f "${package_path}" && ! -L "${package_path}" ]] || { echo "verified database package is unavailable" >&2; exit 3; }
if command -v sha256sum >/dev/null 2>&1; then
  actual_sha="$(sha256sum "${package_path}" | awk '{print tolower($1)}')"
elif command -v shasum >/dev/null 2>&1; then
  actual_sha="$(shasum -a 256 "${package_path}" | awk '{print tolower($1)}')"
else
  echo "SHA256 verification tool is required" >&2
  exit 3
fi
[[ "${actual_sha}" == "${expected_sha}" ]] || { echo "package SHA256 verification failed for ${filename}" >&2; exit 3; }

printf '%s\n' "${package_path}"
