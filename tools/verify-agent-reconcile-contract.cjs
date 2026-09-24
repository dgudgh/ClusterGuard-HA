#!/usr/bin/env node
/**
 * verify-agent-reconcile-contract.cjs
 *
 * Regression gate for the MySQL writer reconcile loop, anchored on a live
 * three-node incident (2026-09-24, site 192.168.102.152-154) where the primary
 * flapped between writable and read-only about every ten seconds: the cluster
 * stayed "degraded", both replication links stayed "unhealthy", and the console
 * blocked every planned shutdown.
 *
 * Two independent defects produced the flap:
 *
 *  1. P0 — the reconcile unit restricted capabilities to
 *     CAP_NET_ADMIN/CAP_NET_RAW/CAP_SETUID/CAP_SETGID. Root without
 *     CAP_DAC_OVERRIDE cannot read the MySQL data directory's mysqld-auto.cnf
 *     (mysql:mysql 0640), so MySQLRoleController.IsolationStatus failed and the
 *     fail-closed loop self-isolated the owner: VIP released and MySQL fenced
 *     read-only. The unit must keep the DAC capabilities.
 *
 *  2. P0 — convergeWritableRestartState required RestartReadOnly to clear, but
 *     the durable restart fence is a permanent site invariant: every managed
 *     instance must restart read-only (server defaults file plus SET
 *     PERSIST_ONLY on every role mutation). The requirement was unsatisfiable,
 *     so keep_vip self-isolated and the controller answered the next cycle with
 *     bootstrap_primary. Only the Agent's isolation intent and runtime
 *     writability may be required to converge.
 *
 * Run: node tools/verify-agent-reconcile-contract.cjs [--repo <path>]
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

const unit = read('packaging/systemd/clusterguard-agent-reconcile.service');
const reconciler = read('internal/agent/reconciler.go');
const role = read('internal/agent/role.go');
const operations = read('docs/operations.md');

const bounding = (() => {
  const match = unit.match(/^CapabilityBoundingSet=(.*)$/m);
  return match ? match[1].trim().split(/\s+/) : [];
})();
const ambient = (() => {
  const match = unit.match(/^AmbientCapabilities=(.*)$/m);
  return match ? match[1].trim().split(/\s+/) : [];
})();

// --- Gate 1: the reconcile unit can inspect MySQL state owned by mysql ----
check(
  'reconcile unit: CapabilityBoundingSet keeps CAP_DAC_OVERRIDE',
  bounding.includes('CAP_DAC_OVERRIDE'),
  'root without DAC override cannot read <datadir>/mysqld-auto.cnf (mysql:mysql 0640)',
);
check(
  'reconcile unit: CapabilityBoundingSet keeps CAP_DAC_READ_SEARCH',
  bounding.includes('CAP_DAC_READ_SEARCH'),
);
check(
  'reconcile unit: AmbientCapabilities grants the same DAC capabilities',
  ambient.includes('CAP_DAC_OVERRIDE') && ambient.includes('CAP_DAC_READ_SEARCH'),
);
check(
  'reconcile unit: still runs the reconcile entrypoint as root',
  /^User=root$/m.test(unit) && unit.includes('--reconcile'),
);

// --- Gate 2: writable convergence must not demand the fence disappears ----
const convergeBody = (() => {
  const start = reconciler.indexOf('func (reconciler *Reconciler) convergeWritableRestartState(');
  if (start === -1) return '';
  const end = reconciler.indexOf('\nfunc ', start + 1);
  return reconciler.slice(start, end === -1 ? undefined : end);
})();
check('reconciler: convergeWritableRestartState exists', convergeBody !== '');
check(
  'reconciler: writable convergence ignores RestartReadOnly',
  /func writableRestartStateConverged\(/.test(reconciler) &&
    !/RestartReadOnly/.test(reconciler.slice(reconciler.indexOf('func writableRestartStateConverged('))),
  'requiring RestartReadOnly=false self-isolates every healthy primary because the restart fence never clears',
);
check(
  'reconciler: writable convergence still requires the isolation intent to clear',
  /!status\.PersistedReadOnly/.test(reconciler),
);
check(
  'reconciler: writable convergence still requires runtime writability',
  /status\.DatabaseReachable && !status\.ReadOnly && !status\.SuperReadOnly/.test(reconciler),
);

// --- Gate 3: the durable restart fence stays a site invariant -------------
check(
  'role controller: writable commit keeps the restart fence (PERSIST_ONLY ON)',
  role.includes('SET PERSIST_ONLY super_read_only = ON; SET PERSIST_ONLY read_only = ON'),
);
check(
  'role controller: still reads persisted globals from the data directory',
  role.includes('mysqld-auto.cnf') && role.includes('readMySQLPersistedRoleOverrides'),
);
check(
  'operations doc: restart read-only defaults still documented as required',
  /read_only=ON` and `super_read_only=ON` must be effective restart defaults/.test(operations),
);

if (failures.length) {
  console.error(`\n${failures.length} check(s) failed.`);
  process.exit(1);
}
console.log('\nall agent reconcile contract checks passed.');
