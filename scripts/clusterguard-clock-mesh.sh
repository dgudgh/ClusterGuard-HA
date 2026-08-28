#!/usr/bin/env bash
set -euo pipefail

# Configure an isolated ClusterGuard installation with a fixed internal clock
# authority. The control-plane source is intentionally independent from the
# database primary and VIP, so a database switchover never changes time source.

usage() {
  cat <<'EOF'
Usage:
  clusterguard-clock-mesh.sh --server [--subnet CIDR]
  clusterguard-clock-mesh.sh --client --server-address ADDRESS
EOF
}

mode=""
server_address=""
subnet="192.168.102.0/24"

while (($#)); do
  case "$1" in
    --server) mode="server"; shift ;;
    --client) mode="client"; shift ;;
    --server-address) server_address="${2:-}"; shift 2 ;;
    --subnet) subnet="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) printf 'Unknown argument: %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
done

[[ -n "$mode" ]] || { usage >&2; exit 2; }
if [[ "$mode" == "client" && -z "$server_address" ]]; then
  printf '%s\n' '--client requires --server-address' >&2
  exit 2
fi

backup="/etc/chrony.conf.clusterguard-backup.$(date -u +%Y%m%dT%H%M%SZ)"
[[ -f /etc/chrony.conf ]] && cp -a /etc/chrony.conf "$backup"

wait_for_fixed_source() {
  local deadline=$((SECONDS + 45))
  while (( SECONDS < deadline )); do
    if chronyc -n sources 2>/dev/null | awk -v source="${server_address}" '$1 == "^*" && $2 == source { found=1 } END { exit !found }'; then
      chronyc makestep >/dev/null 2>&1 || true
      sleep 1
      chronyc tracking
      chronyc sources -v
      return 0
    fi
    sleep 1
  done
  printf 'Chrony did not select fixed internal source %s within 45 seconds\n' "${server_address}" >&2
  chronyc tracking >&2 || true
  chronyc sources -v >&2 || true
  return 1
}

persist_utc_clock() {
  timedatectl set-local-rtc 0 >/dev/null
  if ! hwclock --systohc --utc; then
    printf 'Unable to persist the synchronized UTC clock to the hardware clock\n' >&2
    return 1
  fi
}

if [[ "$mode" == "server" ]]; then
  cat > /etc/chrony.conf <<EOF
# Managed by ClusterGuard HA. This isolated deployment has no external NTP.
driftfile /var/lib/chrony/drift
makestep 1.0 3
rtcsync
local stratum 10
allow ${subnet}
keyfile /etc/chrony.keys
leapsectz right/UTC
logdir /var/log/chrony
EOF
  if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    firewall-cmd --permanent --add-service=ntp >/dev/null
    firewall-cmd --reload >/dev/null
  fi
else
  cat > /etc/chrony.conf <<EOF
# Managed by ClusterGuard HA. The server is the fixed local time authority.
server ${server_address} iburst prefer
driftfile /var/lib/chrony/drift
makestep 1.0 3
rtcsync
keyfile /etc/chrony.keys
leapsectz right/UTC
logdir /var/log/chrony
EOF
fi

systemctl enable chronyd >/dev/null
systemctl restart chronyd
if [[ "$mode" == "client" ]]; then
  wait_for_fixed_source
else
  sleep 1
  chronyc tracking
  chronyc sources -v
fi
persist_utc_clock
