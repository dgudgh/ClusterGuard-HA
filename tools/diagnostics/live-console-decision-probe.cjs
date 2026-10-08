#!/usr/bin/env node
// Read live update records back through the console's own decision functions.
//
// Why this exists: the panel's badge and the panel's action gates read different
// fields of the same record - the badge reads the event chain, the gates read the
// raw status. When those disagree, the panel can describe one record and offer an
// action aimed at that same record with a verdict that no longer matches, and the
// screenshot alone cannot tell you which function produced which line. This runs
// the shipped functions over the shipped JSON so the answer is reproducible.
//
// Usage:
//   ssh root@<leader> 'set -a; . /etc/clusterguard/agent.env; set +a;
//     curl -sk -H "Authorization: Bearer $CG_CONTROL_TOKEN" \
//       https://127.0.0.1:3000/api/v1/platform/updates' > /tmp/updates.json
//   node tools/diagnostics/live-console-decision-probe.cjs /tmp/updates.json
//
// Read-only: it parses a file and evaluates a block of the page verbatim. It never
// talks to a cluster and never writes anything.

const fs = require('fs');
const path = require('path');

const htmlPath = process.argv[3]
  || path.join(__dirname, '..', '..', 'internal', 'api', 'console.html');
const jsonPath = process.argv[2];
if (!jsonPath) {
  console.error('usage: live-console-decision-probe.cjs <updates.json> [console.html]');
  process.exit(2);
}

const source = fs.readFileSync(htmlPath, 'utf8');
const first = 'const softwareUpdateCompletedAttempt = job =>';
const last = "&& softwareUpdateOutcome(item.job) === 'failed';";
const start = source.indexOf(first);
const end = source.indexOf(last, start);
if (start < 0 || end < 0) {
  console.error('the console no longer contains the block this probe reads; update the markers');
  process.exit(2);
}
// The block spans the badge helpers, the actionable test, the subject resolver and
// the retry decision, verbatim. Evaluating the shipped text is the point: a copy of
// the logic here could pass while the console drifted away from it.
const block = source.slice(start, end + last.length);

const state = { softwareUpdates: { packages: [] } };
const console_ = new Function('state', `
  ${block}
  return { softwareUpdateCompletedAttempt, softwareUpdateOutcome, softwareUpdateActionable,
           softwareUpdatePayloadApplied, softwareUpdateAppliedAt, newestAppliedSoftwareUpdate,
           pendingSoftwareUpdate, softwareUpdateSubject, softwareUpdateRollbackTarget,
           softwareUpdateIsHotfix, softwareUpdateIsRetry };
`)(state);

state.softwareUpdates = JSON.parse(fs.readFileSync(jsonPath, 'utf8')).result;

const STATUS = {
  uploaded: '已上传', queued: '排队中', running: '升级中', planned: '计划已通过',
  succeeded: '升级成功', failed: '升级失败', rolled_back: '已回退', interrupted: '状态待确认',
  applied_attempt_failed: '已生效 · 本次尝试失败',
};
const MODE = (value, kind) => (kind === 'hotfix'
  ? { plan: '只读热修计划', execute: '热修应用', retry: '重新执行热修', resume: '续跑热修', rollback: '热修回退' }
  : { plan: '只读计划', execute: '滚动升级', resume: '续跑升级', rollback: '受控回退' })[value] || value || '-';
const stamp = value => (value ? new Date(value).toISOString().replace('.000Z', 'Z') : '-');

const packages = state.softwareUpdates.packages || [];
const subject = console_.softwareUpdateSubject();
const newest = console_.newestAppliedSoftwareUpdate();
const rollbackTarget = console_.softwareUpdateRollbackTarget();

console.log('records, in the order the server sends them');
console.log('');
console.log('  #  patch_id                          kind     raw status   outcome                  payload-on-disk  actionable  applied_at');
for (const [i, item] of packages.entries()) {
  const job = item.job || null;
  console.log(
    '  ' + String(i).padEnd(2) +
    String(item.package?.patch_id || '?').padEnd(34) +
    String(item.package?.kind || '-').padEnd(9) +
    String(job?.status || '(none)').padEnd(13) +
    String(job ? console_.softwareUpdateOutcome(job) : '-').padEnd(25) +
    String(console_.softwareUpdatePayloadApplied(item)).padEnd(17) +
    String(console_.softwareUpdateActionable(item)).padEnd(12) +
    stamp(console_.softwareUpdateAppliedAt(item)) +
    (item === subject ? '  <= subject' : '') +
    (item === newest && item !== subject ? '  <= newest applied' : ''),
  );
}

