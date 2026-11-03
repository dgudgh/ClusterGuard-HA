const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),os=require('node:os'),path=require('node:path'),zlib=require('node:zlib'),{spawnSync}=require('node:child_process');
const source=fs.readFileSync(path.join(__dirname,'../scripts/build-hotfix-patch.sh'),'utf8');
test('actual archive writer emits only root carrier ownership',()=>{
 const fn=source.match(/^write_hotfix_archive\(\) \{\n.*?^\}/ms);assert(fn);
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'cg-carrier-'));try{
  fs.mkdirSync(path.join(dir,'clusterguard-hotfix'));fs.writeFileSync(path.join(dir,'clusterguard-hotfix','manifest.json'),'{}');
  const script=path.join(dir,'probe.sh'),archive=path.join(dir,'candidate.tgz');fs.writeFileSync(script,`set -euo pipefail\n${fn[0]}\nwrite_hotfix_archive "$1" "$2"\n`);
  const run=spawnSync('bash',[script,dir,archive],{encoding:'utf8'});assert.equal(run.status,0,run.stderr);
  const body=zlib.gunzipSync(fs.readFileSync(archive));let count=0;
  for(let offset=0;offset+512<=body.length;){const h=body.subarray(offset,offset+512);if(h.every(x=>x===0))break;const oct=(a,b)=>parseInt(h.subarray(a,b).toString().replaceAll('\0','').trim()||'0',8);assert.equal(oct(108,116),0,'carrier uid');assert.equal(oct(116,124),0,'carrier gid');const size=oct(124,136);if(['x','g'].includes(String.fromCharCode(h[156])))for(const m of body.subarray(offset+512,offset+512+size).toString().matchAll(/(?:^|\n)\d+ (uid|gid)=(\d+)/g))assert.equal(Number(m[2]),0,m[0]);count++;offset+=512+Math.ceil(size/512)*512;}
  assert(count>=2,'actual archive entries inspected');
 }finally{fs.rmSync(dir,{recursive:true,force:true})}
});
const end=source.indexOf('console.log(value);',source.indexOf('hotfix_revision='))+'console.log(value);'.length;
const start=source.lastIndexOf('  const fs = require("fs");',end);
const guard=source.slice(start,end);assert(guard.includes('spec.revision'));
for(const [name,reason,pass] of [['stable','Replace the earlier candidate with bounded compatibility protection.',true],['numeric','Replace candidate 3.',false],['path','Replace runtime/helper.',false]])test('actual pre-sign revision guard '+name,()=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'cg-reason-'));try{
  const file=path.join(dir,'spec.json');fs.writeFileSync(file,JSON.stringify({revision:1,supersedes_artifact:{file:'release/3.1.7.9/old.cgpatch',sha256:'a'.repeat(64),reason}}));const run=spawnSync(process.execPath,['-e',guard,file],{encoding:'utf8'});assert.equal(run.status===0,pass,run.stderr);
 }finally{fs.rmSync(dir,{recursive:true,force:true})}
});
