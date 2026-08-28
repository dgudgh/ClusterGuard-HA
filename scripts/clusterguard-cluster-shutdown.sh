#!/bin/bash

# Compatibility entry point for planned shutdown. The implementation lives in
# the control-plane power lifecycle; this helper must never become a second,
# privileged execution path around Safety Guard, lock, approval, audit, Agent
# allowlists, snapshot persistence, and verification.

set -Eeuo pipefail

if [[ -f /etc/clusterguard/clusterguard.env ]]; then
  # shellcheck disable=SC1091
  source /etc/clusterguard/clusterguard.env
fi

cgctl_command="${CGCTL_COMMAND:-/usr/local/bin/cgctl}"
cg_api_url="${CLUSTERGUARD_API:-https://127.0.0.1:3000}"
cluster_name="${CLUSTER_NAME:-}"
shutdown_mode="${SHUTDOWN_MODE:-service}"
approval_token="${CG_APPROVAL_TOKEN:-}"
dry_run="${DRY_RUN:-false}"

function fail {
  echo "clusterguard-cluster-shutdown.sh: $1" >&2
  exit "${2:-1}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --cluster) cluster_name="${2:-}"; shift 2 ;;
    --mode) shutdown_mode="${2:-}"; shift 2 ;;
    --approval-token) approval_token="${2:-}"; shift 2 ;;
    --dry-run) dry_run=true; shift ;;
    --no-dry-run) dry_run=false; shift ;;
    *) fail "unknown option: $1" 2 ;;
  esac
done

[[ -x "$cgctl_command" ]] || fail "cgctl is not executable: ${cgctl_command}"
[[ -n "$cluster_name" ]] || fail "--cluster <display name or UUID> is required" 2
case "$shutdown_mode" in
  service|poweroff) ;;
  *) fail "shutdown mode must be service or poweroff" 2 ;;
esac
[[ -n "${CG_CONTROL_TOKEN:-}" ]] || fail "CG_CONTROL_TOKEN is required for control-plane authorization"

arguments=(--server "$cg_api_url" --token-env CG_CONTROL_TOKEN)
if [[ -n "${CG_TLS_CA_FILE:-}" ]]; then
  arguments+=(--ca-file "$CG_TLS_CA_FILE")
fi
arguments+=(cluster shutdown --cluster "$cluster_name" --mode "$shutdown_mode")
if [[ "$dry_run" == "true" ]]; then
  arguments+=(--dry-run)
else
  [[ -n "$approval_token" ]] || fail "--approval-token is required for real shutdown; the Web console issues its own one-time approval"
  arguments+=(--approval-token "$approval_token")
fi

exec "$cgctl_command" "${arguments[@]}"
