#!/usr/bin/env bash
set -euo pipefail

# Configure an isolated ClusterGuard installation with a fixed internal clock
# authority. The control-plane source is intentionally independent from the
# database primary and VIP, so a database switchover never changes time source.
#
# An isolated deployment has no external reference, which makes this tool the
# only thing that decides what time the cluster believes. That shapes the two
# decisions it is allowed to take:
#
#   * Becoming the authority is deliberate, never incidental. The server adopts
#     whatever its own clock already says and makes that true for every other
#     node, then writes it to the hardware clock so it survives reboots. So the
#     clock has to be either supplied (--set-utc) or explicitly vouched for
#     (--accept-current-time). Without one of the two it refuses to proceed: a
#     node whose RTC held local time while the kernel read it as UTC is off by
#     the zone offset, and freezing that into the cluster and the RTC is how an
#     eight-hour error becomes permanent.
#
#   * The display timezone is part of the cluster's time configuration, so it is
#     pinned identically on both roles. Left to the OS installer's default, one
#     cluster can show two different wall clocks for the same instant, and every
#     log line and console panel has to be read with that in mind.

usage() {
  cat <<'EOF'
Usage:
  clusterguard-clock-mesh.sh --server (--set-utc TS | --accept-current-time)
                             [--subnet CIDR] [--timezone ZONE] [--dry-run]
  clusterguard-clock-mesh.sh --client --server-address ADDRESS
                             [--timezone ZONE] [--dry-run]

Options:
  --set-utc TS           Adopt this UTC instant before becoming the authority.
                         RFC3339, e.g. 2026-09-29T02:20:00Z.
  --accept-current-time  Vouch that the node's current UTC is correct. Required
                         when --set-utc is absent; confirm it with `date -u`
                         first, because nothing here can verify it for you.
  --timezone ZONE        Display timezone applied to this node, e.g.
                         Asia/Shanghai. Apply the same zone on every node.
  --dry-run              Print the decisions and the configuration that would be
                         written, then exit without changing anything.
EOF
}

mode=""
server_address=""
subnet="192.168.102.0/24"
timezone=""
set_utc=""
accept_current_time=false
dry_run=false

while (($#)); do
  case "$1" in
    --server) mode="server"; shift ;;
    --client) mode="client"; shift ;;
    --server-address) server_address="${2:-}"; shift 2 ;;
    --subnet) subnet="${2:-}"; shift 2 ;;
    --timezone) timezone="${2:-}"; shift 2 ;;
    --set-utc) set_utc="${2:-}"; shift 2 ;;
    --accept-current-time) accept_current_time=true; shift ;;
    --dry-run) dry_run=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) printf 'Unknown argument: %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
done

# Parsing an instant must work on the node as well as wherever this is exercised
# from, so try the GNU spelling and fall back to the BSD one.
epoch_of() {
  date -u -d "$1" +%s 2>/dev/null || date -u -j -f "%Y-%m-%dT%H:%M:%SZ" "$1" +%s 2>/dev/null || true
}

[[ -n "$mode" ]] || { usage >&2; exit 2; }
if [[ "$mode" == "client" && -z "$server_address" ]]; then
  printf '%s\n' '--client requires --server-address' >&2
  exit 2
fi
if [[ -n "$timezone" && ! -f "/usr/share/zoneinfo/${timezone}" ]]; then
  printf 'Unknown timezone: %s\n' "$timezone" >&2
  exit 2
fi
requested_epoch=""
if [[ -n "$set_utc" ]]; then
  requested_epoch="$(epoch_of "$set_utc")"
  if [[ ! "$requested_epoch" =~ ^[0-9]+$ ]]; then
    printf 'Invalid --set-utc (want RFC3339 in UTC, e.g. 2026-09-29T02:20:00Z): %s\n' "$set_utc" >&2
    exit 2
  fi
fi
if [[ "$mode" == "server" && -z "$set_utc" && "$accept_current_time" != true ]]; then
  cat >&2 <<'EOF'
