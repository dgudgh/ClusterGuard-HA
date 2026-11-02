const {test} = require('node:test');
const assert = require('node:assert/strict');
const {assessProblems, coverageBaselines, localizedDescription} = require('./hotfix-publication-policy.cjs');

test('frozen tooling AND missing payload defects must match exactly; live defects never exempt', () => {
  const item = {archive:'old',body:{id:'old'},publication:{status:'frozen',known_defects:['tooling','missing agent']}};
  const found = new Map([['old',['tooling','missing agent']]]);
  assert.deepEqual(assessProblems([item],found),{live:[],undeclared:[],stale:[]});
  item.publication.known_defects.pop(); assert.equal(assessProblems([item],found).undeclared.length,1);
  item.publication.known_defects.push('missing agent','invented defect');
  assert.equal(assessProblems([item],found).stale.length,1);
  for (const status of ['current','superseded',undefined]) {
    item.publication.status=status; assert.equal(assessProblems([item],found).live.length,2);
  }
});

test('retired baseline stops future coverage only when its exact publication is frozen', () => {
  const specs=[{body:{id:'old',base_commit:'old-base'}},{body:{id:'new',base_commit:'field-base'}}];
  const ledger=new Map([['old',{status:'frozen'}],['new',{status:'current'}]]);
  const path=body=>body.id;
  assert.deepEqual(coverageBaselines(specs,ledger,path),['field-base']);
  for(const status of ['current','superseded','typo']) {
    ledger.set('old',{status}); assert.deepEqual(coverageBaselines(specs,ledger,path),['old-base','field-base']);
  }
  ledger.delete('old');assert.deepEqual(coverageBaselines(specs,ledger,path),['old-base','field-base']);
});

test('historical text is explicitly missing, never fabricated; active incomplete manifest fails', () => {
  const manifest={hotfix_id:'old',publication:{status:'frozen'}};
  assert.match(localizedDescription(manifest,{},'fix','en'),/Not recorded.*not an install/);
  assert.match(localizedDescription(manifest,{},'fix','zh'),/未记录.*非可安装/);
  assert.equal(localizedDescription(manifest,{fix:{en:'signed original'}},'fix','en'),'signed original');
  for (const status of ['current','superseded',undefined]) {
    manifest.publication.status=status;
    assert.throws(()=>localizedDescription(manifest,{},'fix','en'),/missing signed fix.en/);
    assert.throws(()=>localizedDescription(manifest,{fix:{en:' '}},'fix','en'),/missing signed/);
  }
});

test('real renderer retains incomplete frozen bytes and rejects the same active artifact', t => {
  const fs=require('node:fs'),path=require('node:path'),os=require('node:os');
  const {spawnSync}=require('node:child_process');
  const root=fs.mkdtempSync(path.join(os.tmpdir(),'cg-frozen-render-'));
  t.after(()=>fs.rmSync(root,{recursive:true,force:true}));
  fs.mkdirSync(path.join(root,'release/3.1.4.1'),{recursive:true});
  fs.mkdirSync(path.join(root,'clusterguard-hotfix'));
  const manifest={kind:'hotfix',hotfix_id:'3.1.4.1',patch_version:'3.1.4.1',title:{en:'old',zh:'旧'},
    build_commit:'a'.repeat(40),base_commit:'b'.repeat(40),fix_commits:['c'.repeat(40)],
    severity:'P1',source:{version:'2.2',release:'113'},target:{version:'2.2',release:'113'},
    files:[],rollback:'original rollback',fixes:[{commit:'c'.repeat(40),title:{en:'old',zh:'旧'}}]};
  fs.writeFileSync(path.join(root,'clusterguard-hotfix/HOTFIX-MANIFEST.json'),JSON.stringify(manifest));
  const file='release/3.1.4.1/clusterguard-3.1.4.1.x86_64.cgpatch';
  assert.equal(spawnSync('tar',['-czf',path.join(root,file),'-C',root,'clusterguard-hotfix']).status,0);
  const archive=fs.readFileSync(path.join(root,file));
  const ledger=path.join(root,'ledger.json'),en=path.join(root,'en.md'),zh=path.join(root,'zh.md');
  const args=[path.resolve(__dirname,'../scripts/render-hotfix-catalog.cjs'),'--artifact-root',root,'--ledger',ledger,'--out-en',en,'--out-zh',zh];
  fs.writeFileSync(ledger,JSON.stringify({publications:[{file,status:'frozen',known_defects:[]}]}));
  let result=spawnSync(process.execPath,args,{encoding:'utf8'});
  assert.equal(result.status,0,result.stderr);
  assert.match(fs.readFileSync(en,'utf8'),/Not recorded in this frozen signed manifest/);
  assert.match(fs.readFileSync(zh,'utf8'),/此冻结签名清单未记录/);
  assert.deepEqual(fs.readFileSync(path.join(root,file)),archive);
  fs.writeFileSync(ledger,JSON.stringify({publications:[{file,status:'current'}]}));
  result=spawnSync(process.execPath,args,{encoding:'utf8'});
  assert.notEqual(result.status,0);assert.match(result.stderr,/missing signed symptom.en/);
});
