#!/usr/bin/env node
/**
 * verify-clock-mesh.cjs
 *
 * Regression gate for the cluster's time configuration, anchored on the live
 * site incident of 2026-09-29 (192.168.102.152-154).
 *
 * All three nodes agreed with each other to within milliseconds and were still
 * eight hours ahead of real time. Each node's own clock read 10:14 UTC while the
 * real instant was 02:14. The mechanism was mundane and self-reinforcing: the
 * virtual machine's RTC held local time (+08:00) while the kernel read it as
 * UTC, so system time started life eight hours fast; clusterguard-clock-mesh.sh
 * --server promoted that unverified clock to the cluster's authority with
 * nothing upstream to argue with (local stratum 10); and hwclock --systohc --utc
 * wrote it back into the RTC under rtcsync, where it survived every reboot. The
 * site's own PKI still carries the fingerprint — the CA certificate issued on
 * install day has notBefore Sep 23 14:05:49 2026 GMT, eight hours later than the
 * real instant.
 *
 * "The nodes agree" is not "the clock is correct", and an isolated deployment
 * has nothing that can tell the two apart. So the tooling has to:
 *
 *   1. make the authority decision deliberate — the server either adopts a
 *      supplied instant or is explicitly vouched for, and refuses before it
 *      writes anything;
 *   2. pin the display timezone identically on both roles, because the console
 *      renders the configuration update time with the answering node's local
 *      zone (internal/runtime/configuration_view.go), so one cluster can
 *      otherwise show two wall clocks for the same instant;
 *   3. measure every node against a reference outside the node set, because a
 *      spread test cannot see a uniform offset.
 *
 * The lease half of the same incident is checked here too. After a backwards
 * clock step a stored expiry is stranded hours into the future, and a renewal
 * that only asks "is more than half the TTL left?" answers "nothing to do" for
 * exactly as long as the step was. The one-minute VIP ownership lease was pinned
 * roughly eight hours ahead: it still authorised its owner, but it no longer
 * expired when that owner stopped renewing it, so automatic failover was
 * silently disabled for the whole window. The detector must therefore measure
 * the distance from the current clock — not from the record's own UpdatedAt,
 * which the same renewal rewrote, so the difference between them is always the
 * granted TTL and carries no clock information at all.
 *
 * Run: node tools/verify-clock-mesh.cjs [--repo <path>]
 */
'use strict';

const fs = require('fs');
const path = require('path');

const repo = (() => {
  const flag = process.argv.indexOf('--repo');
  if (flag !== -1 && process.argv[flag + 1]) return process.argv[flag + 1];
  return path.resolve(__dirname, '..');
})();

const read = (relative) => {
  const file = path.join(repo, relative);
  if (!fs.existsSync(file)) throw new Error(`missing file: ${relative}`);
  return fs.readFileSync(file, 'utf8');
};

const failures = [];
const check = (name, ok, detail) => {
  if (ok) {
    console.log(`ok   ${name}`);
  } else {
    failures.push(name);
    console.error(`FAIL ${name}${detail ? ` — ${detail}` : ''}`);
  }
};

const mesh = read('scripts/clusterguard-clock-mesh.sh');
const installer = read('scripts/install_clusterguard.sh');
const lease = read('internal/coordination/lease.go');
const leaseTest = read('internal/coordination/lease_test.go');
const nfpm = read('packaging/rpm/nfpm.yaml');
const rpmBuilder = read('scripts/build-clusterguard-rpm.sh');
const docEnglish = read('docs/en-US/offline-rpm-install.md');
const docChinese = read('docs/zh-CN/offline-rpm-install.md');

const at = (needle) => mesh.indexOf(needle);

// --- Gate 1: becoming the clock authority is a deliberate decision ----------
// An isolated deployment has no upstream, so whatever the server believes
// becomes the truth for every node and then, through hwclock, for every future
// boot. That decision may not happen by omission.
check(
  'clock mesh: --server refuses to serve a clock nobody vouched for',
  /if \[\[ "\$mode" == "server" && -z "\$set_utc" && "\$accept_current_time" != true \]\]/.test(mesh),
  'a server that adopts whatever its own clock says is how a local-time RTC becomes the cluster standard',
);
const guard = mesh.slice(at('Refusing to become the clock authority'), at('Refusing to become the clock authority') + 1400);
check(
  'clock mesh: the refusal names both ways to proceed',
  guard.includes('--set-utc') && guard.includes('--accept-current-time'),
);
check(
  'clock mesh: the refusal says why it is permanent',
  guard.includes('hwclock --systohc'),
  'the hardware clock is what makes an unverified offset survive every reboot',
);
check(
  'clock mesh: the refusal happens before anything is configured or persisted',
  at('Refusing to become the clock authority') !== -1 &&
    at('Refusing to become the clock authority') < at('cat > /etc/chrony.conf') &&
    at('Refusing to become the clock authority') < at('hwclock --systohc --utc'),
);