Refusing to become the clock authority: neither --set-utc nor --accept-current-time was given.

This deployment is isolated, so whatever this node believes becomes the time for
every other node, and `hwclock --systohc` then writes it into the hardware clock
where it survives every reboot. If the RTC actually holds local time while the
kernel reads it as UTC — the usual shape of a virtualised install whose zone is
not UTC — that offset is frozen into the cluster permanently.

Choose one:
  --set-utc 2026-09-29T02:20:00Z   adopt this UTC instant, then serve it
  --accept-current-time            vouch that `date -u` on this node is correct
EOF
  exit 2
fi

effective_timezone="$(timedatectl show -p Timezone --value 2>/dev/null || true)"
[[ -n "$effective_timezone" ]] || effective_timezone="$(readlink -f /etc/localtime 2>/dev/null | sed 's#.*/zoneinfo/##' || true)"

if [[ "$dry_run" == true ]]; then
  printf 'dry-run: mode=%s\n' "$mode"
  [[ -n "$server_address" ]] && printf 'dry-run: server-address=%s\n' "$server_address"
  [[ "$mode" == "server" ]] && printf 'dry-run: subnet=%s\n' "$subnet"
  if [[ -n "$requested_epoch" ]]; then
    printf 'dry-run: would adopt %s (epoch %s) before serving\n' "$set_utc" "$requested_epoch"
  elif [[ "$accept_current_time" == true ]]; then
    printf 'dry-run: current clock accepted as correct (%s)\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  fi
  if [[ -n "$timezone" ]]; then
    printf 'dry-run: would set display timezone to %s (currently %s)\n' "$timezone" "$effective_timezone"
  else
    printf 'dry-run: no --timezone given; display timezone stays %s on this node and is NOT managed here\n' "$effective_timezone"
  fi
  printf 'dry-run: nothing was changed\n'
  exit 0
fi

backup="/etc/chrony.conf.clusterguard-backup.$(date -u +%Y%m%dT%H%M%SZ)"
if [[ -f /etc/chrony.conf ]]; then cp -a /etc/chrony.conf "$backup"; fi

if [[ -n "$timezone" ]]; then
  command -v timedatectl >/dev/null 2>&1 ||
    { printf 'timedatectl is required to pin the display timezone\n' >&2; exit 1; }
  timedatectl set-timezone "$timezone" >/dev/null
  printf 'Display timezone set to %s\n' "$timezone"
else
  printf 'Display timezone left at %s; pass --timezone to pin it across the cluster\n' "$effective_timezone"
fi

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

# Only reachable once the clock has been supplied or vouched for: persisting an
# unverified clock is what turns a one-off install mistake into a permanent one.
persist_utc_clock() {
  timedatectl set-local-rtc 0 >/dev/null
  if ! hwclock --systohc --utc; then
    printf 'Unable to persist the synchronized UTC clock to the hardware clock\n' >&2
    return 1
  fi
}

# Chrony is the only writer of this node's clock, so a manual step has to happen
# with it stopped or it will be argued with.
systemctl stop chronyd >/dev/null 2>&1 || true

if [[ -n "$requested_epoch" ]]; then
  current_epoch="$(date -u +%s)"
  drift=$(( current_epoch - requested_epoch ))
  (( drift < 0 )) && drift=$(( -drift ))
  printf 'Adopting %s (UTC); this clock was off by %s seconds\n' "$set_utc" "$drift"
  if ! date -u -s "@${requested_epoch}" >/dev/null 2>&1; then
    stamp="$(date -u -r "${requested_epoch}" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || true)"
    [[ -n "$stamp" ]] && date -u -s "$stamp" >/dev/null 2>&1 ||
      { printf 'Unable to set the system clock to %s\n' "$set_utc" >&2; exit 1; }
  fi
  printf 'System clock now reads %s (UTC)\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
fi

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
