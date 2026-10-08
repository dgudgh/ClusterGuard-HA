const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const gate = fs.readFileSync(path.join(__dirname, 'verify-hotfix-patch-catalog.cjs'), 'utf8');
const begin = gate.indexOf('const brokenChain = [];');
const end = gate.indexOf('const notCurrent = []', begin);
assert.ok(begin >= 0 && end > begin, 'the real revision-chain gate must be found');
function run(next = {}, before = {}, nextFile = 'release/2.2-105-hotfixes/clusterguard-3.1.1.2.x86_64.cgpatch') {
  const oldFile = 'release/2.2-105-hotfixes/clusterguard-3.1.1.1.x86_64.cgpatch';
  const original = { revision: 0, patch_version: '3.1.1.1', sha256: 'old', superseded_by: nextFile, ...before };
  const revision = { revision: 1, patch_version: '3.1.1.2', sha256: 'new', supersedes_sha256: 'old', ...next };
  let verdict;
  vm.runInNewContext(gate.slice(begin, end), {
    path, publications: new Map([[oldFile, original], [nextFile, revision]]),
    check: (_title, passed, detail) => { verdict = { passed, detail }; },
  });
  return verdict;
}
test('versioned revision accepts a signed replacement chain without a legacy r suffix', () => assert.equal(run().passed, true));
test('legacy revision keeps its r suffix requirement', () => {
  assert.equal(run({ patch_version: undefined }, { patch_version: undefined }, 'release/clusterguard-ha-hotfix-HF-2026-1008-02-r1-2.2-105.x86_64.cgpatch').passed, true);
  assert.equal(run({ patch_version: undefined }, { patch_version: undefined }).passed, false);
});
test('wrong version filename, skipped bug revision and broken links are rejected', () => {
  for (const changes of [{ patch_version: '3.1.1.3' }, { patch_version: '3.1.2.2' }, { patch_version: 'invalid' }, { revision: 2 }, { supersedes_sha256: 'missing' }]) assert.equal(run(changes).passed, false, JSON.stringify(changes));
  assert.equal(run({}, { superseded_by: null }).passed, false);
  assert.equal(run({}, {}, 'release/clusterguard-3.1.1.9.x86_64.cgpatch').passed, false);
});
