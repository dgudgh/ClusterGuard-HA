// Evidence inventory validation; this does not execute site tests or verify a package signature.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const { execFileSync } = require('node:child_process');
const ids = prefix => Array.from({length:prefix==='ART'?13:12},(_,i)=>`${prefix}-${String(i+1).padStart(3,'0')}`);
const digest = bytes => crypto.createHash('sha256').update(bytes).digest('hex');
function sourceDigest(root) {
  const untracked=execFileSync('git',['ls-files','--others','--exclude-standard','-z'],{cwd:root,encoding:'utf8'}).split('\0').filter(file=> /^(adapters|cmd|internal|pkg|scripts|tools|configs|packaging|deploy|hotfixes)\//.test(file) && /\.(go|sh|cjs|mjs|py)$/.test(file));
  if(untracked.length) throw Error('untracked source must be committed before acceptance: '+untracked.join(', '));
  const files=execFileSync('git',['ls-files','-z'],{cwd:root,encoding:'utf8'}).split('\0').filter(file =>
    /^(adapters|cmd|internal|pkg|scripts|tools|configs|packaging|deploy|hotfixes)\//.test(file) || ['go.mod','go.sum','docs/upgrade-validation-chain.md','docs/zh-CN/upgrade-validation-chain.md'].includes(file));
  if (!files.length) throw Error('source inventory is empty');
  const hash=crypto.createHash('sha256');
  for (const file of files.sort()) {
    const absolute=path.join(root,file);
    if (!fs.lstatSync(absolute).isFile()) throw Error(`source is not a regular file: ${file}`);
    hash.update(file+'\0'+digest(fs.readFileSync(absolute))+'\n');
  }
  return hash.digest('hex');
}
function keys(value,required,optional=[]) {
  if (!value || typeof value!=='object' || Array.isArray(value)) throw Error('expected an object');
  if(required.some(key=>!Object.hasOwn(value,key)) || Object.keys(value).some(key=>![...required,...optional].includes(key))) throw Error('missing or unknown report field');
}
function nonempty(value) { if(typeof value!=='string'||!value.trim()) throw Error('empty evidence identity'); }
function validateReport(root,reportFile,stage) {
  if(!['artifact','field'].includes(stage)) throw Error('invalid acceptance stage');
  const report=JSON.parse(fs.readFileSync(reportFile,'utf8'));
  keys(report,['schema_version','contract_sha256','source_sha256','package_id','artifacts','checks','recorded_by','recorded_at'],['field']);
  if(report.schema_version!==1) throw Error('unsupported evidence schema');
  if(report.contract_sha256!==digest(fs.readFileSync(path.join(root,'docs/upgrade-validation-chain.md')))) throw Error('evidence contract digest is stale');
  if(report.source_sha256!==sourceDigest(root)) throw Error('evidence source digest is stale');
  nonempty(report.package_id); nonempty(report.recorded_by); nonempty(report.recorded_at);
  const recorded=Date.parse(report.recorded_at);
  if(!Number.isFinite(recorded)||recorded>Date.now()+300000) throw Error('invalid evidence timestamp');
  const base=path.dirname(path.resolve(reportFile));
  function verifyFile(item) {
    keys(item,['path','sha256']);nonempty(item.path);
    if(!/^[a-f0-9]{64}$/.test(item.sha256)) throw Error('invalid evidence digest');
    const file=path.resolve(base,item.path);
    if(!fs.lstatSync(file).isFile()) throw Error('evidence is not a regular file');
    if(fs.statSync(file).size===0 || digest(fs.readFileSync(file))!==item.sha256) throw Error(`evidence digest mismatch: ${item.path}`);
  }
  if(!Array.isArray(report.artifacts)||!report.artifacts.length) throw Error('missing package artifacts');
  for(const artifact of report.artifacts) verifyFile(artifact);
  if(!report.artifacts.some(item=>/\.(cgupgrade|cgpatch)$/.test(item.path))) throw Error('missing signed update artifact');
  if(!Array.isArray(report.checks)) throw Error('missing check inventory');
  const seen=new Set(); const allowed=[...ids('ART'),...ids('FIELD')];
  for(const check of report.checks) {
    keys(check,['id','status','evidence']);
    if(!allowed.includes(check.id)||seen.has(check.id)) throw Error('unknown or duplicate acceptance check');
    if(check.status!=='passed') throw Error(`${check.id} is not passed`);
    if(!Array.isArray(check.evidence)||!check.evidence.length) throw Error(`${check.id} lacks evidence`);
    for(const item of check.evidence) verifyFile(item);
    seen.add(check.id);
  }
  const missing=ids('ART').filter(id=>!seen.has(id));
  if(stage==='field') {
    missing.push(...ids('FIELD').filter(id=>!seen.has(id)));
    keys(report.field,['cluster_id','controller_ids']); nonempty(report.field.cluster_id);
    if(!Array.isArray(report.field.controller_ids)||!report.field.controller_ids.length) throw Error('missing field controller scope');
    report.field.controller_ids.forEach(nonempty);
    if(new Set(report.field.controller_ids).size!==report.field.controller_ids.length) throw Error('duplicate field controller identity');
  }
  if(missing.length) throw Error('missing acceptance checks: '+missing.join(', '));
  return report;
}
module.exports={sourceDigest,digest,ids,validateReport};
