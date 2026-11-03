const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const source = fs.readFileSync(path.join(__dirname, '../scripts/clusterguard-update-job.sh'), 'utf8');
const fn = source.slice(source.indexOf('import_private_history() {'), source.indexOf('\nimport_private_history\n'));
const record = (id, mode, status) => ({ patch_id: 'patch', operation_id: id, mode, status });
const rollback = record('old-rollback', 'rollback', 'rolled_back');
const plan = record('new-plan', 'plan', 'planned');
function run(local, replicated, localHistory, replicatedHistory) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'cg-root-history-'));
  try {
    const job = path.join(root, 'jobs/patch'), history = path.join(root, 'history/patch');
    fs.mkdirSync(job, { recursive: true }); fs.mkdirSync(history, { recursive: true });
    for (const [dir, state, rows] of [[job, local, localHistory], [history, replicated, replicatedHistory]]) {
      fs.writeFileSync(path.join(dir, 'status.json'), JSON.stringify(state));
      fs.writeFileSync(path.join(dir, 'operations.jsonl'), rows.map(x => JSON.stringify(x)).join('\n') + '\n');
    }
    const helper = path.join(root, 'helper');
    fs.writeFileSync(helper, '#!/bin/bash\nset -eu\ncase "$2" in check-file) test -f "$3" && test ! -L "$3";; snapshot) cp "$3" "$4";; *) exit 91;; esac\n', { mode: 0o700 });
    const before = fs.readFileSync(path.join(job, 'operations.jsonl'), 'utf8');
    const result = spawnSync('bash', ['-c', 'set -euo pipefail\nprivate_root=$1; job_dir=$1/jobs/patch; patch_id=patch; status_file=$job_dir/status.json; workspace_helper=$1/helper; jq_binary=$(command -v jq)\ndie() { echo "$*" >&2; exit 1; }\n' + fn + '\nimport_private_history', 'test', root], { encoding: 'utf8' });
    return { ...result, state: JSON.parse(fs.readFileSync(path.join(job, 'status.json'))), before, after: fs.readFileSync(path.join(job, 'operations.jsonl'), 'utf8') };
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
}
test('root history proves rollback → new plan, without replacing either result', () => {
  const r = run(plan, rollback, [rollback, plan], [rollback]);
  assert.equal(r.status, 0, r.stderr); assert.deepEqual(r.state, plan); assert.ok(r.after.startsWith(r.before));
  assert.ok(r.after.includes(JSON.stringify(rollback)));
});
test('known failed attempt can follow a terminal ancestor', () => {
  const failed = record('new-execute', 'execute', 'failed');
  const r = run(failed, rollback, [rollback, plan, failed], [rollback]);
  assert.equal(r.status, 0, r.stderr); assert.deepEqual(r.state, failed);
});
for (const [name, local, replicated, lh, rh] of [
  ['unknown ancestry', plan, rollback, [plan], [rollback]],
  ['active different operation', record('new-execute','execute','running'), rollback, [rollback,record('new-execute','execute','running')], [rollback]],
  ['same operation contradictory terminal', record('old-rollback','rollback','failed'), rollback, [record('old-rollback','rollback','failed')], [rollback]],
  ['one identity with two modes', plan, rollback, [rollback,plan,record('new-plan','execute','planned')], [rollback]],
  ['foreign package in root history', plan, rollback, [rollback,plan], [{...rollback,patch_id:'foreign'}]],
  ['missing modes cannot prove different-operation ancestry', {...plan,mode:null}, {...rollback,mode:null}, [{...rollback,mode:null},{...plan,mode:null}], [{...rollback,mode:null}]],
]) test(name + ' still fails closed', () => { const r=run(local,replicated,lh,rh); assert.notEqual(r.status,0); assert.match(r.stderr,/CG_HISTORY_OVERWRITE_FORBIDDEN/); assert.equal(r.after,r.before); });
