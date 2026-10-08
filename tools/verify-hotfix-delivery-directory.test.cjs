const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const vm = require('node:vm');
const gate = fs.readFileSync(path.join(__dirname, 'verify-hotfix-patch-catalog.cjs'), 'utf8');
const directoryCode = gate.slice(gate.indexOf('const artifactDirFor ='), gate.indexOf('const failures ='));

test('current directory uses product version; legacy and frozen identities preserve their baseline location', () => {
  const publications = new Map();
  const resolve = body => vm.runInNewContext(directoryCode + '\nartifactDirFor(body)', {path, publications, body});
  const body = {id:'HF-2026-1008-04', patch_version:'3.1.1.3', rpm_version:'2.2', rpm_release:'105'};
  assert.equal(resolve(body), 'release/3.1.1.3');
  assert.equal(resolve({...body, patch_version:undefined}), 'release/2.2-105-hotfixes');
  publications.set('old', {...body, hotfix_id:body.id, status:'superseded', file:'release/2.2-105-hotfixes/clusterguard-3.1.1.3.x86_64.cgpatch'});
  assert.equal(resolve(body), 'release/2.2-105-hotfixes');
});

test('splitting directories cannot create two current packages for one admission baseline', () => {
  const begin=gate.indexOf('const badDirCounts = []');
  const end=gate.indexOf('// A .cgpatch sitting',begin);
  const publications=new Map([
    ['release/3.1.1.2/old.cgpatch',{status:'current',release_line:'2.2-105'}],
    ['release/3.1.1.3/new.cgpatch',{status:'current',release_line:'2.2-105'}],
  ]);
  let passed;
  const run=()=>vm.runInNewContext(gate.slice(begin,end), {publications, check:(_name,ok)=>{passed=ok;}});
  run(); assert.equal(passed,false);
  publications.values().next().value.status='superseded';run();assert.equal(passed,true);
});

test('an undeclared four-part directory containing an archive is rejected', () => {
  const root=fs.mkdtempSync(path.join(os.tmpdir(),'cg-delivery-directory-'));
  try {
    fs.mkdirSync(path.join(root,'release/3.1.1.9'),{recursive:true});
    fs.writeFileSync(path.join(root,'release/3.1.1.9/stray.cgpatch'),'undeclared');
    const begin=gate.indexOf('const strayArchives = []');
    const end=gate.indexOf('// --- Publication identity',begin);
    let passed;
    vm.runInNewContext(directoryCode+gate.slice(begin,end),{fs,path,artifactRoot:root,declaredDirs:new Map(),check:(_name,ok)=>{passed=ok;}});
    assert.equal(passed,false);
  } finally {fs.rmSync(root,{recursive:true,force:true});}
});
