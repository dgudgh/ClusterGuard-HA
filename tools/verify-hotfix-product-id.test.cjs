const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const {spawnSync}=require('node:child_process');
const builder=fs.readFileSync(path.join(__dirname,'../scripts/build-hotfix-patch.sh'),'utf8');
const begin=builder.indexOf('if [[ "${patch_version_declared}" == "present" ]]');
const end=builder.indexOf('[[ "${rpm_version}"',begin);
assert.ok(begin>=0 && end>begin);
const rule=builder.slice(begin,end);
test('builder requires version identity while retaining historical HF declarations',()=>{
  for(const [id,version,declared,ok] of [['3.1.1.4','3.1.1.4','present',true],['HF-2026-1008-05','3.1.1.4','present',false],['3.1.1.3','3.1.1.4','present',false],['HF-2026-1008-05','','absent',true],['3.1.1.4','','absent',false]]){
    const result=spawnSync('bash',['-c','die(){ exit 73; }; '+rule],{env:{...process.env,hotfix_id:id,patch_version:version,patch_version_declared:declared}});
    assert.equal(result.status===0,ok,`${id}/${version}/${declared}`);
  }
});