// --- Gate 2: the clock can be set, and it is set with chrony stopped --------
check(
  'clock mesh: --set-utc adopts an explicit instant and reports the drift',
  mesh.includes('date -u -s "@${requested_epoch}"') &&
    /this clock was off by %s seconds/.test(mesh),
);
check(
  'clock mesh: the instant is parsed with a GNU and a BSD spelling',
  mesh.includes('date -u -d "$1" +%s') && mesh.includes('date -u -j -f "%Y-%m-%dT%H:%M:%SZ"'),
  'the node runs one or the other; a parse that silently returns nothing hands the clock back to the default',
);
check(
  'clock mesh: chrony is stopped before the clock is stepped',
  at('systemctl stop chronyd') !== -1 && at('systemctl stop chronyd') < at('date -u -s "@${requested_epoch}"'),
  'the only writer of the clock must not argue with the manual step',
);
check(
  'clock mesh: the clock is persisted as UTC hardware time',
  mesh.includes('timedatectl set-local-rtc 0') && mesh.includes('hwclock --systohc --utc'),
);

// --- Gate 3: the display timezone is one cluster-wide choice ----------------
// The console renders the configuration update time in the answering node's
// local zone, so a cluster with mixed zones shows two wall clocks for one
// instant. The zone therefore belongs to the cluster, not to each node's OS.
const timezoneLanding = at('timedatectl set-timezone "$timezone"');
check(
  'clock mesh: the display timezone is applied at all',
  timezoneLanding !== -1,
);
check(
  'clock mesh: the display timezone is applied before either role is configured',
  timezoneLanding !== -1 &&
    timezoneLanding < at('cat > /etc/chrony.conf') &&
    timezoneLanding < at('# Managed by ClusterGuard HA. The server is the fixed local time authority.'),
  'applying it inside one branch is how the other role keeps whatever the OS installed',
);
check(
  'clock mesh: an unknown timezone is refused instead of written',
  mesh.includes('Unknown timezone:') && mesh.includes('-f "/usr/share/zoneinfo/${timezone}"'),
);
check(
  'clock mesh: leaving the timezone unmanaged is announced, not silent',
  mesh.includes('is NOT managed here'),
  'silence here is what let one cluster display two wall clocks',
);
check(
  'clock mesh: --timezone is part of the documented interface',
  mesh.includes('--timezone ZONE'),
);
check(
  'clock mesh: --dry-run reports the decisions and changes nothing',
  mesh.includes('dry-run: nothing was changed') &&
    at('dry-run: nothing was changed') < timezoneLanding &&
    at('dry-run: nothing was changed') < at('cat > /etc/chrony.conf'),
);
check(
  'clock mesh: the isolated authority still has no external upstream',
  mesh.includes('local stratum 10') && /^server \$\{server_address\} iburst prefer$/m.test(mesh),
);

