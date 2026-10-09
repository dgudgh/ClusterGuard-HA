// Real page, isolated three-node observations; no updater or production writes.
const {scenarios} = require('./console-update-hotfix-recovery-acceptance.cjs');
const {runConsoleDriver} = require('./console-cdp-harness.cjs');
async function main(){
 let failures=0,count=0;
 for(const width of [1440,390]){
  const {fixture,mock}=scenarios.find(s=>s.name==='a hotfix that succeeded').make();
  const base=structuredClone(mock.snapshot[0]);
  const row=(id,from)=>{const r=structuredClone(base);r.package.patch_id=id;r.package.patch_version=id;r.package.source_version='2.2-105';r.job.patch_id=id;r.job.operation_id='operation-'+id;
   if(from){r.job.from_version=from;r.job.to_version=id;r.job.from_node_versions=['one','two','three'].map(node=>({node,version:from}));}return r;};
  const legacy=row('3.1.1.5');
  const records={uniform:[row('3.1.1.8','3.1.1.7'),legacy],legacy:[row('3.1.1.7'),legacy],mixed:[row('3.1.1.8','3.1.1.7')],missing:[row('3.1.1.8','3.1.1.7')],rollback:[row('3.1.1.8','3.1.1.8')]};
  records.mixed[0].job.from_version='';records.mixed[0].job.from_node_versions[1].version='3.1.1.8';
  records.missing[0].job.from_node_versions[1].version='';
  records.restored=structuredClone(records.rollback);records.restored[0].job.mode='execute';records.restored[0].job.status='rolled_back';records.restored[0].job.to_version='3.1.1.7';
  records.rollback[0].job.mode='rollback';records.rollback[0].job.status='rolled_back';records.rollback[0].job.to_version='';
  // The same grid has to stay honest for the rolling packages the bootstrap wording
  // belongs to: one that carries the RPM upgrade bootstrapper, one that predates it.
  const rolling=id=>{const r=row(id);r.package.kind='upgrade';r.package.target_version=id;return r;};
  records.rolling=[(()=>{const r=rolling('3.1.1.9');r.package.bootstrap_available=true;r.package.bootstrap_protocol=1;return r;})()];
  records.legacyRolling=[rolling('3.1.1.9')];
  mock.snapshot=structuredClone(records.uniform);
  // The node build metadata the panel must read its running version from. It is
  // deliberately unlike any uploaded record: the baseline these packages declare is
  // 2.2-105 and their targets are 3.1.1.x, so a running version of 3.1.2.2 can only
  // have come from the node itself.
  const RUNNING_PRODUCT='3.1.2.2';
  const originalHook=fixture.control.hook;fixture.control.hook=async args=>{
   if(args.url.pathname==='/api/__version/case'){mock.snapshot=structuredClone(records[args.url.searchParams.get('key')]);return {result:true};}
   if(args.url.pathname==='/api/v1/platform/version'){return {result:{product_version:RUNNING_PRODUCT,version:'2.2',release:'105',rpm_architecture:'x86_64',architecture:'x86_64'}};}
   return originalHook(args);
  };
  const driver=`(async()=>{
   const out=[];const check=(name,ok,detail)=>out.push({name,ok,detail});
   const byId=id=>document.getElementById(id);const sleep=ms=>new Promise(r=>setTimeout(r,ms));
   for(let i=0;i<300&&!canAdministerPlatform();i++)await sleep(25);if(!canAdministerPlatform())throw Error('admin not ready');
   const expectations={restored:['3.1.1.8 → 3.1.1.7','3.1.1.8 → 3.1.1.7'],uniform:['3.1.1.7 → 3.1.1.8','3.1.1.7 → 3.1.1.8'],legacy:['执行前版本未记录 → 3.1.1.7','Pre-operation version not recorded → 3.1.1.7'],mixed:['节点版本不一致 → 3.1.1.8','Mixed node versions → 3.1.1.8'],missing:['执行前版本未记录 → 3.1.1.8','Pre-operation version not recorded → 3.1.1.8'],rollback:['3.1.1.8 → 回退目标未记录','3.1.1.8 → Rollback target not recorded']};
   for(const [key,expected] of Object.entries(expectations)){
    await fetch('/api/__version/case?key='+key);await loadSoftwareUpdates(false);await sleep(75);
    const raw=JSON.stringify(state.softwareUpdates.packages);
    for(const [index,lang] of ['zh-CN','en-US'].entries()){
     byId('language-select').value=lang;byId('language-select').dispatchEvent(new Event('change',{bubbles:true}));
     const cell=document.querySelector('#software-update-history tr td:nth-child(3)');
     check(key+' '+lang+' operation transition',cell.querySelector('div').textContent===expected[index],cell.textContent);
     check(key+' '+lang+' baseline separately identified',cell.querySelector('small').textContent===(index?'RPM baseline: 2.2-105':'RPM基线：2.2-105'),cell.textContent);
     check(key+' '+lang+' progress agrees with history',byId('software-update-progress-version').textContent===expected[index],byId('software-update-progress-version').textContent);
     check(key+' '+lang+' does not rewrite API evidence',JSON.stringify(state.softwareUpdates.packages)===raw,'original records preserved');
    check(key+' '+lang+' not baseline as transition',!cell.querySelector('div').textContent.includes('2.2-105 →'),'RPM eligibility is independent');
    // The upload preview must answer "what am I running now" from the node's own build
    // metadata, and the answer must not move when a package is selected: the RPM
    // compatibility baseline sits next to it and is a different fact.
    check(key+' '+lang+': running version stays independent of the uploaded record',
      byId('software-update-running-version').textContent===${JSON.stringify(RUNNING_PRODUCT)} && byId('software-update-package-running-version').textContent===${JSON.stringify(RUNNING_PRODUCT)},
      'summary='+byId('software-update-running-version').textContent+' preview='+byId('software-update-package-running-version').textContent);
    check(key+' '+lang+': the preview keeps the RPM baseline apart from the running version',
      byId('software-update-source-version').textContent==='2.2-105' && byId('software-update-source-version').textContent!==byId('software-update-package-running-version').textContent,
      'baseline='+byId('software-update-source-version').textContent+' running='+byId('software-update-package-running-version').textContent);
    // A hotfix carries no RPM upgrade bootstrapper, so the signature cell must report
    // the verification, not a bootstrap fact it can never have.
    check(key+' '+lang+': a hotfix is never a legacy compatible package',
      byId('software-update-signature').textContent===(index?'Passed · Hotfix':'已通过 · 热修补丁'),
      byId('software-update-signature').textContent);
   }
    if(key==='legacy')check('does not infer from previous successful row',!document.querySelector('#software-update-history tr td:nth-child(3) div').textContent.startsWith('3.1.1.5'),'previous row is not evidence');
   }
   // The bootstrap wording belongs to the rolling packages it describes: a modern
   // rolling package keeps its bootstrapper, and a historical one that predates the
   // bootstrapper keeps the legacy wording. Neither claim may drift onto a hotfix.
   for(const entry of [['rolling','已通过 · 引导器 v1','Passed · Bootstrap v1'],['legacyRolling','已通过 · 历史兼容包','Passed · Legacy compatible package']]){
    const key=entry[0],zh=entry[1],en=entry[2];
    for(const [index,lang] of ['zh-CN','en-US'].entries()){
     byId('language-select').value=lang;byId('language-select').dispatchEvent(new Event('change',{bubbles:true}));
     await fetch('/api/__version/case?key='+key);await loadSoftwareUpdates(false);await sleep(75);
     check(key+' '+lang+': the bootstrap wording stays with the rolling package',byId('software-update-signature').textContent===(index?en:zh),byId('software-update-signature').textContent);
    }
   }
   return out;
  })()`;
  const run=await runConsoleDriver({fixture,driver,hash:'#settings',viewport:{width,height:900},profilePrefix:'cg-version-transition-'});
  if(!run)throw Error('browser NOT RUN');if(run.pageErrors.length)throw Error(run.pageErrors.join('\n'));
  for(const c of run.outcome){count++;console.log((c.ok?'PASS':'FAIL')+' '+width+'px '+c.name+': '+c.detail);if(!c.ok)failures++;}
  if(mock.actions.length)throw Error('version read reached action');
 }
 console.log(count+' browser assertions, '+failures+' failures; no execution.');if(failures)process.exitCode=1;
}
if(require.main===module)main().catch(e=>{console.error(e);process.exitCode=1;});
