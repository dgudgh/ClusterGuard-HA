const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),os=require('node:os'),{spawnSync}=require('node:child_process');
const repo=path.resolve(__dirname,'..'),source=fs.readFileSync(path.join(repo,'scripts/clusterguard-upgrade.sh'),'utf8');
const fn=name=>{const m=source.match(new RegExp('^'+name+'\\(\\) \\{\\n.*?^\\}','ms'));assert(m,'missing '+name);return m[0]};
const actual=['run_hotfix_update','prepare_runtime_compatibility','verify_runtime_compatibility','read_cluster_runtime_floor','rollback_hotfix_nodes','restore_hotfix_on_node'].map(fn).join('\n');
function scenario({floor=1,target=0,raise=false,unknown=false,auto=false}={}){
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'cg-floor-runner-'));try{
 const manifest=path.join(dir,'HOTFIX-MANIFEST.json');fs.writeFileSync(manifest,JSON.stringify({files:[{restart_unit:'clusterguard-ha.service'}]}));
 const script=`set -euo pipefail
${actual}
trace() { printf '%s\\n' "$*" >> "$FIXTURE/trace"; }
patch_id=policy-patch;source_version=2.2-113;target_version=2.2-113+policy-patch;package_kind=hotfix
resume_requested=false;rollback_requested=true;retry_requested=false;execute=true;assume_yes=true
current_patch_maintenance_active=false;remote_stage=/isolated;mutation_guard=:
work_dir="$FIXTURE";patch_root="$FIXTURE";patch_file="$FIXTURE/HOTFIX-MANIFEST.json";leader_host=node-a
controllers=(node-a node-b node-c);data_nodes=(node-a node-b node-c);all_nodes=(node-a node-b node-c);updated_nodes=();ordered_nodes=()
runtime_required_floor='{"protocol":0,"instance_failover_policy":0}'
floor_raised=false
load_nodes(){ :; };configure_passwords(){ :; };configure_known_hosts(){ :; };load_runtime_data_members(){ :; };log_all_node_service_facts(){ :; }
detect_current_update_lock(){ :; };detect_foreign_update_lock(){ :; };write_hotfix_state_probe(){ :; };remote_package_version(){ printf '2.2-113'; };sha256_file(){ printf 'fixture-digest'; }
verify_cluster_idle(){ :; };wait_cluster_idle(){ :; };is_controller(){ return 0; };log(){ :; };die(){ trace "REJECT $*";exit 71; }
stage_hotfix_on_node(){ trace "stage $1"; }
runtime_compatibility_on_node(){
 trace "check $1 $2"
 if [[ "$2" == requirements ]];then
  if ${unknown};then printf '{"protocol":2,"instance_failover_policy":1}';
  elif ${raise} && ! $floor_raised;then printf '{"protocol":0,"instance_failover_policy":0}';
  else printf '{"protocol":${floor},"instance_failover_policy":${floor}}';fi
 elif [[ "$runtime_required_floor" == *'"protocol":1'* && ${target} == 0 ]];then return 1;fi
}
publish_update_artifacts(){ trace publish; };acquire_update_locks(){ trace LOCK; };capture_operation_versions(){ :; };write_journal(){ :; }
all_controllers_support_replicated_gate(){ return 0; };acquire_replicated_update_gate(){ floor_raised=true;trace RAFT_LOCK; };finish_update_maintenance(){ trace RELEASE; }
remote_run(){ trace RESTORE; };restart_hotfix_units(){ :; };wait_node_ready(){ :; }
${auto ? `runtime_required_floor='{"protocol":1,"instance_failover_policy":1}';restore_hotfix_on_node node-a /isolated/policy-patch clusterguard-ha.service` : 'run_hotfix_update'}
`;
 const file=path.join(dir,'probe.sh');fs.writeFileSync(file,script);const run=spawnSync('bash',[file],{cwd:dir,env:{...process.env,FIXTURE:dir},encoding:'utf8',timeout:10000});const trace=fs.existsSync(path.join(dir,'trace'))?fs.readFileSync(path.join(dir,'trace'),'utf8'):'';assert(!run.error,String(run.error));return{status:run.status,trace,stderr:run.stderr};
 }finally{fs.rmSync(dir,{recursive:true,force:true})}
}
test('saved policy rejects unknown original target before locks or restore',()=>{const r=scenario();assert.equal(r.status,71,r.stderr);assert(!r.trace.includes('\nLOCK'));assert(!r.trace.includes('RESTORE'));assert(r.trace.includes('requirements'))});
test('compatible original target is allowed and all three nodes restore',()=>{const r=scenario({target:1});assert.equal(r.status,0,r.stderr);assert.equal(r.trace.split('\n').filter(x=>x==='RESTORE').length,3)});
test('never-saved policy does not prohibit legacy rollback',()=>{const r=scenario({floor:0});assert.equal(r.status,0,r.stderr);assert.equal(r.trace.split('\n').filter(x=>x==='RESTORE').length,3)});
test('unknown cluster floor fails before maintenance',()=>{const r=scenario({unknown:true});assert.equal(r.status,71,r.stderr);assert(!r.trace.includes('\nLOCK'));assert(!r.trace.includes('RESTORE'))});
test('floor raised before replicated lock is re-read before first replacement',()=>{const r=scenario({raise:true});assert.equal(r.status,71,r.stderr);assert(r.trace.includes('RAFT_LOCK'));assert(!r.trace.includes('RESTORE'))});
test('automatic rollback repeats the same actual target guard',()=>{const r=scenario({auto:true});assert.notEqual(r.status,0);assert(!r.trace.includes('RESTORE'))});
test('generated apply and rollback guard precedes backup creation or restoration',()=>{
 const build=fs.readFileSync(path.join(repo,'scripts/build-hotfix-patch.sh'),'utf8');const m=build.match(/cat >"\$\{render_js\}" <<'RENDER_JS'\n([\s\S]*?)\nRENDER_JS/);assert(m);const dir=fs.mkdtempSync(path.join(os.tmpdir(),'cg-floor-render-'));try{
 const paths=['manifest.json','apply.sh','rollback.sh'].map(n=>path.join(dir,n));fs.writeFileSync(paths[0],JSON.stringify({hotfix_id:'fixture',build_commit:'0123456789012345678901234567890123456789',fix_commits:[],source:{version:'2.2',release:'113'},files:[],verification:[]}));const js=path.join(dir,'render.cjs');fs.writeFileSync(js,m[1]);const r=spawnSync(process.execPath,[js,paths[0],'',paths[1],paths[2]],{encoding:'utf8'});assert.equal(r.status,0,r.stderr);
 for(const [i,action] of [[1,'execute'],[2,'rollback']]){const s=fs.readFileSync(paths[i],'utf8'),at=s.indexOf('runtime-compatibility check');assert(at>=0);assert(s.includes('--mode '+action));if(i===1)assert(at<s.indexOf('mkdir -p "${backup_dir}"'));else assert(at<s.indexOf('restore_backup()'));const b=spawnSync('bash',['-n',paths[i]],{encoding:'utf8'});assert.equal(b.status,0,b.stderr)}
 }finally{fs.rmSync(dir,{recursive:true,force:true})}
});
test('remote compatibility command preserves remote argv and has valid shell syntax',()=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'cg-floor-command-'));try{
 const file=path.join(dir,'probe.sh'),capture=path.join(dir,'remote.sh');fs.writeFileSync(file,`set -euo pipefail\n${fn('runtime_compatibility_on_node')}\nremote_stage=/isolated;package_kind=hotfix;patch_id=bound-patch;runtime_required_floor='{"protocol":1,"instance_failover_policy":1}'\nremote_run(){ printf '%s' "$2" > "$CAPTURE"; }\nruntime_compatibility_on_node node-a rollback\n`);
 const r=spawnSync('bash',[file],{env:{...process.env,CAPTURE:capture},encoding:'utf8'});assert.equal(r.status,0,r.stderr);const command=fs.readFileSync(capture,'utf8');assert(command.includes('supports() { "$1"'));assert(command.includes('"${helper}" runtime-compatibility check'));assert(command.includes('--patch-id \'bound-patch\''));assert(!command.includes('\\"'));const check=spawnSync('bash',['-n',capture],{encoding:'utf8'});assert.equal(check.status,0,check.stderr);
 }finally{fs.rmSync(dir,{recursive:true,force:true})}
});
test('rolling automatic rollback checks source RPM, not requested upgrade target',()=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'cg-floor-rpm-runner-'));try{
 const file=path.join(dir,'probe.sh');fs.writeFileSync(file,`set -euo pipefail
${fn('install_node_rpm')}
remote_stage=/isolated;patch_id=bound-patch;source_version=old;source_product_version=2.2;source_release=113;source_state_format=1;source_update_protocol=1
target_product_version=2.2;target_release=114;target_state_format=1;target_update_protocol=1;mutation_guard=:
leader_host=node-a;upgrade_lock_name=/isolated/lock;execution_id=fixture
remote_run(){ :; };remote_copy(){ :; };assert_update_lock_ownership(){ :; };is_controller(){ return 1; };is_data_node(){ return 1; }
read_cluster_runtime_floor(){ printf 'READ_FLOOR\\n'; }
runtime_compatibility_on_node(){ printf 'TARGET %s\\n' "$2"; return 71; }
install_node_rpm node-a /isolated/source.rpm old
`);
 const r=spawnSync('bash',[file],{encoding:'utf8'});assert.notEqual(r.status,0,r.stderr);assert(r.stdout.includes('READ_FLOOR'));assert(r.stdout.includes('TARGET rollback'));assert(!r.stdout.includes('TARGET execute'));
 }finally{fs.rmSync(dir,{recursive:true,force:true})}
});
