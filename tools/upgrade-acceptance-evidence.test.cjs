const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const os=require('node:os');
const path=require('node:path');
const {execFileSync}=require('node:child_process');
const {sourceDigest,digest,ids,validateReport}=require('./upgrade-acceptance-evidence.cjs');
function fixture(t) {
 const root=fs.mkdtempSync(path.join(os.tmpdir(),'cg-acceptance-'));
 t.after(()=>fs.rmSync(root,{recursive:true,force:true}));
 fs.mkdirSync(path.join(root,'docs'),{recursive:true});fs.mkdirSync(path.join(root,'internal'));
 fs.writeFileSync(path.join(root,'docs/upgrade-validation-chain.md'),'contract v2');
 fs.writeFileSync(path.join(root,'internal/feature.go'),'original source');
 execFileSync('git',['init','-q'],{cwd:root});execFileSync('git',['add','.'],{cwd:root});
 const artifact=path.join(root,'HF-TEST.cgpatch'); fs.writeFileSync(artifact,'synthetic package - NOT a signed delivery');
 const proof=path.join(root,'proof.txt');fs.writeFileSync(proof,'synthetic acceptance evidence - NOT field acceptance');
 const reference=file=>({path:path.basename(file),sha256:digest(fs.readFileSync(file))});
 const report={schema_version:1,contract_sha256:digest(fs.readFileSync(path.join(root,'docs/upgrade-validation-chain.md'))),source_sha256:sourceDigest(root),package_id:'HF-TEST',artifacts:[reference(artifact)],checks:[...ids('ART'),...ids('FIELD')].map(id=>({id,status:'passed',evidence:[reference(proof)]})),recorded_by:'isolated test fixture',recorded_at:new Date().toISOString(),field:{cluster_id:'fixture-only',controller_ids:['node-a','node-b','node-c']}};
 const file=path.join(root,'report.json');const write=()=>fs.writeFileSync(file,JSON.stringify(report));write();
 return {root,report,file,write,artifact,proof};
}
test('complete evidence inventory closes only its declared version and scope',t=>{
 const f=fixture(t);assert.equal(validateReport(f.root,f.file,'field').package_id,'HF-TEST');
 f.report.checks=f.report.checks.filter(c=>c.id.startsWith('ART'));delete f.report.field;f.write();
 assert.equal(validateReport(f.root,f.file,'artifact').package_id,'HF-TEST');
 assert.throws(()=>validateReport(f.root,f.file,'field'));
});
for(const [name,mutate] of [
 ['untracked new feature',f=>fs.writeFileSync(path.join(f.root,'internal/new.go'),'new function')],
 ['old source',f=>fs.writeFileSync(path.join(f.root,'internal/feature.go'),'new function')],
 ['old contract',f=>fs.writeFileSync(path.join(f.root,'docs/upgrade-validation-chain.md'),'contract v3')],
 ['changed artifact',f=>fs.appendFileSync(f.artifact,'tampered')],
 ['changed proof',f=>fs.appendFileSync(f.proof,'tampered')],
 ['missing check',f=>f.report.checks.pop()],
 ['failed check',f=>f.report.checks[0].status='failed'],
 ['missing proof',f=>f.report.checks[0].evidence=[]],
 ['duplicate check',f=>f.report.checks.push(f.report.checks[0])],
 ['unknown field',f=>f.report.bypass=true],
 ['unknown schema',f=>f.report.schema_version=99],
 ['future timestamp',f=>f.report.recorded_at='2999-01-01T00:00:00Z'],
 ['duplicate field node',f=>f.report.field.controller_ids=['a','a']],
 ['symlink proof',f=>{fs.renameSync(f.proof,f.proof+'.saved');fs.symlinkSync(f.proof+'.saved',f.proof);}],
]) test('reject '+name,t=>{const f=fixture(t);mutate(f);f.write();assert.throws(()=>validateReport(f.root,f.file,'field'));});
test('CLI stages can close new evidence and refuse stale evidence without weakening default strict',t=>{
 const f=fixture(t);
 const repo=path.resolve(__dirname,'..');const gate=path.join(repo,'tools/verify-upgrade-validation-chain.cjs');
 const text=fs.readFileSync(gate,'utf8');const inventory=text.match(/const READ_FILES = \[([\s\S]*?)\n\];/)[1];
 const files=['docs/upgrade-validation-chain.md','docs/zh-CN/upgrade-validation-chain.md','tools/upgrade-acceptance-evidence.cjs',...Array.from(inventory.matchAll(/'([^']+)'/g),match=>match[1])];
 for(const file of new Set(files)) {const target=path.join(f.root,file);fs.mkdirSync(path.dirname(target),{recursive:true});fs.copyFileSync(path.join(repo,file),target);}
 execFileSync('git',['add','.'],{cwd:f.root});
 f.report.source_sha256=sourceDigest(f.root);f.report.contract_sha256=digest(fs.readFileSync(path.join(f.root,'docs/upgrade-validation-chain.md')));f.write();
 const run=args=>{try {return {status:0,output:execFileSync(process.execPath,[gate,'--repo',f.root,...args],{encoding:'utf8',stdio:['ignore','pipe','pipe']})};}catch(error){return {status:error.status,output:String(error.stdout)+String(error.stderr)};}};
 assert.equal(run(['--strict']).status,1);
 assert.equal(run(['--stage','source','--strict']).status,0);
 assert.equal(run(['--stage','artifact','--strict','--acceptance-report',f.file]).status,0);
 assert.equal(run(['--stage','field','--strict','--acceptance-report',f.file]).status,0);
 assert.equal(run(['--stage','typo','--strict']).status,1);
 assert.equal(run(['--stage','source','--acceptance-report',f.file]).status,1);
 assert.equal(run(['--contract-only','--stage','source']).status,1);
 assert.equal(run(['--acceptance-report']).status,1);
 assert.equal(run(['--print-evidence-binding','--strict']).status,1);
 assert.equal(run(['--print-evidence-binding']).status,0);
 fs.appendFileSync(f.artifact,'changed');
 assert.equal(run(['--stage','field','--acceptance-report',f.file]).status,1);
});