if (!subject) {
  console.log('\nno subject: the panel falls back to an empty state.');
  process.exit(0);
}

const job = subject.job || null;
const record = subject.package || {};
const status = job?.status;
const retry = console_.softwareUpdateIsRetry(subject);
const completed = console_.softwareUpdateCompletedAttempt(job);
const note = console_.softwareUpdateOutcome(job) === 'applied_attempt_failed'
  ? `补丁已生效：最近一次完成的执行于 ${new Date(completed?.updated_at).toLocaleString('zh-CN')} 逐文件校验通过。`
  : '';

console.log('');
console.log('what the panel renders from that subject');
console.log('  待升级目标版本 : ' + (record.target_version || '-'));
console.log('  标签           : ' + (console_.softwareUpdateActionable(subject) ? '待升级目标版本'
  : console_.softwareUpdateOutcome(job) === 'succeeded' ? '最近完成版本' : '最近处理版本'));
console.log('  状态           : ' + (STATUS[console_.softwareUpdateOutcome(job)] || console_.softwareUpdateOutcome(job)));
console.log('  模式           : ' + MODE(job?.mode, record.kind));
console.log('  当前节点       : ' + ((job?.node) || '-'));
console.log('  说明           : ' + (note
  ? `${note}本次${MODE(job?.mode, record.kind)}未生效：${job?.message || ''}`
  : (job?.message || '等待生成只读计划。')));

const pending = console_.pendingSoftwareUpdate();
const executeLabel = retry ? '重新执行' : console_.softwareUpdateIsHotfix(subject) ? '应用热修补丁' : '滚动升级';
console.log('');
console.log('action row');
console.log('  execute  : label=' + executeLabel + '  disabled=' + String(
  !state.softwareUpdates.available || !pending ||
  !record.rolling || !(retry || ['uploaded', 'planned'].includes(status || 'uploaded')),
));
console.log('  resume   : hidden=' + String(!(pending && status === 'failed' && !console_.softwareUpdateIsHotfix(subject))));
const rollbackApplies = !!rollbackTarget && rollbackTarget === subject;
console.log('  rollback : hidden=' + String(!rollbackApplies) + '  target=' + (rollbackTarget?.package?.patch_id || '(none)'));
if (rollbackApplies) {
  console.log('             -> the confirmation would name ' + (rollbackTarget.package.patch_id || '-'));
} else if (rollbackTarget) {
  console.log('             -> the only record a rollback may revert is ' + rollbackTarget.package.patch_id
    + '; the panel describes ' + record.patch_id + ', so the button is withheld');
} else {
  console.log('             -> nothing on disk carries a rollback artifact');
}
console.log('');
console.log('why each candidate action is or is not offered');
for (const item of packages) {
  const itemJob = item.job || null;
  const id = item.package?.patch_id || '?';
  const reasons = [];
  if (item.incompatible) reasons.push('package targets another release line');
  if (!itemJob) reasons.push('no operation yet: nothing has been applied from it');
  else {
    if (itemJob.verification_required) reasons.push('the job record needs verification');
    if (console_.softwareUpdatePayloadApplied(item)) reasons.push('its payload is on disk');
    else if (['succeeded', 'rolled_back'].includes(itemJob.status)) reasons.push('raw status is terminal: ' + itemJob.status);
  }
  if (console_.softwareUpdateActionable(item) && newest && newest !== item
      && console_.softwareUpdateAppliedAt(newest) > console_.softwareUpdateAppliedAt(item)) {
    // The console does not retire a record on a timestamp: supersession is a signed
    // declaration in the package manifest, the server enforces it, and the panel shows
    // the refusal. This line says where that verdict will come from, not what it is.
    reasons.push('older than ' + newest.package.patch_id + ', which is applied: the server decides '
      + 'supersession from the signed manifest and is expected to refuse this action');
  }
  console.log('  ' + id.padEnd(34) + (console_.softwareUpdateActionable(item) ? 'actionable' : 'not actionable') +
    (reasons.length ? '  (' + reasons.join('; ') + ')' : ''));
}