// --- Gate 4: the installer measures nodes against a reference outside them --
// The spread test alone is blind to a uniform offset, which is exactly the
// shape of this incident: 2 seconds of spread, 8 hours of error.
check(
  'installer: a reference instant is taken from the installer host',
  installer.includes('reference_epoch="$(date -u +%s)"') && installer.includes('reference_stamp="$(date -u +%Y-%m-%dT%H:%M:%SZ)"'),
);
check(
  'installer: every node is compared against that reference, not only against its peers',
  installer.includes('drift=$(( epoch - reference_epoch ))'),
);
check(
  'installer: the comparison is two-sided, so a slow clock is caught too',
  installer.includes('drift=$(( -drift ))'),
);
check(
  'installer: the absolute comparison has a bounded tolerance',
  installer.includes('if (( drift > 30 )); then'),
);
check(
  'installer: the failure names the shape of the mistake',
  installer.includes('RTC 中存的是本地时间、内核却按 UTC 读取'),
);
check(
  'installer: mutual agreement is no longer reported as verification',
  !installer.includes('节点时钟一致性已确认，最大偏差 ${skew} 秒') &&
    installer.includes('与本机 UTC 相差均不超过 30 秒'),
  'the old wording claimed the clock had been verified when all it had shown was that the nodes matched',
);
check(
  'installer: the display timezone is a single explicit cluster choice',
  installer.includes('cluster_timezone_zone()') &&
    installer.includes('--timezone) need_value "$@"; cluster_timezone="$2"'),
);
check(
  'installer: the authority is told which instant it is vouching for',
  installer.includes("clusterguard-clock-mesh.sh --server --accept-current-time --subnet '${source_subnet}' ${zone_arg}"),
  'the acknowledgement must be earned by the measurement above, not passed unconditionally',
);
check(
  'installer: every client is given the same display timezone as the authority',
  installer.includes("clusterguard-clock-mesh.sh --client --server-address '${source_host}' ${zone_arg}"),
);

// --- Gate 5: a backwards clock step cannot silently disable failover --------
check(
  'lease: one constant bounds the grant and the legitimate distance from now',
  /const maxLeaseTTL = time\.Minute/.test(lease),
  'a renewal writes clock + one TTL, so an expiry further out than that cannot come from elapsed time',
);
check(
  'lease: a renewal re-anchors an expiry stranded beyond one full grant',
  lease.includes('staleAnchor := record.Lease.ExpiresAt.Sub(now) > maxLeaseTTL') &&
    lease.includes('if !staleAnchor && record.Lease.ExpiresAt.Sub(now) > ttl/2 {'),
  'with only the headroom test, "more than half a TTL left" reads as "nothing to do" for as long as the step was',
);
check(
  'lease: the transfer path re-anchors the same way',
  lease.includes('requestedExpiry.After(lease.ExpiresAt) || lease.ExpiresAt.Sub(now) > maxLeaseTTL'),
);
check(
  "lease: staleness is measured from the current clock, never from the record's own update time",
  !/ExpiresAt\.Sub\(record\.UpdatedAt\)/.test(lease),
  'a renewal rewrites expiry and UpdatedAt together, so their difference is always the granted TTL and carries no clock information',
);
check(
  'lease: extending a lease still never shortens an existing expiry',
  lease.includes('requestedExpiry.After(lease.ExpiresAt)'),
);
for (const name of [
  'TestQuorumStableLeaseReanchorsAfterTheClockMovesBackwards',
  'TestQuorumSingleLeaseReanchorsAfterTheClockMovesBackwards',
  'TestQuorumLeaseReanchorBoundaryIsOneFullTTL',
]) {
  check(`lease: ${name} pins the behaviour`, leaseTest.includes(name));
}

// --- Gate 6: a site can receive a corrected copy of the tool ----------------
// The installer copies the mesh tool onto every node, but a site that already
// has it can only be corrected through the package path. Leaving it
// installer-only shipped a fix that reached fresh installs and nothing else.
check(
  'packaging: the clock mesh tool is shipped, with its site path declared',
  /src: \$\{CG_RPM_STAGE\}\/scripts\/clusterguard-clock-mesh\.sh[\s\S]{0,220}?dst: \/usr\/local\/sbin\/clusterguard-clock-mesh\.sh/.test(nfpm),
);
check(
  'packaging: the RPM staging helper list carries it',
  rpmBuilder.includes('clusterguard-clock-mesh.sh'),
);

// --- Gate 7: the operator is told, in both languages ------------------------
check(
  'offline install doc: the absolute check and the single display timezone are documented in Chinese',
  docChinese.includes('各节点 UTC 与运维机相差不超过 30 秒') && docChinese.includes('12.7'),
);
check(
  'offline install doc: the same is documented in English',
  docEnglish.includes("within 30 seconds of the operations machine") && docEnglish.includes('12.7'),
);
check(
  'offline install doc: both languages explain the uniform-offset shape',
  docChinese.includes('虚拟化环境下 RTC 存本地时间、内核按 UTC 读取会让整个集群一致地快一个时区偏移') &&
    docEnglish.includes('uniformly ahead by one zone offset'),
);

if (failures.length) {
  console.error(`\n${failures.length} check(s) failed.`);
  process.exit(1);
}
console.log('\nall clock mesh checks passed.');
